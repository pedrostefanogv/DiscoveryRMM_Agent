//go:build !windows

package terminal

import (
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// Shell representa um terminal interativo em Linux/macOS.
type Shell struct {
	cmd    *exec.Cmd
	shell  string
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	mu       sync.Mutex
	closed   bool
	onOutput func(string)
}

func NewShell(shell string, onOutput func(string)) (*Shell, error) {
	cmd := exec.Command("/bin/sh")
	if shell == "bash" {
		cmd = exec.Command("/bin/bash")
	}

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	s := &Shell{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderr,
		onOutput: onOutput,
	}

	// Leitura assincrona
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				s.onOutput(string(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				s.onOutput(string(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	}()

	return s, nil
}

func (s *Shell) WriteStdin(data string) error {
	_, err := fmt.Fprint(s.stdin, data)
	return err
}

func (s *Shell) Resize(cols, rows int) error { return nil }

func (s *Shell) Close() error {
	s.stdin.Close()
	s.cmd.Process.Kill()
	s.cmd.Wait()
	return nil
}

// ShellKind retorna o tipo de shell em uso (conformidade com IShell).
func (s *Shell) ShellKind() ShellKind {
	return ShellKind(s.shell)
}

// Alive reporta se o processo shell ainda está em execução (conformidade com
// IShell), sem consumir o Wait().
func (s *Shell) Alive() bool {
	if s.cmd == nil || s.cmd.Process == nil {
		return false
	}
	return processAlive(s.cmd.Process.Pid)
}

// Wait aguarda o processo do shell terminar (conformidade com IShell).
func (s *Shell) Wait() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return fmt.Errorf("processo nao inicializado")
	}
	return s.cmd.Wait()
}

var _ io.ReadCloser
