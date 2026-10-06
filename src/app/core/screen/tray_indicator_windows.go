//go:build windows

package screen

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Indicador nativo na MÁQUINA ACESSADA: ícone na bandeja (notification area) do
// usuário da sessão interativa + balloon informativo.
//
// Por que nativo e não pela UI do agent: a sessão remota SEMPRE roda no worker
// spawnado pelo serviço (SYSTEM na sessão interativa), nunca na UI companion
// (decisão documentada em remote_debug_commands.go). O serviço de notificações
// do Wails vive na UI e não alcança o worker.
//
// O ícone é removido no Hide/Close e, se o worker morrer, o shell limpa ícones
// de processos mortos — o indicador some junto com o bloqueio.
const (
	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifIcon = 0x00000002
	nifTip  = 0x00000004
	nifInfo = 0x00000010

	niifInfo = 0x00000001

	trayIconID    = 1
	idiInfoIcon   = 32516 // IDI_INFORMATION: ícone de sistema (sem asset embutido)
	trayTooltip   = "Entrada bloqueada pelo acesso remoto"
	trayBalloon   = "Teclado e mouse bloqueados pelo suporte remoto"
	trayInfoTitle = "Acesso remoto Discovery"
)

var (
	shell32DLL           = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIconW = shell32DLL.NewProc("Shell_NotifyIconW")
	procRegisterClassExW = user32DLL.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32DLL.NewProc("CreateWindowExW")
	procDestroyWindow    = user32DLL.NewProc("DestroyWindow")
	procDefWindowProcW   = user32DLL.NewProc("DefWindowProcW")
	procLoadIconW        = user32DLL.NewProc("LoadIconW")
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type notifyIconGUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// notifyIconDataW espelha NOTIFYICONDATAW (Vista+). O teste de layout garante
// que o tamanho bate com o da API (976 bytes em x64) — cbSize errado faz o
// shell recusar a chamada.
type notifyIconDataW struct {
	cbSize            uint32
	hWnd              uintptr
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	hIcon             uintptr
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uTimeoutOrVersion uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
	guidItem          notifyIconGUID
	hBalloonIcon      uintptr
}

var (
	trayClassOnce sync.Once
	trayClassErr  error
	trayClassName = "DiscoveryRmmRemoteInputLock"
)

// TrayIndicator é um indicador de bandeja com balloon, criado na sessão do
// usuário que o worker está atendendo.
type TrayIndicator struct {
	mu     sync.Mutex
	hwnd   uintptr
	hIcon  uintptr
	added  bool
	closed bool
	stopCh chan struct{}
	doneCh chan struct{}
}

func trayWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return ret
}

