//go:build windows

package screen

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Bloqueio de entrada do host remoto (KVM input lock) ──
//
// MECANISMO: hooks de baixo nível (WH_KEYBOARD_LL / WH_MOUSE_LL) que ENGOLem
// apenas eventos FÍSICOS — os injetados por SendInput têm a flag
// LLKHF_INJECTED/LLMHF_INJECTED e passam direto.
//
// POR QUE NÃO BlockInput: validado em campo — BlockInput descarta TAMBÉM os
// eventos do SendInput, então o operador perdia o teclado/mouse junto com o
// usuário local. O hook com filtro por flag mantém o operador no controle,
// que é o comportamento esperado (padrão KVM lock).
//
// FAIL-SAFE: os hooks pertencem à thread que os instalou; se o processo morrer,
// o Windows remove os hooks automaticamente. O callback é trivial (só lê a flag
// do evento e devolve 1 ou chama CallNextHookEx) para respeitar o
// LowLevelHooksTimeout.
const (
	whKeyboardLL = 13
	whMouseLL    = 14

	llkhfInjected = 0x00000010 // KBDLLHOOKSTRUCT.flags
	llmhfInjected = 0x00000001 // MSLLHOOKSTRUCT.flags

	wmQuit     = 0x0012
	pmNoRemove = 0x0000
)

// Estruturas Win32 (x64) usadas pelos hooks e pelo message loop.
type hookPoint struct {
	X int32
	Y int32
}

type hookMSG struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       hookPoint
	lPrivate uint32
}

