//go:build windows

package screenshot

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Enumeração de janelas (Win32) ─────────────────────────────────────────
//
// Usada pela tool MCP list_open_windows (o LLM descobre o que está aberto e
// escolhe a janela alvo) e pelo overlay de seleção (destaque da janela sob o
// cursor).

const (
	GWL_EXSTYLE                 = -20
	WS_EX_TOOLWINDOW            = 0x00000080
	DWMWA_EXTENDED_FRAME_BOUNDS = 9
	DWMWA_CLOAKED               = 14
	maxWindowTitle              = 512
)

// gwlExStyleIndex é GWL_EXSTYLE (-20) como uintptr (Call é uintptr).
var gwlExStyleIndex = ^uintptr(19)

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

var (
	user32S   = syscall.NewLazyDLL("user32.dll")
	kernel32S = syscall.NewLazyDLL("kernel32.dll")
	dwmapiS   = syscall.NewLazyDLL("dwmapi.dll")

	procEnumWindowsS                = user32S.NewProc("EnumWindows")
	procIsWindowVisibleS            = user32S.NewProc("IsWindowVisible")
	procIsIconicS                   = user32S.NewProc("IsIconic")
	procGetWindowTextWS             = user32S.NewProc("GetWindowTextW")
	procGetWindowTextLengthWS       = user32S.NewProc("GetWindowTextLengthW")
	procGetWindowRectS              = user32S.NewProc("GetWindowRect")
	procGetWindowThreadProcessIdS   = user32S.NewProc("GetWindowThreadProcessId")
	procGetForegroundWindowS        = user32S.NewProc("GetForegroundWindow")
	procGetWindowLongPtrWS          = user32S.NewProc("GetWindowLongPtrW")
	procQueryFullProcessImageNameWS = kernel32S.NewProc("QueryFullProcessImageNameW")
	procDwmGetWindowAttributeS      = dwmapiS.NewProc("DwmGetWindowAttribute")
)

var (
	windowsAccumMu sync.Mutex
	windowsAccum   []WindowInfo
	// enumMu serializa chamadas concorrentes de ListWindows: o acumulador é
	// global (o callback do EnumWindows é registrado uma única vez por processo)
	// e duas enumerações simultâneas intercalariam janelas de ambas.
	enumMu sync.Mutex
)

const (
	// maxListedWindows limita o payload entregue ao LLM e ao overlay.
	maxListedWindows = 60
	// maxWindowTitleRunes evita títulos patológicos (alguns apps usam o título
	// como buffer de status, com dezenas de KB).
	maxWindowTitleRunes = 160
)

// enumWindowsCallback é criado UMA vez por processo: syscall.NewCallback
// registra um callback permanente no runtime (limite ~2000/processo).
var enumWindowsCallback = syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
	info, ok := describeWindow(hwnd)
	if !ok {
		return 1 // continua a enumeração
	}
	windowsAccumMu.Lock()
	info.ZOrder = len(windowsAccum)
	windowsAccum = append(windowsAccum, info)
	windowsAccumMu.Unlock()
	return 1
})

func describeWindow(hwnd uintptr) (WindowInfo, bool) {
	if visible, _, _ := procIsWindowVisibleS.Call(hwnd); visible == 0 {
		return WindowInfo{}, false
	}
	// Janelas cloaked (outro virtual desktop, UWP suspensa) não são visíveis.
	var cloaked uint32
	if hr, _, _ := procDwmGetWindowAttributeS.Call(hwnd, DWMWA_CLOAKED, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked)); hr == 0 && cloaked != 0 {
		return WindowInfo{}, false
	}
	if ex, _, _ := procGetWindowLongPtrWS.Call(hwnd, gwlExStyleIndex); ex&WS_EX_TOOLWINDOW != 0 {
		return WindowInfo{}, false
	}
	title := windowTitle(hwnd)
	if title == "" {
		return WindowInfo{}, false
	}
	x, y, w, h, ok := windowBounds(hwnd)
	if !ok || w <= 0 || h <= 0 {
		return WindowInfo{}, false
	}
	var pid uint32
	procGetWindowThreadProcessIdS.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	fg, _, _ := procGetForegroundWindowS.Call()
	minimized, _, _ := procIsIconicS.Call(hwnd)
	return WindowInfo{
		Handle:      uint64(hwnd),
		Title:       title,
		ProcessName: processName(pid),
		PID:         pid,
		X:           x,
		Y:           y,
		Width:       w,
		Height:      h,
		Minimized:   minimized != 0,
		Foreground:  fg == hwnd,
		IsSelf:      pid == uint32(os.Getpid()),
	}, true
}

func windowTitle(hwnd uintptr) string {
	length, _, _ := procGetWindowTextLengthWS.Call(hwnd)
	if length == 0 {
		return ""
	}
	buf := make([]uint16, maxWindowTitle)
	n, _, _ := procGetWindowTextWS.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return truncateRunes(windows.UTF16ToString(buf[:n]), maxWindowTitleRunes)
}

// truncateRunes corta a string em max runes (Unicode-safe).
func truncateRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// windowBounds usa o retângulo estendido do DWM (exclui sombra/área invisível)
// com fallback para GetWindowRect.
func windowBounds(hwnd uintptr) (int, int, int, int, bool) {
	var r winRect
	if hr, _, _ := procDwmGetWindowAttributeS.Call(hwnd, DWMWA_EXTENDED_FRAME_BOUNDS, uintptr(unsafe.Pointer(&r)), unsafe.Sizeof(r)); hr == 0 {
		return int(r.Left), int(r.Top), int(r.Right - r.Left), int(r.Bottom - r.Top), true
	}
	if ret, _, _ := procGetWindowRectS.Call(hwnd, uintptr(unsafe.Pointer(&r))); ret == 0 {
		return 0, 0, 0, 0, false
	}
	return int(r.Left), int(r.Top), int(r.Right - r.Left), int(r.Bottom - r.Top), true
}

func processName(pid uint32) string {
	if pid == 0 {
		return ""
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	ret, _, _ := procQueryFullProcessImageNameWS.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ret == 0 {
		return ""
	}
	full := windows.UTF16ToString(buf[:size])
	return filepath.Base(full)
}

// ListWindows retorna as janelas visíveis de nível superior, em ordem de
// z-order (índice 0 = mais à frente).
func ListWindows() ([]WindowInfo, error) {
	enumMu.Lock()
	defer enumMu.Unlock()

	windowsAccumMu.Lock()
	windowsAccum = windowsAccum[:0]
	windowsAccumMu.Unlock()

	ret, _, err := procEnumWindowsS.Call(enumWindowsCallback, 0)
	if ret == 0 {
		return nil, err
	}
	windowsAccumMu.Lock()
	count := len(windowsAccum)
	if count > maxListedWindows {
		count = maxListedWindows
	}
	out := append([]WindowInfo(nil), windowsAccum[:count]...)
	windowsAccumMu.Unlock()
	return out, nil
}
