//go:build windows

package automation

import (
	"context"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// userLoginEvent é disparado quando um usuário faz logon interativo (tipo LogonType Interactive/RemoteInteractive).
// Consumido pelo automation.Service para executar tasks TriggerOnUserLogin de verdade (por evento de sessão).
type userLoginEvent struct {
	SessionID uint32
	UserName  string
}

// startUserLoginWatcher registra WTSRegisterSessionNotification e escuta mensagens de sessão
// em uma message loop dedicada. Retorna um canal de eventos e uma função de cleanup.
// Em caso de falha no registro (ex.: serviço sem acesso a winsta), retorna canal nil —
// o caller mantém o fallback "uma vez por processo".
func startUserLoginWatcher(ctx context.Context) (<-chan userLoginEvent, func()) {
	hwnd, err := createMessageWindow()
	if err != nil {
		return nil, func() {}
	}

	if !wtsRegisterSessionNotification(hwnd, 0) { // NOTIFY_FOR_THIS_SESSION = 0
		destroyWindow(hwnd)
		return nil, func() {}
	}

	events := make(chan userLoginEvent, 16)
	done := make(chan struct{})

	go func() {
		<-ctx.Done()
		wtsUnRegisterSessionNotification(hwnd)
		// Posta WM_QUIT para encerrar a message loop.
		postThreadMessage(getWindowThreadID(hwnd), 0x0012, 0, 0) // WM_QUIT
	}()

	go func() {
		defer close(done)
		defer destroyWindow(hwnd)
		var msg msgStruct
		for {
			ret, _, _ := procGetMessage.Call(
				uintptr(unsafe.Pointer(&msg)),
				0, 0, 0)
			if ret == 0 || ret == 0xFFFFFFFF {
				return
			}
			if msg.Message == 0x0400+1 { // WM_USER+1 = WTS session change
				// wParam: WTS_SESSION_LOGON (1) / WTS_SESSION_LOGOFF (2); lParam: sessionId
				if msg.WParam == 1 { // WTS_SESSION_LOGON
					select {
					case events <- userLoginEvent{SessionID: uint32(msg.LParam)}:
					default: // não bloqueia se ninguém consumir
					}
				}
			}
		}
	}()

	cleanup := func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return events, cleanup
}

// ── Win32 bindings mínimos (evita dependência extra) ─────────────────────────

type msgStruct struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

var (
	user32                        = windows.NewLazySystemDLL("user32.dll")
	procCreateWindowExW           = user32.NewProc("CreateWindowExW")
	procDestroyWindow             = user32.NewProc("DestroyWindow")
	procGetMessage                = user32.NewProc("GetMessageW")
	procDefWindowProcW            = user32.NewProc("DefWindowProcW")
	procRegisterClassExW          = user32.NewProc("RegisterClassExW")
	procPostThreadMessage         = user32.NewProc("PostThreadMessageW")
	procGetWindowThreadID         = user32.NewProc("GetWindowThreadProcessId")
	wtsapi                        = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSRegisterSessionNotif   = wtsapi.NewProc("WTSRegisterSessionNotification")
	procWTSUnRegisterSessionNotif = wtsapi.NewProc("WTSUnRegisterSessionNotification")
)

func wtsRegisterSessionNotification(hwnd uintptr, flags uint32) bool {
	ret, _, _ := procWTSRegisterSessionNotif.Call(hwnd, uintptr(flags))
	return ret != 0
}

func wtsUnRegisterSessionNotification(hwnd uintptr) {
	_, _, _ = procWTSUnRegisterSessionNotif.Call(hwnd)
}

func destroyWindow(hwnd uintptr) {
	_, _, _ = procDestroyWindow.Call(hwnd)
}

func postThreadMessage(threadID uint32, msg uint32, wParam, lParam uintptr) {
	_, _, _ = procPostThreadMessage.Call(uintptr(threadID), uintptr(msg), wParam, lParam)
}

func getWindowThreadID(hwnd uintptr) uint32 {
	var pid uint32
	tid, _, _ := procGetWindowThreadID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return uint32(tid)
}

func createMessageWindow() (uintptr, error) {
	className, err := utf16Ptr("discovery_automation_watcher")
	if err != nil {
		return 0, err
	}

	// WNDCLASSEX mínimo (message-only window não precisa de brush/cursor).
	type wndClassEx struct {
		CbSize        uint32
		Style         uint32
		LpfnWndProc   uintptr
		CbClsExtra    int32
		CbWndExtra    int32
		HInstance     uintptr
		HIcon         uintptr
		HCursor       uintptr
		HbrBackground uintptr
		LpszMenuName  uintptr
		LpszClassName uintptr
		HIconSm       uintptr
	}

	var wc wndClassEx
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	wc.LpfnWndProc = windows.NewCallback(func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
		return callDefWindowProc(hwnd, msg, wParam, lParam)
	})
	wc.LpszClassName = uintptr(unsafe.Pointer(className))

	_, _, _ = procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	// HWND_MESSAGE = -3 → message-only window (não aparece na taskbar).
	title, _ := utf16Ptr("discovery")
	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		0, 0, 0, 0, 0,
		uintptr(^uintptr(2)), // HWND_MESSAGE = (HWND)-3
		0, 0, 0)
	if hwnd == 0 {
		return 0, err
	}
	return hwnd, nil
}

func callDefWindowProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func utf16Ptr(s string) (*uint16, error) {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil, err
	}
	return p, nil
}

var _ = sync.Mutex{}
