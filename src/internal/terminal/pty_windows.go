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

	"golang.org/x/sys/windows"
)

// Shell representa um terminal interativo (cmd.exe ou powershell.exe).
type Shell struct {
	cmd    *exec.Cmd
	shell  string
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	mu       sync.Mutex
	closed   bool
	onOutput func(string) // callback para saida
}

// NewShell cria um novo shell interativo.
// shell: "cmd" ou "powershell".
func NewShell(shell string, onOutput func(string)) (*Shell, error) {
	var cmd *exec.Cmd
	switch shell {
	case "powershell", "ps":
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "-")
	case "cmd", "shell", "":
		cmd = exec.Command("cmd.exe")
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

	// Leitura assincrona da saida
	go s.readLoop(stdoutPipe)
	go s.readLoop(stderrPipe)

	return s, nil
}

func (s *Shell) readLoop(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			output := string(buf[:n])
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

// WriteStdin escreve dados no stdin do shell.
func (s *Shell) WriteStdin(data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("shell fechado")
	}
	_, err := fmt.Fprint(s.stdin, data)
	return err
}

// Resize notifica o terminal sobre mudanca de tamanho.
// Placeholder: ConPTY resize requer CreatePseudoConsole (Fase 5).
func (s *Shell) Resize(cols, rows int) error {
	return nil
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
