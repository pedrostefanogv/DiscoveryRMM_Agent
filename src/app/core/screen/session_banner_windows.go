//go:build windows

package screen

import (
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Banner local da sessão remota ──
//
// Janela topmost no desktop do usuário acessado, com operador, tempo e botão
// Desconectar. Deve ser EXCLUÍDA da captura (WDA_EXCLUDEFROMCAPTURE): o viewer e
// a gravação não podem ver o banner. Se a exclusão não for suportada pelo
// Windows, a janela NÃO é exibida (melhor sem banner do que vazando no stream).

const (
	wdaExcludeFromCapture = 0x00000011

	wsPopup        = 0x80000000
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsClipSiblings = 0x04000000

	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080

	ssCenter = 0x00000001

	bsPushButton = 0x00000000

	wmDestroy        = 0x0002
	wmTimer          = 0x0113
	wmCommand        = 0x0111
	wmCtlColorStatic = 0x0138
	wmSetFont        = 0x0030
	wmEraseBkgnd     = 0x0014

	swShow = 5

	smCXScreen = 0
	smCYScreen = 1

	colorWindowText = 8
	colorWindowBg   = 5
	transparent     = 1

	dtCenter     = 0x00000001
	dtVCenter    = 0x00000004
	dtSingleLine = 0x00000020
	dtHidden     = 0x00000100
)

var (
	procSetWindowDisplayAffinity = user32DLL.NewProc("SetWindowDisplayAffinity")
	procSetWindowTextW           = user32DLL.NewProc("SetWindowTextW")
	procSetTimer                 = user32DLL.NewProc("SetTimer")
	procKillTimer                = user32DLL.NewProc("KillTimer")
	procShowWindow               = user32DLL.NewProc("ShowWindow")
	procUpdateWindow             = user32DLL.NewProc("UpdateWindow")
	procSendMessageW             = user32DLL.NewProc("SendMessageW")
	procPostQuitMessage          = user32DLL.NewProc("PostQuitMessage")
	procSetBkMode                = gdi32.NewProc("SetBkMode")
	procSetTextColor             = gdi32.NewProc("SetTextColor")
	procCreateSolidBrush         = gdi32.NewProc("CreateSolidBrush")
	procCreateFontW              = gdi32.NewProc("CreateFontW")
	procFillRect                 = user32DLL.NewProc("FillRect")
	procGetClientRect            = user32DLL.NewProc("GetClientRect")
)

type BannerOptions struct {
	Operator        string
	Text            string
	AllowDisconnect bool
	MonitorIndex    int
	OnDisconnect    func()
}

type SessionBanner struct {
	mu       sync.Mutex
	hwnd     uintptr
	textHwnd uintptr
	btnHwnd  uintptr
	start    time.Time
	baseText string
	onClose  func()
	closed   bool
	ready    chan error
	stopCh   chan struct{}
	doneCh   chan struct{}
	font     uintptr
	brush    uintptr
}

type bannerState struct {
	banner *SessionBanner
}

var bannerStates sync.Map

const bannerClassName = "DiscoveryRmmSessionBanner"

var (
	bannerClassOnce sync.Once
	bannerClassErr  error
)

func ensureBannerClass() error {
	bannerClassOnce.Do(func() {
		className, err := windows.UTF16PtrFromString(bannerClassName)
		if err != nil {
			bannerClassErr = err
			return
		}
		wc := wndClassExW{
			cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
			lpfnWndProc:   windows.NewCallback(bannerWndProc),
			lpszClassName: className,
			hbrBackground: 0,
		}
		ret, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if ret == 0 && errnoOf(callErr) != 1410 {
			bannerClassErr = fmt.Errorf("RegisterClassExW(banner) falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
		}
	})
	return bannerClassErr
}

func bannerWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmEraseBkgnd:
		if raw, ok := bannerStates.Load(hwnd); ok {
			if state, ok := raw.(*bannerState); ok && state.banner != nil {
				var rc rect
				procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
				procFillRect.Call(wparam, uintptr(unsafe.Pointer(&rc)), state.banner.brush)
				return 1
			}
		}
		return 0
	case wmTimer:
		if raw, ok := bannerStates.Load(hwnd); ok {
			if state, ok := raw.(*bannerState); ok && state.banner != nil {
				state.banner.refresh()
			}
		}
		return 0
	case wmCommand:
		if raw, ok := bannerStates.Load(hwnd); ok {
			if state, ok := raw.(*bannerState); ok && state.banner != nil && wparam == 1 {
				state.banner.disconnect()
			}
		}
		return 0
	case wmCtlColorStatic:
		if raw, ok := bannerStates.Load(hwnd); ok {
			if state, ok := raw.(*bannerState); ok && state.banner != nil {
				procSetBkMode.Call(lparam, transparent)
				procSetTextColor.Call(lparam, 0x00FFFFFF)
				return state.banner.brush
			}
		}
	case wmDestroy:
		bannerStates.Delete(hwnd)
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return ret
}

// NewSessionBanner cria e exibe o banner na thread dedicada. Retorna erro se a
// janela não puder ser excluída da captura (nesse caso nada é exibido).
func NewSessionBanner(opts BannerOptions) (*SessionBanner, error) {
	if err := ensureBannerClass(); err != nil {
		return nil, err
	}

	banner := &SessionBanner{
		start:    time.Now(),
		baseText: opts.Text,
		onClose:  opts.OnDisconnect,
		ready:    make(chan error, 1),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(banner.doneCh)

		className, _ := windows.UTF16PtrFromString(bannerClassName)
		windowName, _ := windows.UTF16PtrFromString("Discovery RMM — sessão remota")
		screenW, _, _ := procGetSystemMetrics.Call(smCXScreen)
		width := uintptr(720)
		height := uintptr(52)

		// Posiciona no monitor que está sendo compartilhado (monitorIndex do
		// capturer). Sem geometria disponível, cai para o primário.
		originX := int64(0)
		originY := int64(0)
		monitorWidth := int64(screenW)
		if monitors, err := GetMonitors(); err == nil && len(monitors) > 0 {
			index := opts.MonitorIndex
			if index < 0 || index >= len(monitors) {
				index = 0
			}
			originX = int64(monitors[index].X)
			originY = int64(monitors[index].Y)
			monitorWidth = int64(monitors[index].Width)
		}
		if int64(width) > monitorWidth-40 {
			width = uintptr(monitorWidth - 40)
		}
		x := uintptr(originX + (monitorWidth-int64(width))/2)
		y := uintptr(originY + 8)

		hwnd, _, callErr := procCreateWindowExW.Call(
			wsExTopmost|wsExToolWindow,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(windowName)),
			wsPopup|wsClipSiblings,
			x, y, width, height,
			0, 0, 0, 0,
		)
		if hwnd == 0 {
			banner.ready <- fmt.Errorf("CreateWindowExW(banner) falhou: %v (errno=%d)", describeErr(callErr), errnoOf(callErr))
			return
		}
		banner.hwnd = hwnd
		bannerStates.Store(hwnd, &bannerState{banner: banner})

		// Exclusão da captura: sem isso o banner apareceria no viewer/gravação.
		ok, _, affinityErr := procSetWindowDisplayAffinity.Call(hwnd, wdaExcludeFromCapture)
		if ok == 0 {
			procDestroyWindow.Call(hwnd)
			banner.ready <- fmt.Errorf("SetWindowDisplayAffinity falhou: %v (errno=%d)", describeErr(affinityErr), errnoOf(affinityErr))
			return
		}

		// Fonte e controles.
		fontName, _ := windows.UTF16PtrFromString("Segoe UI")
		banner.font, _, _ = procCreateFontW.Call(
			uintptr(^uint32(15)+1), 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0,
			uintptr(unsafe.Pointer(fontName)),
		)
		banner.brush, _, _ = procCreateSolidBrush.Call(0x00202020)

		textPtr, _ := windows.UTF16PtrFromString(banner.currentText())
		textHwnd, _, _ := procCreateWindowExW.Call(
			0, uintptr(unsafe.Pointer(mustUTF16("STATIC"))), uintptr(unsafe.Pointer(textPtr)),
			wsChild|wsVisible|ssCenter,
			12, 12, width-160, 28, hwnd, 0, 0, 0,
		)
		banner.textHwnd = textHwnd
		procSendMessageW.Call(textHwnd, wmSetFont, banner.font, 1)

		if opts.AllowDisconnect {
			btnPtr, _ := windows.UTF16PtrFromString("Desconectar")
			btnHwnd, _, _ := procCreateWindowExW.Call(
				0, uintptr(unsafe.Pointer(mustUTF16("BUTTON"))), uintptr(unsafe.Pointer(btnPtr)),
				wsChild|wsVisible|bsPushButton,
				width-140, 12, 124, 28, hwnd, 1, 0, 0,
			)
			banner.btnHwnd = btnHwnd
			procSendMessageW.Call(btnHwnd, wmSetFont, banner.font, 1)
		}

		procShowWindow.Call(hwnd, swShow)
		procUpdateWindow.Call(hwnd)
		procSetTimer.Call(hwnd, 1, 1000, 0)
		banner.ready <- nil

		var msg struct {
			hwnd    uintptr
			message uint32
			wParam  uintptr
			lParam  uintptr
			time    uint32
			pt      struct{ x, y int32 }
		}
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}

		if banner.font != 0 {
			procDeleteObject.Call(banner.font)
		}
		if banner.brush != 0 {
			procDeleteObject.Call(banner.brush)
		}
	}()

	if err := <-banner.ready; err != nil {
		return nil, err
	}
	return banner, nil
}