func ensureTrayWindowClass() error {
	trayClassOnce.Do(func() {
		className, err := windows.UTF16PtrFromString(trayClassName)
		if err != nil {
			trayClassErr = err
			return
		}
		wc := wndClassExW{
			cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
			lpfnWndProc:   windows.NewCallback(trayWndProc),
			lpszClassName: className,
		}
		ret, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		// ERROR_CLASS_ALREADY_EXISTS (1410): outra instância já registrou a
		// classe no processo — não é erro.
		if ret == 0 && errnoOf(callErr) != 1410 {
			trayClassErr = fmt.Errorf("RegisterClassExW falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
		}
	})
	return trayClassErr
}

// NewTrayIndicator cria a janela oculta e mantém viva a thread dona dela (a
// janela é destruída quando a thread termina). Não cria o ícone ainda: Show()
// faz o NIM_ADD com o balloon.
func NewTrayIndicator() (*TrayIndicator, error) {
	if err := ensureTrayWindowClass(); err != nil {
		return nil, err
	}
	className, err := windows.UTF16PtrFromString(trayClassName)
	if err != nil {
		return nil, err
	}
	windowName, err := windows.UTF16PtrFromString("Discovery RMM — indicador de entrada bloqueada")
	if err != nil {
		return nil, err
	}

	t := &TrayIndicator{stopCh: make(chan struct{}), doneCh: make(chan struct{})}
	ready := make(chan error, 1)

	go func() {
		// A janela pertence a esta thread; ela precisa sobreviver enquanto o
		// ícone existir (ao sair, o Windows destrói as janelas da thread).
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(t.doneCh)

		hwnd, _, callErr := procCreateWindowExW.Call(
			0, // dwExStyle
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(windowName)),
			0, // dwStyle: sem WS_VISIBLE — janela oculta de mensagens
			0, 0, 0, 0,
			0, 0, 0, 0,
		)
		if hwnd == 0 {
			ready <- fmt.Errorf("CreateWindowExW falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
			return
		}
		icon, _, _ := procLoadIconW.Call(0, idiInfoIcon)

		t.mu.Lock()
		t.hwnd = hwnd
		t.hIcon = icon
		t.mu.Unlock()
		ready <- nil

		<-t.stopCh // mantém a thread (e a janela) vivas até Close()

		t.mu.Lock()
		added := t.added
		t.added = false
		t.mu.Unlock()
		if added {
			nid := notifyIconDataW{
				cbSize: uint32(unsafe.Sizeof(notifyIconDataW{})),
				hWnd:   hwnd,
				uID:    trayIconID,
			}
			_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		}
		_, _, _ = procDestroyWindow.Call(hwnd)
	}()

	if err := <-ready; err != nil {
		return nil, err
	}
	return t, nil
}

// Show cria/atualiza o ícone e exibe o balloon informando o bloqueio.
func (t *TrayIndicator) Show(message string) error {
	t.mu.Lock()
	hwnd, hIcon, added, closed := t.hwnd, t.hIcon, t.added, t.closed
	t.mu.Unlock()
	if closed || hwnd == 0 {
		return fmt.Errorf("indicador indisponível")
	}
	if message == "" {
		message = trayBalloon
	}

	nid := notifyIconDataW{
		cbSize: uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:   hwnd,
		uID:    trayIconID,
		hIcon:  hIcon,
		uFlags: nifIcon | nifTip | nifInfo,
		// NOTA: sem NIF_MESSAGE de propósito — não precisamos de cliques; isso
		// dispensa um message loop na thread do indicador.
		dwInfoFlags: niifInfo,
	}
	copyUTF16(nid.szTip[:], trayTooltip)
	copyUTF16(nid.szInfoTitle[:], trayInfoTitle)
	copyUTF16(nid.szInfo[:], message)

	op := uintptr(nimModify)
	if !added {
		op = nimAdd
	}
	ret, _, callErr := procShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&nid)))
	if ret == 0 {
		return fmt.Errorf("Shell_NotifyIconW falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
	}
	t.mu.Lock()
	t.added = true
	t.mu.Unlock()
	return nil
}

// Hide remove o ícone da bandeja (mantém a janela viva para um novo bloqueio).
func (t *TrayIndicator) Hide() error {
	t.mu.Lock()
	if !t.added || t.hwnd == 0 {
		t.mu.Unlock()
		return nil
	}
	t.added = false
	nid := notifyIconDataW{
		cbSize: uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:   t.hwnd,
		uID:    trayIconID,
	}
	t.mu.Unlock()

	ret, _, callErr := procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	if ret == 0 {
		return fmt.Errorf("Shell_NotifyIconW(DELETE) falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
	}
	return nil
}

// Close remove o ícone, destrói a janela e encerra a thread do indicador.
// Idempotente.
func (t *TrayIndicator) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
	close(t.stopCh)
	<-t.doneCh
}

// copyUTF16 copia s para dst como UTF-16 com terminador NUL, truncando se
// necessário (o shell exige buffer terminado em NUL).
func copyUTF16(dst []uint16, s string) {
	if len(dst) == 0 {
		return
	}
	src, err := windows.UTF16FromString(s)
	if err != nil {
		dst[0] = 0
		return
	}
	n := copy(dst[:len(dst)-1], src)
	dst[n] = 0
}
