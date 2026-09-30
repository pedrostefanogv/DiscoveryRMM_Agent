//go:build windows

package terminal

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
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

	// writeQueue desacopla WriteStdin/Resize da escrita no named pipe, que pode
	// BLOQUEAR (dispatcher ocupado/travado). Escrever direto sob d.mu prendia
	// Dimensions/Alive/Close — e o encerramento da sessão inteira. Mesmo padrão
	// do ConPTYShell.stdinQueue.
	writeQueue chan string

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

	// Telemetria do dispatcher: sem isso os log.Printf do filho (falha do
	// ConPTY com 0xC0000142, ListenPipe, exit 1...) eram PERDIDOS — o
	// discovery-service.exe é console-subsystem mas roda como serviço/GUI sem
	// console, e o stderr do dispatcher ia para handle inválido. Na estação,
	// o agent-service.log não registrava NADA do terminal. Capturamos
	// stdout+stderr e teeamos com prefixo [term-dispatcher] no log do agente
	// (que, no modo serviço, chega ao agent-service.log).
	if stderr, serr := cmd.StderrPipe(); serr == nil {
		go teeDispatcherLog(stderr)
	}
	if stdout, oerr := cmd.StdoutPipe(); oerr == nil {
		go teeDispatcherLog(stdout)
	}

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
		shellKind:  shell,
		cols:       cols,
		rows:       rows,
		cmd:        cmd,
		pipeIn:     inConn,
		pipeOut:    outConn,
		onOutput:   onOutput,
		writeQueue: make(chan string, 256),
	}

	go ds.readOutputLoop()
	go ds.writeLoop()

	return ds, nil
}

// teeDispatcherLog drena um stream do dispatcher (stdout/stderr) e repassa as
// linhas para o log do agente com prefixo [term-dispatcher] — diagnóstico do
// ConPTY isolado visível no agent-service.log (via tee do stdlib log).
func teeDispatcherLog(r interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 256*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		log.Printf("[term-dispatcher] %s", line)
	}
}

// readOutputLoop lê o output do dispatcher (pipeOut) e repassa via onOutput.
func (d *dispatcherShell) readOutputLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := d.pipeOut.Read(buf)
		if n > 0 {
			output := string(buf[:n])
			// Captura sob o mutex e invoca FORA dele: onOutput encadeia até um
			// publish NATS (pode bloquear). Com o mutex travado, WriteStdin/
			// Resize/Close/Dimensions ficariam presos atrás do publish.
			d.mu.Lock()
			cb := d.onOutput
			closed := d.closed
			d.mu.Unlock()
			if cb != nil && !closed {
				cb(output)
			}
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

// shutdown encerra a fila e os pipes de forma idempotente. O canal é fechado
// SEGURANDO d.mu: os senders (WriteStdin/Resize) também seguram o mutex, então
// ninguém pode estar no meio de um send quando o canal fecha (send em canal
// fechado = panic).
func (d *dispatcherShell) shutdown() {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		if d.writeQueue != nil {
			close(d.writeQueue)
		}
		d.mu.Unlock()
		_ = d.pipeIn.Close()
		_ = d.pipeOut.Close()
	})
}

// markOutputClosed fecha os pipes de output de forma idempotente.
func (d *dispatcherShell) markOutputClosed() {
	d.shutdown()
}

// writeLoop drena a fila de escrita para o named pipe em goroutine dedicada.
func (d *dispatcherShell) writeLoop() {
	for line := range d.writeQueue {
		d.mu.Lock()
		closed := d.closed
		d.mu.Unlock()
		if closed {
			continue // shell encerrado: descarta o restante da fila
		}
		if _, err := d.pipeIn.Write([]byte(line)); err != nil {
			log.Printf("[terminal] dispatcher pipe in write falhou: %v", err)
			d.markOutputClosed()
			return
		}
	}
}

// enqueueLocked coloca uma linha na fila de escrita (chamador segura d.mu).
func (d *dispatcherShell) enqueueLocked(line string) error {
	if d.closed {
		return fmt.Errorf("shell fechado")
	}
	select {
	case d.writeQueue <- line:
		return nil
	default:
		return fmt.Errorf("fila de escrita cheia (%d/%d)", len(d.writeQueue), cap(d.writeQueue))
	}
}

// WriteStdin enfileira o input (não-bloqueante) — a escrita real acontece em
// writeLoop, fora do mutex.
func (d *dispatcherShell) WriteStdin(data string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	line := base64.StdEncoding.EncodeToString([]byte(data)) + "\n"
	return d.enqueueLocked(line)
}

func (d *dispatcherShell) Resize(cols, rows int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("dimensões inválidas")
	}
	if err := d.enqueueLocked(fmt.Sprintf("resize:%dx%d\n", cols, rows)); err != nil {
		return err
	}
	// Sem isto Dimensions() devolvia para sempre o tamanho do spawn, mesmo
	// depois de um resize aplicado com sucesso.
	d.cols, d.rows = cols, rows
	return nil
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
	d.shutdown()
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
	}
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
		// Mesmo encerramento idempotente dos pipes/fila.
		d.shutdown()
	})
	return d.waitErr
}

var _ IShell = (*dispatcherShell)(nil)
