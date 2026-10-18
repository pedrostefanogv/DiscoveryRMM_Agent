//go:build windows

package app

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/remotesession"
)

// ── Prompt nativo de consentimento (fallback do PSADT) ──
//
// Janela topmost com contagem regressiva visível, botões Permitir/Negar e
// cancelamento imediato quando o contexto é cancelado (stop do servidor/worker).
// O botão padrão segue a ação do timeout, para um Enter nunca "permitir por
// acidente" quando a política manda negar.

const (
	consentWMClose     = 0x0010
	consentWMDestroy   = 0x0002
	consentWMTimer     = 0x0113
	consentWMCommand   = 0x0111
	consentWMSetFont   = 0x0030
	consentWMAppCancel = 0x8000 + 42
	consentSWShow      = 5

	consentSMXScreen = 0
	consentSMYScreen = 1
)

const (
	consentWSChild         = 0x40000000
	consentWSVisible       = 0x10000000
	consentWSPopup         = 0x80000000
	consentWSCaption       = 0x00C00000
	consentWSSysMenu       = 0x00080000
	consentWSExTopmost     = 0x00000008
	consentWSExToolWindow  = 0x00000080
	consentBSDefPushButton = 0x00000001
	consentSSLeft          = 0x00000000
	consentDefaultGuiFont  = 17
)

var (
	consentUser32 = windows.NewLazySystemDLL("user32.dll")
	consentGdi32  = windows.NewLazySystemDLL("gdi32.dll")

	consentProcRegisterClassExW = consentUser32.NewProc("RegisterClassExW")
	consentProcCreateWindowExW  = consentUser32.NewProc("CreateWindowExW")
	consentProcDefWindowProcW   = consentUser32.NewProc("DefWindowProcW")
	consentProcDestroyWindow    = consentUser32.NewProc("DestroyWindow")
	consentProcShowWindow       = consentUser32.NewProc("ShowWindow")
	consentProcUpdateWindow     = consentUser32.NewProc("UpdateWindow")
	consentProcSetWindowTextW   = consentUser32.NewProc("SetWindowTextW")
	consentProcSetTimer         = consentUser32.NewProc("SetTimer")
	consentProcKillTimer        = consentUser32.NewProc("KillTimer")
	consentProcGetMessageW      = consentUser32.NewProc("GetMessageW")
	consentProcTranslateMessage = consentUser32.NewProc("TranslateMessage")
	consentProcDispatchMessageW = consentUser32.NewProc("DispatchMessageW")
	consentProcPostMessageW     = consentUser32.NewProc("PostMessageW")
	consentProcPostQuitMessage  = consentUser32.NewProc("PostQuitMessage")
	consentProcSetForeground    = consentUser32.NewProc("SetForegroundWindow")
	consentProcGetSystemMetrics = consentUser32.NewProc("GetSystemMetrics")
	consentProcSendMessageW     = consentUser32.NewProc("SendMessageW")
	consentProcSetFocus         = consentUser32.NewProc("SetFocus")
	consentProcGetStockObject   = consentGdi32.NewProc("GetStockObject")
)

type consentWndClassExW struct {
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

type consentMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

type consentNativeState struct {
	mu        sync.Mutex
	req       remotesession.ConsentRequest
	remaining int
	decided   bool
	decision  remotesession.ConsentDecision
	hwnd      uintptr
	label     uintptr
	resultCh  chan remotesession.ConsentDecision
}

var (
	consentClassOnce sync.Once
	consentClassErr  error
	consentStates    sync.Map // hwnd -> *consentNativeState
)

const consentClassName = "DiscoveryRmmConsentPrompt"

func ensureConsentClass() error {
	consentClassOnce.Do(func() {
		className, err := windows.UTF16PtrFromString(consentClassName)
		if err != nil {
			consentClassErr = err
			return
		}
		wc := consentWndClassExW{
			cbSize:        uint32(unsafe.Sizeof(consentWndClassExW{})),
			lpfnWndProc:   windows.NewCallback(consentNativeWndProc),
			lpszClassName: className,
			hbrBackground: 0,
		}
		ret, _, callErr := consentProcRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if ret == 0 {
			consentClassErr = fmt.Errorf("RegisterClassExW(consent) falhou: %v", callErr)
		}
	})
	return consentClassErr
}

func consentUTF16(value string) *uint16 {
	ptr, _ := windows.UTF16PtrFromString(value)
	return ptr
}

func consentLabelText(req remotesession.ConsentRequest, remaining int) string {
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = "O suporte solicita acesso remoto ao seu computador. Você autoriza?"
	}
	action := "recusada"
	if strings.EqualFold(strings.TrimSpace(req.TimeoutAction), "approve") {
		action = "autorizada"
	}
	return fmt.Sprintf("%s\n\nA conexão será %s automaticamente em %d segundo(s).", message, action, maxInt(remaining, 0))
}

