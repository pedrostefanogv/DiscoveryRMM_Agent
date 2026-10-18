//go:build windows

package app

// Teste de INTEGRAÇÃO com named pipe REAL (o que faltava no incidente de
// 2026-10-17): todos os testes do canal admin usavam net.Pipe em memória, então
// um nome de pipe inválido (ListenPipe → "Incorrect function") passava batido e
// o serviço subia com TODAS as mutações bloqueadas.

import (
	"bufio"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

// TestIPCRealNamedPipes cria de fato os dois pipes cujos nomes derivam das
// constantes de produção e faz um round-trip JSON-lines em cada um.
func TestIPCRealNamedPipes(t *testing.T) {
	// Sufixo por processo: não colide com um serviço real rodando na máquina.
	suffix := "-test-" + strconv.Itoa(os.Getpid())
	t.Run("leitura", func(t *testing.T) {
		ipcTestRealPipe(t, IPCPipeName+suffix, ipcReadPipeSDDL)
	})
	t.Run("admin", func(t *testing.T) {
		ipcTestRealPipe(t, IPCPipeAdminName+suffix, ipcAdminPipeSDDL)
	})
}

// ipcTestRealPipe valida o nome, cria o pipe no SO, conecta e troca um
// hello/hello_ack. Se o nome de produção estiver malformado, ListenPipe falha
// aqui — exatamente o erro que só aparecia no log do serviço em produção.
func ipcTestRealPipe(t *testing.T, name, sddl string) {
	t.Helper()
	if err := validateIPCPipeName(name); err != nil {
		t.Fatalf("validateIPCPipeName(%q): %v", name, err)
	}
	l, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: sddl, MessageMode: false})
	if err != nil {
		t.Fatalf("ListenPipe(%q) falhou: %v — este nome quebraria as mutações em produção", name, err)
	}
	defer l.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, aErr := l.Accept()
		if aErr != nil {
			serverErr <- aErr
			return
		}
		defer conn.Close()
		msg, dErr := DecodeIPCMessage(bufio.NewReader(conn))
		if dErr != nil {
			serverErr <- dErr
			return
		}
		_, wErr := conn.Write(EncodeIPCMessage(NewIPCMessage(IPCMsgHelloAck, map[string]any{"echo": string(msg.Type)})))
		serverErr <- wErr
	}()

	timeout := 5 * time.Second
	conn, err := winio.DialPipe(name, &timeout)
	if err != nil {
		t.Fatalf("DialPipe(%q) falhou: %v", name, err)
	}
	defer conn.Close()

	if _, err := conn.Write(EncodeIPCMessage(NewIPCMessage(IPCMsgHello, map[string]any{"pid": os.Getpid()}))); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	ack, err := DecodeIPCMessage(bufio.NewReader(conn))
	if err != nil {
		t.Fatalf("decode hello_ack: %v", err)
	}
	if ack.Type != IPCMsgHelloAck {
		t.Fatalf("tipo da resposta = %q, esperado %q", ack.Type, IPCMsgHelloAck)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("servidor: %v", err)
	}
}
