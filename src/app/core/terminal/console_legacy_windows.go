//go:build windows

package terminal

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// smokeAnchor mantém o pacote syscall importado mesmo que Helpers sejam
// reordenados pelo compilador (todos os símbolos syscall aqui são usados).
var _ = syscall.Handle(0)

// ── Console real (legacy) — helpers de VT/ANSI e resize ──
//
// O backend legacy cria o shell com CREATE_NEW_CONSOLE (um console real de
// verdade no lado do processo filho), mas redireciona stdout/stderr para
// pipes. Sem intervenção, o console do filho não processa sequências ANSI/VT
// e fica no tamanho default (80×const).
//
// Este arquivo implementa dois helpers que atuam SOBRE O CONSOLE DO FILHO,
// anexando-se a ele por um breve intervalo via AttachConsole:
//   - enableVtOnChildConsole: liga ENABLE_VIRTUAL_TERMINAL_PROCESSING para que
//     aplicações CRT/console possam emitir cores/ANSI (quando aplicável).
//   - resizeChildConsole: redimensiona o buffer para cols×rows, alinhando o
//     console real ao xterm.js do viewer.
//
// O agente (Wails, GUI subsystem) não possui console próprio, então
// AttachConsole(childPid) → operar → FreeConsole é seguro e não interfere com
// a leitura via pipe (as operações são breves e não bloqueiam o readLoop).

const (
	// ENABLE_VIRTUAL_TERMINAL_PROCESSING — o console anexado interpreta as
	// sequências de escape ANSI/VT.
	enableVirtualTerminalProcessing = 0x0004
	// ENABLE_PROCESSED_OUTPUT — caracteres de controle são processados.
	enableProcessedOutput = 0x0001
)

var (
	procGetConsoleMode                 = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode                 = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo     = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procSetConsoleScreenBufferSize     = kernel32.NewProc("SetConsoleScreenBufferSize")
	procSetConsoleWindowInfo           = kernel32.NewProc("SetConsoleWindowInfo")
	procAttachConsole                  = kernel32.NewProc("AttachConsole")
	procFreeConsole                    = kernel32.NewProc("FreeConsole")
	procGetStdHandle                = kernel32.NewProc("GetStdHandle")
	procGetLargestConsoleWindowSize = kernel32.NewProc("GetLargestConsoleWindowSize")
)

const invalidHandleValue = ^uintptr(0) // (HANDLE)-1

func stdOutputHandle() (syscall.Handle, error) {
	r, _, _ := procGetStdHandle.Call(uintptr(windows.STD_OUTPUT_HANDLE))
	if r == 0 || r == invalidHandleValue {
		return 0, fmt.Errorf("GetStdHandle(STD_OUTPUT_HANDLE) invalido")
	}
	return syscall.Handle(r), nil
}

// attachChildConsole anexa o processo atual ao console do processo filho e
// retorna uma função de cleanup (FreeConsole). O processo atual (agente Wails)
// não tem console próprio, então AttachConsole é seguro.
func attachChildConsole(childPid uint32) (func(), error) {
	// Garante que este processo esteja desassociado de qualquer console.
	r, _, _ := procFreeConsole.Call()
	_ = r

	r1, _, err := procAttachConsole.Call(uintptr(childPid))
	if r1 == 0 {
		return nil, fmt.Errorf("AttachConsole(%d): %w", childPid, err)
	}
	return func() {
		r, _, _ := procFreeConsole.Call()
		_ = r
	}, nil
}

// getConsoleMode retorna o ConsoleMode do handle de saída do console anexado.
func getConsoleMode(h syscall.Handle) (uint32, error) {
	var mode uint32
	r, _, err := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return 0, fmt.Errorf("GetConsoleMode: %w", err)
	}
	return mode, nil
}

// enableVtOnChildConsole liga ENABLE_VIRTUAL_TERMINAL_PROCESSING no console do
// processo filho. Retorna erro silenciosamente tolerado (se o host não
// suporta VT, mantém o modo atual — o terminal legacy segue funcional, apenas
// sem cores/ANSI).
func enableVtOnChildConsole(childPid uint32) {
	free, err := attachChildConsole(childPid)
	if err != nil {
		return
	}
	defer free()

	hOut, err := stdOutputHandle()
	if err != nil {
		return
	}

	mode, err := getConsoleMode(hOut)
	if err != nil {
		return
	}
	newMode := mode | enableVirtualTerminalProcessing | enableProcessedOutput
	_, _, _ = procSetConsoleMode.Call(uintptr(hOut), uintptr(newMode))
}

// ── Estrutura COORD/SMALL_RECT (já usada para outros fins) ──

type coord struct {
	X int16
	Y int16
}

type smallRect struct {
	Left, Top, Right, Bottom int16
}

type consoleScreenBufferInfo struct {
	dwSize              coord
	dwCursorPosition    coord
	wAttributes         uint16
	srWindow            smallRect
	dwMaximumWindowSize coord
}

// resizeChildConsole redimensiona o console real do processo filho para as
// dimensões cols×rows informadas (buffer máximo + janela visível). Isso
// alinha o console legado ao tamanho do xterm.js no viewer.
func resizeChildConsole(childPid uint32, cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("dimensoes invalidas: %dx%d", cols, rows)
	}

	free, err := attachChildConsole(childPid)
	if err != nil {
		return err
	}
	defer free()

	hOut, err := stdOutputHandle()
	if err != nil {
		return err
	}

	var info consoleScreenBufferInfo
	r, _, err := procGetConsoleScreenBufferInfo.Call(uintptr(hOut), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return fmt.Errorf("GetConsoleScreenBufferInfo: %w", err)
	}

	// Limita cols/rows ao window size máximo suportado pelo host.
	largestW, _, _ := procGetLargestConsoleWindowSize.Call(uintptr(hOut))
	maxCols := int16(largestW & 0xFFFF)
	maxRows := int16((largestW >> 16) & 0xFFFF)
	if maxCols > 0 && int16(cols) > maxCols {
		cols = int(maxCols)
	}
	if maxRows > 0 && int16(rows) > maxRows {
		rows = int(maxRows)
	}

	// 1) Ajusta o tamanho da janela para o desejado (necessário antes de
	//    redimensionar o buffer além ou abaixo do tamanho atual).
	destWindow := smallRect{
		Left:   0,
		Top:    0,
		Right:  int16(cols) - 1,
		Bottom: int16(rows) - 1,
	}
	// Usa SetConsoleWindowInfo(bAbsolute=true).
	_, _, _ = procSetConsoleWindowInfo.Call(
		uintptr(hOut),
		1, // bAbsolute
		uintptr(unsafe.Pointer(&destWindow)),
	)

	// 2) Redimensiona o buffer para cols×rows.
	newSize := coord{X: int16(cols), Y: int16(rows)}
	r, _, err = procSetConsoleScreenBufferSize.Call(uintptr(hOut), uintptr(unsafe.Pointer(&newSize)))
	if r == 0 {
		// Se o buffer não pôde ser reduzido (ex.: janela maior), ao menos
		// tentamos manter o buffer existente e re-fitar a janela.
		return fmt.Errorf("SetConsoleScreenBufferSize: %w", err)
	}

	// 3) Re-aplica a janela após o resize do buffer.
	_, _, _ = procSetConsoleWindowInfo.Call(
		uintptr(hOut),
		1,
		uintptr(unsafe.Pointer(&destWindow)),
	)

	return nil
}