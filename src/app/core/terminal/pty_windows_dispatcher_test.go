//go:build windows

package terminal

import (
	"net"
	"testing"
	"time"
)

// TestDispatcherWriteNaoBloqueia garante que WriteStdin/Resize NÃO escrevem no
// named pipe dentro do mutex: com um pipe que ninguém lê, a escrita bloquearia
// e prenderia Dimensions/Alive/Close (encerramento da sessão travado).
func TestDispatcherWriteNaoBloqueia(t *testing.T) {
	agente, dispatcher := net.Pipe() // net.Pipe é SÍNCRONO: Write bloqueia até Read
	defer agente.Close()
	defer dispatcher.Close()

	d := &dispatcherShell{
		shellKind:  ShellCmd,
		cols:       80,
		rows:       25,
		pipeIn:     agente,
		pipeOut:    agente,
		writeQueue: make(chan string, 8),
	}
	go d.writeLoop()
	defer d.shutdown()

	// 1) WriteStdin não pode bloquear (ninguém lê o pipe ainda).
	writeDone := make(chan error, 1)
	go func() { writeDone <- d.WriteStdin("eco") }()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("WriteStdin devolveu erro: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WriteStdin bloqueou: a escrita continua dentro do mutex")
	}

	// 2) O mutex está livre: Dimensions responde com a escrita ainda pendente.
	dimDone := make(chan struct{})
	go func() { _, _ = d.Dimensions(); close(dimDone) }()
	select {
	case <-dimDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Dimensions bloqueou atrás da escrita no pipe")
	}

	// 3) Quando o dispatcher lê, a linha chega (base64 + \n).
	if err := dispatcher.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 32)
	n, err := dispatcher.Read(buf)
	if err != nil {
		t.Fatalf("leitura do pipe: %v", err)
	}
	if got := string(buf[:n]); got != "ZWNv\n" {
		t.Fatalf("payload no pipe = %q, want %q", got, "ZWNv\n")
	}
}

// TestDispatcherResizeAtualizaDimensoes garante que Dimensions reflete o resize
// (antes devolvia sempre o tamanho do spawn).
func TestDispatcherResizeAtualizaDimensoes(t *testing.T) {
	agente, dispatcher := net.Pipe()
	defer agente.Close()
	defer dispatcher.Close()

	d := &dispatcherShell{
		shellKind:  ShellCmd,
		cols:       80,
		rows:       25,
		pipeIn:     agente,
		pipeOut:    agente,
		writeQueue: make(chan string, 8),
	}
	go d.writeLoop()
	defer d.shutdown()
	go func() { // drena o pipe para o resize não depender de leitura
		buf := make([]byte, 64)
		for {
			if _, err := dispatcher.Read(buf); err != nil {
				return
			}
		}
	}()

	if err := d.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if cols, rows := d.Dimensions(); cols != 120 || rows != 40 {
		t.Fatalf("Dimensions = %dx%d, want 120x40", cols, rows)
	}
}

// TestDispatcherFilaCheiaRetornaErro garante que o input não é descartado em
// silêncio quando a fila enche (o SessionTerminal reporta stdin_rejected).
func TestDispatcherFilaCheiaRetornaErro(t *testing.T) {
	agente, dispatcher := net.Pipe()
	defer agente.Close()
	defer dispatcher.Close()

	d := &dispatcherShell{
		shellKind:  ShellCmd,
		pipeIn:     agente,
		pipeOut:    agente,
		writeQueue: make(chan string, 1), // sem writeLoop: ninguém drena
	}
	defer d.shutdown()
	if err := d.WriteStdin("a"); err != nil {
		t.Fatalf("primeira escrita deveria enfileirar: %v", err)
	}
	if err := d.WriteStdin("b"); err == nil {
		t.Fatal("fila cheia deveria devolver erro (nunca descartar em silêncio)")
	}
}