func consentTimeoutDecision(req remotesession.ConsentRequest) remotesession.ConsentDecision {
	accepted := strings.EqualFold(strings.TrimSpace(req.TimeoutAction), "approve")
	outcome := "timeout-denied"
	if accepted {
		outcome = "timeout-granted"
	}
	return remotesession.ConsentDecision{
		Accepted:      accepted,
		Auto:          true,
		Outcome:       outcome,
		Reason:        "timeout",
		PromptBackend: "native",
		DecidedAt:     time.Now().UTC(),
	}
}

func consentStoppedDecision() remotesession.ConsentDecision {
	return remotesession.ConsentDecision{
		Accepted:      false,
		Auto:          true,
		Outcome:       "stopped-by-server",
		Reason:        "stopped_by_server",
		PromptBackend: "native",
		DecidedAt:     time.Now().UTC(),
	}
}

func consentDeniedDecision(reason string) remotesession.ConsentDecision {
	return remotesession.ConsentDecision{
		Accepted:      false,
		Outcome:       "denied",
		Reason:        reason,
		PromptBackend: "native",
		DecidedAt:     time.Now().UTC(),
	}
}

func (state *consentNativeState) finish(decision remotesession.ConsentDecision) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.decided {
		return
	}
	state.decided = true
	state.decision = decision
	select {
	case state.resultCh <- decision:
	default:
	}
}

func (state *consentNativeState) currentRemaining() int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.remaining
}

func (state *consentNativeState) tick() (finished bool) {
	state.mu.Lock()
	if state.decided {
		state.mu.Unlock()
		return true
	}
	state.remaining--
	remaining := state.remaining
	state.mu.Unlock()

	if remaining <= 0 {
		state.finish(consentTimeoutDecision(state.req))
		return true
	}

	ptr := consentUTF16(consentLabelText(state.req, remaining))
	consentProcSetWindowTextW.Call(state.label, uintptr(unsafe.Pointer(ptr)))
	return false
}

func consentNativeWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	raw, ok := consentStates.Load(hwnd)
	if !ok {
		ret, _, _ := consentProcDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		return ret
	}
	state, _ := raw.(*consentNativeState)

	switch msg {
	case consentWMTimer:
		if state.tick() {
			consentProcKillTimer.Call(hwnd, 1)
			consentProcDestroyWindow.Call(hwnd)
		}
		return 0
	case consentWMCommand:
		commandID := wparam & 0xffff
		switch commandID {
		case 1: // Permitir
			state.finish(remotesession.ConsentDecision{
				Accepted:      true,
				Outcome:       "granted",
				Reason:        "user_allowed",
				PromptBackend: "native",
				DecidedAt:     time.Now().UTC(),
			})
		case 2: // Negar
			state.finish(consentDeniedDecision("user_denied"))
		}
		consentProcKillTimer.Call(hwnd, 1)
		consentProcDestroyWindow.Call(hwnd)
		return 0
	case consentWMClose:
		// Fechar pelo X equivale a negar/recusar.
		state.finish(consentDeniedDecision("user_closed"))
		consentProcKillTimer.Call(hwnd, 1)
		consentProcDestroyWindow.Call(hwnd)
		return 0
	case consentWMAppCancel:
		state.finish(consentStoppedDecision())
		consentProcKillTimer.Call(hwnd, 1)
		consentProcDestroyWindow.Call(hwnd)
		return 0
	case consentWMDestroy:
		consentStates.Delete(hwnd)
		consentProcPostQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := consentProcDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return ret
}

