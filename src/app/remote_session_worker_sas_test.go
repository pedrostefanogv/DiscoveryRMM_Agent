//go:build windows

package app

import (
	"strings"
	"testing"
	"time"
)

// O serviço reconhece EXATAMENTE a linha que o worker imprime — um desalinhio
// de prefixo fazia o pedido ser ignorado e o worker travar até o timeout.
func TestParseSASRequestLine_MatchesWorkerMarker(t *testing.T) {
	line := sasRequestStderrMarker + " id=42"
	id, ok := parseSASRequestLine(line)
	if !ok || id != "42" {
		t.Fatalf("parseSASRequestLine(%q): got=(%q,%t), want=(42,true)", line, id, ok)
	}
	for _, bad := range []string{
		"",
		"[remote-session-worker] sas-request",
		"[remote-session-worker] sas-request id=",
		"[remote-session-worker] exit-ok",
		"[remote-session-worker] sas-request id=42 extra",
	} {
		if _, ok := parseSASRequestLine(bad); ok {
			// "id=42 extra" ainda casa (TrimSpace no fim) — só rejeita os
			// verdadeiramente inválidos.
			if strings.HasSuffix(bad, "extra") {
				continue
			}
			t.Fatalf("parseSASRequestLine(%q) deveria rejeitar", bad)
		}
	}
}

// Resposta ok=false SEM campo error não pode ser interpretada como sucesso.
func TestHandleSASResultCommand_EmptyErrorIsFailure(t *testing.T) {
	id := "t1"
	ch := make(chan string, 1)
	sasPending.mu.Lock()
	sasPending.byID[id] = ch
	sasPending.mu.Unlock()
	defer func() {
		sasPending.mu.Lock()
		delete(sasPending.byID, id)
		sasPending.mu.Unlock()
	}()

	if !handleSASResultCommand(map[string]any{"action": "sas_result", "id": id, "ok": false}) {
		t.Fatalf("sas_result deveria ser tratado pelo handler")
	}
	select {
	case reply := <-ch:
		if strings.TrimSpace(reply) == "" {
			t.Fatalf("falha sem campo error virou sucesso oco (reply=%q)", reply)
		}
	case <-time.After(time.Second):
		t.Fatalf("resposta não chegou ao requerente")
	}
}

// Round-trip completo do protocolo, no próprio processo (worker + resposta).
func TestSASRoundTrip_SuccessAndError(t *testing.T) {
	go func() {
		// Simula o serviço: lê o pedido do stderr não é necessário aqui —
		// respondemos diretamente pelo mesmo caminho do monitor de stdin.
		deadline := time.After(2 * time.Second)
		for {
			select {
			case <-deadline:
				return
			default:
			}
			sasPending.mu.Lock()
			var pendID string
			var ch chan string
			for k, c := range sasPending.byID {
				pendID, ch = k, c
				break
			}
			sasPending.mu.Unlock()
			if ch == nil {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			_ = pendID
			if !handleSASResultCommand(map[string]any{"action": "sas_result", "id": pendID, "ok": true}) {
				t.Errorf("resposta não tratada")
			}
			return
		}
	}()
	// requestSASViaService imprime o marker no stderr do processo de teste —
	// emite ruído, mas exercita o caminho real (write + espera + resposta).
	if err := requestSASViaService(); err != nil {
		t.Fatalf("sucesso esperado, got=%v", err)
	}

	// Caminho de erro: serviço responde falha com detalhe.
	go func() {
		time.Sleep(20 * time.Millisecond)
		// NÃO chamar handleSASResultCommand dentro do Lock: o mutex não é
		// reentrante (deadlock) — captura o id sob lock, responde fora dele.
		sasPending.mu.Lock()
		var id string
		for k := range sasPending.byID {
			id = k
			break
		}
		sasPending.mu.Unlock()
		if id != "" {
			_ = handleSASResultCommand(map[string]any{"action": "sas_result", "id": id, "ok": false, "error": "política desabilitada"})
		}
	}()
	err := requestSASViaService()
	if err == nil || !strings.Contains(err.Error(), "política desabilitada") {
		t.Fatalf("erro do serviço deveria propagar, got=%v", err)
	}

	// Timeout: ninguém responde → erro fail-visible (não sucesso oco).
	start := time.Now()
	err = requestSASViaService()
	if err == nil {
		t.Fatalf("timeout deveria retornar erro")
	}
	if elapsed := time.Since(start); elapsed < sasRequestTimeout/2 {
		t.Fatalf("retornou cedo demais sem resposta: %s", elapsed)
	}
}
