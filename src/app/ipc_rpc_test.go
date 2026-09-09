package app

// Testes de melhorias da revisão 2 (PLANO_SEPARACAO_SERVICO_UI.md §0.3):
// cobertura dos caminhos IPC RPC e do ciclo headless de notificações.

import (
	"testing"
	"time"

	"discovery/app/services/notifications"
)

// ── handleIPCRequest (RPC do serviço) ──────────────────────────────────────

func TestHandleIPCRequestNilGuards(t *testing.T) {
	var aNil *App
	if resp := aNil.handleIPCRequest(nil, nil); resp["ok"] != false {
		t.Fatal("App nil deve retornar ok=false")
	}
	a := &App{}
	if resp := a.handleIPCRequest(nil, nil); resp["ok"] != false {
		t.Fatal("payload nil deve retornar ok=false")
	}
}

func TestHandleIPCRequestUnknownMethod(t *testing.T) {
	a := &App{}
	resp := a.handleIPCRequest(nil, map[string]any{"method": "inexistente:x"})
	if resp["ok"] != false {
		t.Fatal("método desconhecido deve retornar ok=false")
	}
	if msg, _ := resp["error"].(string); msg == "" {
		t.Fatal("resposta deve incluir mensagem de erro")
	}
}

func TestHandleIPCRequestMemorySvcIndisponivel(t *testing.T) {
	// memorySvc nil → ok=false com erro (sem panic).
	a := &App{}
	for _, method := range []string{"memory:list", "memory:add", "memory:delete"} {
		resp := a.handleIPCRequest(nil, map[string]any{"method": method})
		if resp["ok"] != false {
			t.Fatalf("%s com memorySvc nil deve retornar ok=false", method)
		}
	}
}

func TestHandleIPCRequestPendingCountsNoDB(t *testing.T) {
	// Sem DB (companion nunca abre o SQLite — D3): ok=true com data vazio,
	// nunca panic.
	a := &App{}
	resp := a.handleIPCRequest(nil, map[string]any{"method": "status:pending_counts"})
	if resp["ok"] != true {
		t.Fatal("status:pending_counts sem DB deve retornar ok=true (data vazio)")
	}
}

func TestHandleIPCRequestDeletesMethodKey(t *testing.T) {
	// Contrato: o handler consome "method" antes do switch (payload limpo
	// para os parâmetros restantes).
	a := &App{}
	payload := map[string]any{"method": "metodo:falho"}
	_ = a.handleIPCRequest(nil, payload)
	if _, still := payload["method"]; still {
		t.Fatal("handleIPCRequest deve remover 'method' do payload")
	}
}

// ── handleIPCMessage (roteamento geral, nil-safe) ──────────────────────────

func TestHandleIPCMessageNilGuards(t *testing.T) {
	var aNil *App
	aNil.handleIPCMessage(nil, IPCMessage{}) // não pode panic
	a := &App{}
	a.handleIPCMessage(nil, IPCMessage{}) // sem type — ignorado
	a.handleIPCMessage(nil, IPCMessage{Type: IPCMsgRequest, Payload: map[string]any{"method": "metodo:falho"}})
	// ipcServer nil em IPCMsgStatus/IPCMsgRemoteSession/IPCMsgRequest — não pode panic.
	a.handleIPCMessage(nil, IPCMessage{Type: IPCMsgStatus})
	a.handleIPCMessage(nil, IPCMessage{Type: IPCMsgRemoteSession})
	a.handleIPCMessage(nil, IPCMessage{Type: IPCMsgNotificationRespond, Payload: map[string]any{"notificationId": "n1", "result": "approved"}})
	a.handleIPCMessage(nil, IPCMessage{Type: "tipo:desconhecido"})
}

// ── Ciclo headless: timeout + idempotência ─────────────────────────────────

func TestDispatchHeadlessRequireConfirmationTimeout(t *testing.T) {
	// B5 — sem resposta no toast, o dispatch deve aguardar a janela de
	// TimeoutSeconds e retornar timeout_policy_applied (não retorno imediato).
	svc := notifications.New(notifications.Deps{
		Ctx: nil,
	})
	start := time.Now()
	resp := svc.Dispatch(notifications.DispatchRequest{
		NotificationID: "toast-timeout-1",
		Mode:           "require_confirmation",
		TimeoutSeconds: 1,
	})
	elapsed := time.Since(start)
	if resp.Result != "timeout_policy_applied" || resp.AgentAction != "timeout" {
		t.Fatalf("esperado timeout_policy_applied/timeout, got %s/%s", resp.Result, resp.AgentAction)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("dispatch retornou antes da janela de timeout: %v", elapsed)
	}
}

func TestRespondAfterPendingExpiry(t *testing.T) {
	// Respond após o dispatch expirar deve retornar false (canal removido).
	svc := notifications.New(notifications.Deps{Ctx: nil})
	done := make(chan struct{})
	go func() {
		svc.Dispatch(notifications.DispatchRequest{
			NotificationID: "toast-late-1",
			Mode:           "require_confirmation",
			TimeoutSeconds: 1,
		})
		close(done)
	}()
	<-done
	time.Sleep(50 * time.Millisecond) // defer de limpeza do pending
	if svc.Respond("toast-late-1", "approved") {
		t.Fatal("Respond após expiração deve retornar false")
	}
}

func TestRespondNormalizesResult(t *testing.T) {
	// Respond com resultado não normalizado ("ADIADO") deve ser aceito e
	// normalizado para "deferred" no Dispatch.
	var svc *notifications.Service
	svc = notifications.New(notifications.Deps{
		Ctx: nil,
		NativeFallback: func(req notifications.DispatchRequest) {
			go func() {
				time.Sleep(30 * time.Millisecond)
				svc.Respond(req.NotificationID, "ADIADO") // variação de caixa
			}()
		},
	})
	resp := svc.Dispatch(notifications.DispatchRequest{
		NotificationID: "toast-norm-1",
		Mode:           "require_confirmation",
		TimeoutSeconds: 2,
	})
	if resp.Result != "deferred" {
		t.Fatalf("esperado deferred (normalizado de ADIADO), got %s", resp.Result)
	}
}

func TestDispatchIdempotencyKeyDeduplicates(t *testing.T) {
	// Mesma IdempotencyKey → segunda chamada é deduplicada (approved/deduplicated).
	svc := notifications.New(notifications.Deps{Ctx: nil})
	req := notifications.DispatchRequest{
		NotificationID: "idem-1",
		IdempotencyKey: "key-1",
		Mode:           "notify_only",
	}
	first := svc.Dispatch(req)
	second := svc.Dispatch(req)
	if first.Result != "approved" {
		t.Fatalf("primeiro dispatch esperado approved, got %s", first.Result)
	}
	if second.AgentAction != "deduplicated" {
		t.Fatalf("segundo dispatch esperado deduplicated, got %s", second.AgentAction)
	}
}
