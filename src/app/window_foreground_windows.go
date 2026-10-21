//go:build windows

package app

import (
	"os"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ── Forçar o PRIMEIRO PLANO no Win32 ────────────────────────────────────────
//
// Complemento nativo de raiseWindowToFront (window_foreground.go). O problema
// que isto resolve está documentado lá: Show()+Focus() do Wails não vencem o
// "foreground lock" do Windows quando o processo não é o dono do primeiro plano
// — exatamente o caso do agent no tray mostrando o convite de chat de suporte.
//
// Referência: "Foreground activation" (SetForegroundWindow) e a receita clássica
// de AttachThreadInput descrita em
// https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setforegroundwindow

const (
	swpNoSizeFg     = 0x0001
	swpNoMoveFg     = 0x0002
	swpNoActivateFg = 0x0010

	// FlashWindowEx: pisca a janela (e o botão na barra de tarefas) até ela
	// receber o primeiro plano — FLASHW_TIMERNOFG faz isso parar sozinho.
	flashwStop      = 0x00000000
	flashwAll       = 0x00000003
	flashwTimerNoFG = 0x0000000C
)

// flashWindowInfo espelha FLASHWINFO (winuser.h).
type flashWindowInfo struct {
	Size    uint32
	Hwnd    uintptr
	Flags   uint32
	Count   uint32
	Timeout uint32
}

var (
	user32Foreground = syscall.NewLazyDLL("user32.dll")
	kernel32Fg       = syscall.NewLazyDLL("kernel32.dll")

	procGetForegroundWindowFg = user32Foreground.NewProc("GetForegroundWindow")
	procGetWindowThreadProcID = user32Foreground.NewProc("GetWindowThreadProcessId")
	procAttachThreadInputFg   = user32Foreground.NewProc("AttachThreadInput")
	procSetForegroundWindowFg = user32Foreground.NewProc("SetForegroundWindow")
	procBringWindowToTopFg    = user32Foreground.NewProc("BringWindowToTop")
	procSetActiveWindowFg     = user32Foreground.NewProc("SetActiveWindow")
	procSetWindowPosFg        = user32Foreground.NewProc("SetWindowPos")
	procFlashWindowExFg       = user32Foreground.NewProc("FlashWindowEx")
	procGetCurrentThreadIDFg  = kernel32Fg.NewProc("GetCurrentThreadId")
	procIsHungAppWindowFg     = user32Foreground.NewProc("IsHungAppWindow")
)

// windowHandle devolve o HWND nativo da janela do Wails (0 quando indisponível).
func windowHandle(win application.Window) uintptr {
	if win == nil {
		return 0
	}
	return uintptr(win.NativeWindow())
}

// forceWindowToForeground tenta colocar a janela em primeiro plano pelo Win32.
//
// Receita (na ordem, porque cada passo cobre a recusa do anterior):
//
//  1. AttachThreadInput na thread que está em primeiro plano — sem isso, o
//     SetForegroundWindow do nosso processo é rejeitado;
//  2. BringWindowToTop + SetActiveWindow + SetForegroundWindow;
//  3. fallback "topmost/não-topmost": subir a janela no z-order obriga o
//     Windows a atendê-la mesmo quando o SetForegroundWindow foi negado.
//
// keepTopmost preserva o estado "sempre no topo" pedido pelo chamador: o
// fallback do passo 3 devolve a janela para NOTOPMOST, então ela precisa ser
// reafirmada no fim.
func forceWindowToForeground(win application.Window, keepTopmost bool) bool {
	hwnd := windowHandle(win)
	if hwnd == 0 {
		return false
	}

	if foreground, _, _ := procGetForegroundWindowFg.Call(); foreground == hwnd {
		return true
	}

	foreground, _, _ := procGetForegroundWindowFg.Call()
	currentThread, _, _ := procGetCurrentThreadIDFg.Call()

	attached := false
	foregroundThread := uintptr(0)
	if foreground != 0 {
		// PID da janela em primeiro plano (para a guarda de travamento abaixo).
		var foregroundPid uint32
		foregroundThread, _, _ = procGetWindowThreadProcID.Call(foreground, uintptr(unsafe.Pointer(&foregroundPid)))
		if foregroundThread != 0 && foregroundThread != currentThread {
			// GUARDA DE TRAVAMENTO (20/10 20:37): AttachThreadInput sincroniza a
			// fila de entrada com a thread de primeiro plano. Se essa thread estiver
			// travada (janela "Não está respondendo"), a NOSSA thread trava junto —
			// a interface inteira congela no instante em que a janela do chat tenta
			// subir. IsHungAppWindow detecta a thread travada: nesse caso pula o
			// attach e fica com o fallback topmost/flash (a janela continua
			// visível, só não rouba o foco de um app morto).
			hung, _, _ := procIsHungAppWindowFg.Call(foreground)
			if shouldAttachForegroundInput(foregroundPid, uint32(os.Getpid()), hung != 0) {
				if ok, _, _ := procAttachThreadInputFg.Call(currentThread, foregroundThread, 1); ok != 0 {
					attached = true
				}
			}
		}
	}

	procBringWindowToTopFg.Call(hwnd)
	procSetActiveWindowFg.Call(hwnd)
	procSetForegroundWindowFg.Call(hwnd)

	if attached {
		procAttachThreadInputFg.Call(currentThread, foregroundThread, 0)
	}

	if foreground, _, _ = procGetForegroundWindowFg.Call(); foreground == hwnd {
		return true
	}

	// Fallback: o z-order topmost é honrado mesmo sem permissão de foreground.
	procSetWindowPosFg.Call(hwnd, hwndTopMost, 0, 0, 0, 0,
		uintptr(swpNoMoveFg|swpNoSizeFg|swpNoActivateFg))
	procSetWindowPosFg.Call(hwnd, hwndNotTopMost, 0, 0, 0, 0,
		uintptr(swpNoMoveFg|swpNoSizeFg|swpNoActivateFg))
	if keepTopmost {
		// Reafirma o "sempre no topo" desfeito pelo fallback.
		procSetWindowPosFg.Call(hwnd, hwndTopMost, 0, 0, 0, 0,
			uintptr(swpNoMoveFg|swpNoSizeFg|swpNoActivateFg))
	}
	procSetForegroundWindowFg.Call(hwnd)

	foreground, _, _ = procGetForegroundWindowFg.Call()
	return foreground == hwnd
}

// shouldAttachForegroundInput decide se o AttachThreadInput é seguro (função
// pura, testável). Anexa quando:
//   - a janela em primeiro plano é do PRÓPRIO agent (outra thread nossa —
//     attach entre threads do mesmo processo não depende de app externo);
//   - ou a thread de primeiro plano está RESPONDENDO (hung=false).
//
// Uma thread de OUTRO processo travada (IsHungAppWindow) é exatamente o caso
// em que o AttachThreadInput pode bloquear a nossa thread para sempre.
func shouldAttachForegroundInput(foregroundPid, selfPid uint32, hung bool) bool {
	if foregroundPid == 0 {
		return false
	}
	if foregroundPid == selfPid {
		return true
	}
	return !hung
}

// flashWindowUntilFocused pisca a janela e o botão da barra de tarefas até ela
// receber o primeiro plano (FLASHW_TIMERNOFG para sozinho).
//
// Último recurso quando nem o "sempre no topo" nem o SetForegroundWindow deram
// primeiro plano: o convite continua visível acima dos outros apps e o usuário
// recebe o aviso piscando na barra.
func flashWindowUntilFocused(win application.Window) {
	hwnd := windowHandle(win)
	if hwnd == 0 {
		return
	}
	info := flashWindowInfo{
		Size:  uint32(unsafe.Sizeof(flashWindowInfo{})),
		Hwnd:  hwnd,
		Flags: flashwAll | flashwTimerNoFG,
	}
	procFlashWindowExFg.Call(uintptr(unsafe.Pointer(&info)))
}

// stopWindowFlash interrompe o piscar iniciado por flashWindowUntilFocused.
func stopWindowFlash(win application.Window) {
	hwnd := windowHandle(win)
	if hwnd == 0 {
		return
	}
	info := flashWindowInfo{
		Size:  uint32(unsafe.Sizeof(flashWindowInfo{})),
		Hwnd:  hwnd,
		Flags: flashwStop,
	}
	procFlashWindowExFg.Call(uintptr(unsafe.Pointer(&info)))
}