type kbDllHookStruct struct {
	vkCode      uint32
	scanCode    uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

type msDllHookStruct struct {
	pt          hookPoint
	mouseData   uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

var (
	procSetWindowsHookExW   = user32DLL.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32DLL.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32DLL.NewProc("CallNextHookEx")
	procGetMessageW         = user32DLL.NewProc("GetMessageW")
	procTranslateMessage    = user32DLL.NewProc("TranslateMessage")
	procDispatchMessageW    = user32DLL.NewProc("DispatchMessageW")
	procPostThreadMessageW  = user32DLL.NewProc("PostThreadMessageW")
	procPeekMessageW        = user32DLL.NewProc("PeekMessageW")
	kernel32DLL             = windows.NewLazySystemDLL("kernel32.dll")
	procGetModuleHandleW    = kernel32DLL.NewProc("GetModuleHandleW")
)

// inputHookSwallow: true enquanto o bloqueio está ativo. Atomic porque o
// callback roda na thread do hook, sem relação com o mutex do estado.
var inputHookSwallow atomic.Bool

// Ponteiros de callback registrados UMA única vez: windows.NewCallback aloca um
// trampolim que NUNCA é liberado (limite de ~1024 por processo). Registrar a
// cada lock vazaria um slot por bloqueio.
var (
	hookProcsOnce sync.Once
	hookKbProc    uintptr
	hookMsProc    uintptr
)

func hookProcPointers() (kb, ms uintptr) {
	hookProcsOnce.Do(func() {
		hookKbProc = windows.NewCallback(keyboardHookProc)
		hookMsProc = windows.NewCallback(mouseHookProc)
	})
	return hookKbProc, hookMsProc
}

var inputHook struct {
	// opMu serializa a OPERAÇÃO inteira de instalar/remover (não só o estado):
	// sem ele, um Unblock chegando enquanto o Block ainda instalava os hooks
	// deixaria os hooks instalados e a thread do message loop viva para sempre.
	opMu sync.Mutex

	mu   sync.Mutex
	busy bool
	tid  uint32
	done chan struct{}
}

// shouldSwallowKeyboard informa se um evento de teclado deve ser engolido:
// bloqueio ativo E evento não injetado. Puro (testável sem instalar hook).
func shouldSwallowKeyboard(active bool, flags uint32) bool {
	return active && flags&llkhfInjected == 0
}

// shouldSwallowMouse é o equivalente para mouse.
func shouldSwallowMouse(active bool, flags uint32) bool {
	return active && flags&llmhfInjected == 0
}

// NOTA: o lParam é declarado como unsafe.Pointer de propósito — o runtime
// aceita tipos pointer-sized em callbacks (assignArg), e assim NÃO existe
// conversão uintptr→unsafe.Pointer no nosso código: o vet não reclama
// (unsafeptr) e o checkptr de builds -race não é acionado.
func callNextHook(nCode, wParam uintptr, lParam unsafe.Pointer) uintptr {
	ret, _, _ := procCallNextHookEx.Call(0, nCode, wParam, uintptr(lParam))
	return ret
}

func keyboardHookProc(nCode, wParam uintptr, lParam unsafe.Pointer) uintptr {
	if int32(nCode) < 0 || lParam == nil {
		return callNextHook(nCode, wParam, lParam)
	}
	kb := (*kbDllHookStruct)(lParam)
	if shouldSwallowKeyboard(inputHookSwallow.Load(), kb.flags) {
		return 1 // engole o evento físico
	}
	return callNextHook(nCode, wParam, lParam)
}

func mouseHookProc(nCode, wParam uintptr, lParam unsafe.Pointer) uintptr {
	if int32(nCode) < 0 || lParam == nil {
		return callNextHook(nCode, wParam, lParam)
	}
	ms := (*msDllHookStruct)(lParam)
	if shouldSwallowMouse(inputHookSwallow.Load(), ms.flags) {
		return 1 // engole o evento físico
	}
	return callNextHook(nCode, wParam, lParam)
}

// BlockInputSystem instala os hooks de teclado e mouse e devolve o método.
//
// Idempotente: um segundo bloqueio sem unlock apenas confirma o estado.
func BlockInputSystem() (string, error) {
	inputHook.opMu.Lock()
	defer inputHook.opMu.Unlock()

	if err := procSetWindowsHookExW.Find(); err != nil {
		return "", fmt.Errorf("SetWindowsHookExW indisponível: %v", err)
	}
	if err := procGetMessageW.Find(); err != nil {
		return "", fmt.Errorf("GetMessageW indisponível: %v", err)
	}

	inputHook.mu.Lock()
	if inputHook.busy {
		inputHook.mu.Unlock()
		return "hook", nil
	}
	inputHook.busy = true
	inputHook.mu.Unlock()

	// Engole desde já: antes de os hooks existirem a flag não tem efeito.
	inputHookSwallow.Store(true)

	ready := make(chan error, 1)
	done := make(chan struct{})

	go func() {
		// A thread precisa ser PINADA: o callback do hook é entregue na fila de
		// mensagens dela e os hooks morrem junto com ela (fail-safe do processo).
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		// Garante a existência da fila de mensagens antes do SetWindowsHookEx.
		var msg hookMSG
		_, _, _ = procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, pmNoRemove)

		tid := windows.GetCurrentThreadId()
		hMod, _, _ := procGetModuleHandleW.Call(0)
		kbProc, msProc := hookProcPointers()

		kb, _, kbErr := procSetWindowsHookExW.Call(whKeyboardLL, kbProc, hMod, 0)
		if kb == 0 {
			ready <- fmt.Errorf("hook de teclado falhou: %v (errno=%d)", describeErr(kbErr), errnoOf(kbErr))
			close(done)
			return
		}
		ms, _, msErr := procSetWindowsHookExW.Call(whMouseLL, msProc, hMod, 0)
		if ms == 0 {
			_, _, _ = procUnhookWindowsHookEx.Call(kb)
			ready <- fmt.Errorf("hook de mouse falhou: %v (errno=%d)", describeErr(msErr), errnoOf(msErr))
			close(done)
			return
		}

		inputHook.mu.Lock()
		inputHook.tid = tid
		inputHook.mu.Unlock()
		ready <- nil

		// Message loop: obrigatório para o hook de baixo nível ser chamado.
		for {
			ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if ret == 0 || ret == ^uintptr(0) {
				break // WM_QUIT (UnblockInputSystem) ou erro
			}
			_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}

		_, _, _ = procUnhookWindowsHookEx.Call(kb)
		_, _, _ = procUnhookWindowsHookEx.Call(ms)
		close(done)
	}()

	if err := <-ready; err != nil {
		inputHookSwallow.Store(false)
		inputHook.mu.Lock()
		inputHook.busy = false
		inputHook.tid = 0
		inputHook.mu.Unlock()
		<-done
		return "", err
	}

	inputHook.mu.Lock()
	inputHook.done = done
	inputHook.mu.Unlock()
	return "hook", nil
}

// UnblockInputSystem remove os hooks e encerra a thread do message loop.
// Idempotente e seguro em qualquer caminho de encerramento.
func UnblockInputSystem() error {
	inputHook.opMu.Lock()
	defer inputHook.opMu.Unlock()

	inputHook.mu.Lock()
	if !inputHook.busy {
		inputHook.mu.Unlock()
		return nil
	}
	tid, done := inputHook.tid, inputHook.done
	inputHook.busy = false
	inputHook.tid = 0
	inputHook.done = nil
	inputHook.mu.Unlock()

	// Primeiro desliga o filtro (para qualquer evento que ainda passe pelo
	// callback durante o desmonte), depois encerra a thread (que desinstala).
	inputHookSwallow.Store(false)
	if tid != 0 {
		_, _, _ = procPostThreadMessageW.Call(uintptr(tid), wmQuit, 0, 0)
	}
	if done != nil {
		<-done
	}
	return nil
}