func (b *SessionBanner) currentText() string {
	text := b.baseText
	if text == "" {
		text = "Sessão remota em andamento"
	}
	elapsed := time.Since(b.start)
	minutes := int(elapsed.Minutes())
	seconds := int(elapsed.Seconds()) % 60
	return fmt.Sprintf("%s  •  %02d:%02d", text, minutes, seconds)
}

func (b *SessionBanner) refresh() {
	if b == nil || b.textHwnd == 0 {
		return
	}
	textPtr, _ := windows.UTF16PtrFromString(b.currentText())
	procSetWindowTextW.Call(b.textHwnd, uintptr(unsafe.Pointer(textPtr)))
}

func (b *SessionBanner) disconnect() {
	if b == nil {
		return
	}
	if b.onClose != nil {
		b.onClose()
	}
	b.Close()
}

// Close encerra a janela (idempotente).
func (b *SessionBanner) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	hwnd := b.hwnd
	b.mu.Unlock()

	if hwnd != 0 {
		procKillTimer.Call(hwnd, 1)
		procDestroyWindow.Call(hwnd)
	}
	select {
	case <-b.doneCh:
	case <-time.After(2 * time.Second):
	}
}

func mustUTF16(value string) *uint16 {
	ptr, _ := windows.UTF16PtrFromString(value)
	return ptr
}
