//go:build windows

package terminal

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/Microsoft/go-winio"
)

// ── Lado cliente do dispatcher no agente ──
//
// Quando DISCOVERY_TERM_DISPATCHER=1, o agente roda o ConPTY num processo filho
// (dispatcher) em vez de no processo GUI. Este arquivo implementa o lado
// cliente: spawna o próprio binário com "--terminal-dispatcher", espera os
// pipes nomeados do dispatcher e expõe uma IShell que traduz para os pipes.
//
// IMPORTANTE (ciclo de vida): o Wait() é reservado ao monitor de exit da
// sessão (session_terminal.go) e guardado por `waitOnce`. O readLoop/Close
// usam um `closeOnce` separado — NUNCA consomem o waitOnce, senão o monitor
// não detectaria a morte do shell (sessão órfã). Este é o mesmo bug que já
// foi corrigido no ConPTY/legacy nos históricos anteriores.

type dispatcherShell struct {
	shellKind ShellKind
	cols      int
	rows      int

	cmd     *exec.Cmd
	pipeIn  net.Conn // agente → dispatcher (input/resize)
	pipeOut net.Conn // dispatcher → agente (output)

	onOutput func(string)

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	waitOnce  sync.Once
	waitErr   error
}

// NewDispatcherShell cria um shell via ConPTY num processo filho (dispatcher).
// Só é chamado por NewShellInteractive quando DispatchersAvailable() é true.
func NewDispatcherShell(shell ShellKind, cols, rows int, onOutput func(string)) (IShell, error) {
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 40
	}

	session := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	inPipe := fmt.Sprintf(`\\.\pipe\discovery-term-%s-in`, session)
	outPipe := fmt.Sprintf(`\\.\pipe\discovery-term-%s-out`, session)

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("os.Executable: %w", err)
	}

	cmd := exec.Command(exe,
		"--terminal-dispatcher",
		"--terminal-session="+session,
		"--terminal-shell="+string(shell),
		"--terminal-cols="+fmt.Sprint(cols),
		"--terminal-rows="+fmt.Sprint(rows),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start dispatcher: %w", err)
	}

	// Aguarda os pipes nomeados aparecerem (o dispatcher os cria como server).
	var inConn, outConn net.Conn
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if inConn == nil {
			if c, derr := winio.DialPipe(inPipe, nil); derr == nil {
				inConn = c
			}
		}
		if outConn == nil {
			if c, derr := winio.DialPipe(outPipe, nil); derr == nil {
				outConn = c
			}
		}
		if inConn != nil && outConn != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if inConn == nil || outConn == nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("dispatcher não conectou aos pipes em 5s")
	}

	ds := &dispatcherShell{
		shellKind: shell,
		cols:      cols,
		rows:      rows,
		cmd:       cmd,
		pipeIn:    inConn,
		pipeOut:   outConn,
		onOutput:  onOutput,
	}

	go ds.readOutputLoop()

	return ds, nil
}

// readOutputLoop lê o output do dispatcher (pipeOut) e repassa via onOutput.
func (d *dispatcherShell) readOutputLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := d.pipeOut.Read(buf)
		if n > 0 {
			output := string(buf[:n])
			d.mu.Lock()
			if d.onOutput != nil && !d.closed {
				d.onOutput(output)
			}
			d.mu.Unlock()
		}
		if err != nil {
			// Dispatcher/shell encerrou (pipe fechado). Fecha os pipes e marca
			// como closed, mas NÃO consome o waitOnce — o Wait() (monitor de
			// exit do SessionTerminal) é quem sinaliza a morte do processo.
			d.markOutputClosed()
			return
		}
	}
}

// markOutputClosed fecha os pipes de output de forma idempotente.
func (d *dispatcherShell) markOutputClosed() {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		_ = d.pipeOut.Close()
		_ = d.pipeIn.Close()
	})
}

func (d *dispatcherShell) WriteStdin(data string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return fmt.Errorf("shell fechado")
	}
	line := base64.StdEncoding.EncodeToString([]byte(data)) + "\n"
	_, err := d.pipeIn.Write([]byte(line))
	return err
}

func (d *dispatcherShell) Resize(cols, rows int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return fmt.Errorf("shell fechado")
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("dimensões inválidas")
	}
	_, err := d.pipeIn.Write([]byte(fmt.Sprintf("resize:%dx%d\n", cols, rows)))
	return err
}

func (d *dispatcherShell) Dimensions() (cols, rows int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cols, d.rows
}

func (d *dispatcherShell) ShellKind() ShellKind {
	return d.shellKind
}

func (d *dispatcherShell) Alive() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return false
	}
	if d.cmd == nil || d.cmd.Process == nil {
		return false
	}
	return processAlive(d.cmd.Process.Pid)
}

// Close encerra o shell. Usa closeOnce (idempotente) e NÃO toca no waitOnce.
func (d *dispatcherShell) Close() error {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		_ = d.pipeIn.Close()
		_ = d.pipeOut.Close()
		if d.cmd != nil && d.cmd.Process != nil {
			_ = d.cmd.Process.Kill()
		}
	})
	return nil
}

// Wait aguarda o processo dispatcher terminar e retorna o erro de saída.
// Guardado por waitOnce (exclusivo do monitor de exit da sessão).
func (d *dispatcherShell) Wait() error {
	d.waitOnce.Do(func() {
		if d.cmd != nil && d.cmd.Process != nil {
			_, err := d.cmd.Process.Wait()
			if err != nil {
				d.waitErr = err
			}
		}
		d.mu.Lock()
		if !d.closed {
			d.closed = true
		}
		d.mu.Unlock()
		_ = d.pipeIn.Close()
		_ = d.pipeOut.Close()
	})
	return d.waitErr
}

var _ IShell = (*dispatcherShell)(nil)