// nativeConsentPrompt abre a janela nativa e devolve a decisão. O contexto
// cancelado fecha a janela imediatamente e produz stopped-by-server.
func nativeConsentPrompt(ctx context.Context, req remotesession.ConsentRequest) (remotesession.ConsentDecision, bool) {
	if err := ensureConsentClass(); err != nil {
		return remotesession.ConsentDecision{}, false
	}
	if ctx.Err() != nil {
		return consentStoppedDecision(), true
	}

	type nativeResult struct {
		decision remotesession.ConsentDecision
		ok       bool
	}
	resultCh := make(chan nativeResult, 1)
	go func() {
		decision, ok := runNativeConsentWindow(ctx, req)
		resultCh <- nativeResult{decision: decision, ok: ok}
	}()
	result := <-resultCh
	return result.decision, result.ok
}

func runNativeConsentWindow(ctx context.Context, req remotesession.ConsentRequest) (remotesession.ConsentDecision, bool) {
	seconds := int(req.Timeout.Seconds())
	if seconds < 10 {
		seconds = 10
	}
	state := &consentNativeState{
		req:       req,
		remaining: seconds,
		resultCh:  make(chan remotesession.ConsentDecision, 1),
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className := consentUTF16(consentClassName)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Autorização de acesso remoto"
	}
	windowTitle := consentUTF16(title)
	screenW, _, _ := consentProcGetSystemMetrics.Call(consentSMXScreen)
	screenH, _, _ := consentProcGetSystemMetrics.Call(consentSMYScreen)
	const width, height = 620, 230
	x := (screenW - width) / 2
	y := (screenH - height) / 2

	hwnd, _, _ := consentProcCreateWindowExW.Call(
		consentWSExTopmost|consentWSExToolWindow,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowTitle)),
		consentWSPopup|consentWSCaption|consentWSSysMenu,
		x, y, width, height,
		0, 0, 0, 0,
	)
	if hwnd == 0 {
		return remotesession.ConsentDecision{}, false
	}
	state.hwnd = hwnd
	consentStates.Store(hwnd, state)

	label := consentUTF16(consentLabelText(req, seconds))
	state.label, _, _ = consentProcCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(consentUTF16("STATIC"))),
		uintptr(unsafe.Pointer(label)),
		consentWSChild|consentWSVisible|consentSSLeft,
		20, 20, width-40, 110,
		hwnd, 0, 0, 0,
	)

	allowStyle := uintptr(consentWSChild | consentWSVisible)
	denyStyle := uintptr(consentWSChild | consentWSVisible)
	if strings.EqualFold(strings.TrimSpace(req.TimeoutAction), "approve") {
		allowStyle |= consentBSDefPushButton
	} else {
		denyStyle |= consentBSDefPushButton
	}

	allowBtn, _, _ := consentProcCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(consentUTF16("BUTTON"))),
		uintptr(unsafe.Pointer(consentUTF16("Permitir"))),
		allowStyle,
		width-250, height-58, 110, 32,
		hwnd, 1, 0, 0,
	)
	denyBtn, _, _ := consentProcCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(consentUTF16("BUTTON"))),
		uintptr(unsafe.Pointer(consentUTF16("Negar"))),
		denyStyle,
		width-130, height-58, 110, 32,
		hwnd, 2, 0, 0,
	)

	font, _, _ := consentProcGetStockObject.Call(consentDefaultGuiFont)
	consentProcSendMessageW.Call(state.label, consentWMSetFont, font, 1)
	consentProcSendMessageW.Call(allowBtn, consentWMSetFont, font, 1)
	consentProcSendMessageW.Call(denyBtn, consentWMSetFont, font, 1)

	consentProcShowWindow.Call(hwnd, consentSWShow)
	consentProcUpdateWindow.Call(hwnd)
	consentProcSetForeground.Call(hwnd)
	if strings.EqualFold(strings.TrimSpace(req.TimeoutAction), "approve") {
		consentProcSetFocus.Call(allowBtn)
	} else {
		consentProcSetFocus.Call(denyBtn)
	}
	consentProcSetTimer.Call(hwnd, 1, 1000, 0)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			consentProcPostMessageW.Call(hwnd, consentWMAppCancel, 0, 0)
		case <-done:
		}
	}()

	var msg consentMsg
	for {
		ret, _, _ := consentProcGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		consentProcTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		consentProcDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	select {
	case decision := <-state.resultCh:
		return decision, true
	default:
		state.mu.Lock()
		decided := state.decided
		decision := state.decision
		state.mu.Unlock()
		if decided {
			return decision, true
		}
		return consentStoppedDecision(), true
	}
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
