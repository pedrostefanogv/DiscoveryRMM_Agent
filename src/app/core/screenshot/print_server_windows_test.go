//go:build windows

package screenshot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunPrintServerRejectsInvalidRequests garante que o worker persistente
// responde erro em vez de travar o agente quando o pedido é inválido.
func TestRunPrintServerRejectsInvalidRequests(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader(
		"\n" + // linha vazia é ignorada
			"nao e json\n" +
			"{\"hwnd\":0,\"out\":\"x.png\"}\n" +
			"{\"hwnd\":1,\"out\":\"\"}\n")
	if code := RunPrintServer(in, &out); code != 0 {
		t.Fatalf("RunPrintServer = %d, want 0", code)
	}
	dec := json.NewDecoder(&out)
	responses := []map[string]any{}
	for dec.More() {
		var resp map[string]any
		if err := dec.Decode(&resp); err != nil {
			t.Fatalf("resposta invalida do worker: %v", err)
		}
		responses = append(responses, resp)
	}
	if len(responses) != 3 {
		t.Fatalf("respostas = %d, want 3 (%v)", len(responses), responses)
	}
	for i, resp := range responses {
		if ok, _ := resp["ok"].(bool); ok {
			t.Errorf("resposta %d deveria ser ok=false: %v", i, resp)
		}
		if msg, _ := resp["error"].(string); strings.TrimSpace(msg) == "" {
			t.Errorf("resposta %d sem mensagem de erro: %v", i, resp)
		}
	}
}

// TestPrintWorkerClientProtocolInProcess exercita o protocolo do worker
// persistente ponta a ponta com transporte em memória (o spawn real exige o
// agente elevado — manifest requireAdministrator) e prova que o MESMO worker
// atende vários pedidos, que é o ganho sobre o modo avulso.
func TestPrintWorkerClientProtocolInProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("captura real ignorada em -short")
	}
	wins, err := ListWindows()
	if err != nil || len(wins) == 0 {
		t.Skipf("nenhuma janela disponivel: %v", err)
	}
	var target *WindowInfo
	for i := range wins {
		if !wins[i].Minimized && !wins[i].IsSelf && wins[i].Width > 50 {
			target = &wins[i]
			break
		}
	}
	if target == nil {
		t.Skip("nenhuma janela alvo elegivel")
	}

	serverIn, clientIn := io.Pipe()
	clientOut, serverOut := io.Pipe()
	done := make(chan int, 1)
	go func() { done <- RunPrintServer(serverIn, serverOut) }()

	var client printWorkerClient
	client.stdin = clientIn
	client.stdout = bufio.NewReader(clientOut)

	for i := 0; i < 2; i += 1 {
		out := filepath.Join(t.TempDir(), fmt.Sprintf("pedido-%d.png", i))
		if err := client.requestLocked(uintptr(target.Handle), out); err != nil {
			if strings.Contains(err.Error(), "worker de PrintWindow: ") {
				t.Skipf("PrintWindow do alvo %q falhou no ambiente: %v", target.Title, err)
			}
			t.Fatalf("pedido %d falhou: %v", i, err)
		}
		data, err := os.ReadFile(out)
		if err != nil || len(data) == 0 {
			t.Fatalf("pedido %d: PNG ausente/vazio: %v", i, err)
		}
		if !bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47}) {
			t.Fatalf("pedido %d: assinatura PNG ausente", i)
		}
	}

	_ = clientIn.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("RunPrintServer = %d no EOF, want 0", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker nao encerrou no EOF do stdin")
	}
}

// TestRunPrintServerExitsOnEOF confirma que o worker encerra sozinho quando o
// agente fecha o stdin (sem processo órfão).
func TestRunPrintServerExitsOnEOF(t *testing.T) {
	var out bytes.Buffer
	if code := RunPrintServer(strings.NewReader(""), &out); code != 0 {
		t.Fatalf("RunPrintServer(EOF) = %d, want 0", code)
	}
	if out.Len() != 0 {
		t.Fatalf("esperava nenhuma saida no EOF, obteve %q", out.String())
	}
}
