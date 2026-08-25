//go:build windows

package terminal

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/windows"
	"golang.org/x/text/encoding/charmap"
)

// Shell representa um terminal interativo (cmd.exe ou powershell.exe).
type Shell struct {
	cmd    *exec.Cmd
	shell  string
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	childPid uint32 // PID do processo filho (para AttachConsole em VT/resize)
	cols     int
	rows     int

	mu       sync.Mutex
	closed   bool
	onOutput func(string) // callback para saida
}

// NewShell cria um novo shell interativo (console real oculto, via pipes).
// Este é o caminho LEGADO/fallback usado quando ConPTY se mostra instável
// (ex.: injetor/AV mata o processo ConPTY com 0xC0000142). Usa
// CREATE_NEW_CONSOLE (console real) + HideWindow, semelhante ao terminal
// legado do MeshCentral — mais resistente a injetores/AV do que ConPTY.
// shell: "powershell" ou "cmd".
func NewShell(shell string, onOutput func(string)) (*Shell, error) {
	resolvedKind, _ := ResolveShell(ShellKind(shell))
	shell = string(resolvedKind)

	var cmd *exec.Cmd
	switch resolvedKind {
	case ShellPowerShell:
		cmd = exec.Command("powershell.exe", "-NoLogo", "-NoExit")
	default:
		cmd = exec.Command("cmd.exe")
	}

	// Configura pipes
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_CONSOLE, // pseudo-terminal behavior
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdoutPipe.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdoutPipe.Close()
		stderrPipe.Close()
		return nil, fmt.Errorf("start shell: %w", err)
	}

	s := &Shell{
		cmd:      cmd,
		shell:    shell,
		stdin:    stdin,
		stdout:   stdoutPipe,
		stderr:   stderrPipe,
		onOutput: onOutput,
	}
	if cmd.Process != nil {
		s.childPid = uint32(cmd.Process.Pid)
	}

	// Leitura assincrona da saida
	go s.readLoop(stdoutPipe)
	go s.readLoop(stderrPipe)

	// Habilita VT/ANSI no console real do filho (apos o start) para que
	// cores/sequencias ANSI sejam processadas sempre que o host suportar.
	// O resize inicial é feito pelo chamador (NewLegacyShell → s.Resize).
	enableVtOnChildConsole(s.childPid)

	return s, nil
}

// ShellKind retorna o tipo de shell em uso (conformidade com IShell).
func (s *Shell) ShellKind() ShellKind {
	return ShellKind(s.shell)
}

// Alive reporta se o processo shell ainda está em execução (conformidade com
// IShell), sem consumir o Wait().
func (s *Shell) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.cmd == nil || s.cmd.Process == nil {
		return false
	}
	return processAlive(s.cmd.Process.Pid)
}

// Wait aguarda o processo do shell terminar (conformidade com IShell).
// Útil para o gerenciador de sessão detectar falha prematura (ex.:
// 0xC0000142 quando um injetor/AV mata o processo no DllMain).
func (s *Shell) Wait() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return fmt.Errorf("processo nao inicializado")
	}
	return s.cmd.Wait()
}

func (s *Shell) readLoop(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			output := normalizeToUtf8(buf[:n])
			s.mu.Lock()
			if s.onOutput != nil && !s.closed {
				s.onOutput(output)
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// normalizeToUtf8 garante que a saída lida da pipe esteja em UTF-8 antes de
// seguir para o pipeline (base64 → frontend TextDecoder('utf-8')).
//
// No backend legacy (CREATE_NEW_CONSOLE + pipes), o stdout/stderr do processo
// filho é redirecionado para um pipe. Nativos Windows (CRT) que escrevem em
// uma pipe usam a code page ANSI do sistema (CP1252 em pt-BR) — NÃO UTF-8.
// Sem intervenção, bytes como 0xED ("í" em CP1252) viram U+FFFD ('')
// quando interpretados como UTF-8, quebrando os acentos ("Estatísticas" →
// "Estat�sticas").
//
// Se o trecho já for UTF-8 válido (ex.: ConPTY normaliza para UTF-8, ou o
// shell já emite UTF-8 após SetConsoleOutputCP(65001)), usamos direto; caso
// contrário, decodificamos de Windows-1252 (ANSI) e re-encodamos em UTF-8.
func normalizeToUtf8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	// CP1252 é aproximação da ANSI (cp_ACP) do Windows para pt-BR; cobre os
	// acentos comuns (á, é, í, ó, ú, ã, õ, ç). Falhas de decodificação viram
	// "\uFFFD" localmente em vez de propagar bytes inválidos.
	decoded, _ := charmap.Windows1252.NewDecoder().String(string(b))
	return decoded
}

// WriteStdin escreve dados no stdin do shell.
//
// NOTA: no backend legacy (`CREATE_NEW_CONSOLE` + pipes), o stdin do processo
// filho é uma PIPE (cmd.StdinPipe). O processo filho (powershell/cmd) lê o
// input DESSA pipe — e a "edição de linha" (Backspace/Delete) só é feita pela
// API de console quando o input chega via console input, o que não acontece
// aqui. Escrevemos, portanto, na pipe normalmente.
//
// A correção REAL do Backspace/Delete vem do **ConPTY** (in-process ou via
// dispatcher F4), que injeta o input no console de forma nativa. Não usamos
// `WriteConsoleInput` no legacy porque o filho (stdin=pipe) não consome o
// console input — seria uma falsa correção. Ver também console_input_windows.go.
func (s *Shell) WriteStdin(data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shell fechado")
	}
	_, err := fmt.Fprint(s.stdin, data)
	return err
}

// Resize redimensiona o console real do processo filho (backend legacy) para
// as dimensoes cols×rows. Como o ConPTY não está envolvido, usamos
// AttachConsole + SetConsoleScreenBufferSize/SetConsoleWindowInfo no console
// real do filho. É o equivalente a um resize real — corrige quebra de linha e
// alinhamento com o xterm.js.
func (s *Shell) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shell fechado")
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("dimensoes invalidas: %dx%d", cols, rows)
	}
	if s.childPid == 0 {
		return nil // sem PID (ex.: shell não iniciado) — no-op seguro
	}
	err := resizeChildConsole(s.childPid, cols, rows)
	if err == nil {
		s.cols = cols
		s.rows = rows
	}
	return err
}

// Close encerra o shell.
func (s *Shell) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.stdin.Close()
	s.cmd.Process.Kill()
	s.cmd.Wait()
	return nil
}

// Ensure imports
var _ io.ReadCloser
var _ context.Context
var _ os.File
var _ exec.Cmd
var _ = windows.CREATE_NEW_CONSOLE
