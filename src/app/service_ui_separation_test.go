package app

// Testes da Fase C2 (uiOnline no heartbeat) e Fase C (IPC request/response)
// do PLANO_SEPARACAO_SERVICO_UI.md.

import (
	"bufio"
	"bytes"
	"context"
	"testing"
	"time"

	"discovery/app/services/notifications"
)

func newTestBufioReader(b []byte) *bufio.Reader {
	return bufio.NewReader(bytes.NewReader(b))
}

func TestHeartbeatUIOnline_NilOutsideServiceMode(t *testing.T) {
	a := NewApp(AppStartupOptions{ServiceMode: false})
	m := a.getHeartbeatMetrics()
	if m.UIOnline != nil {
		t.Fatal("UIOnline deve ser nil fora do modo serviço (campo omitido no JSON)")
	}
}

func TestHeartbeatUIOnline_FalseWhenNoUIConnected(t *testing.T) {
	a := NewApp(AppStartupOptions{ServiceMode: true})
	// ipcServer nil no teste (StartIPCServer só roda em runtime Windows real
	// com pipe) — ClientCount de um server nil não é consultado; o campo só é
	// preenchido quando ipcServer != nil. Aqui validamos que sem server o
	// campo permanece nil.
	m := a.getHeartbeatMetrics()
	if m.UIOnline != nil {
		t.Fatal("UIOnline deve ser nil quando ipcServer não existe")
	}
}

func TestNativeToastHelpersNilSafe(t *testing.T) {
	// Hooks de ativação: set/get não podem panicar com fn nil.
	setToastActivationHook(nil)
	setToastActivationHook(func(notificationID, result string) {})
	setToastActivationHook(nil)
}

func TestIPCMessageCorrelationIDRoundTrip(t *testing.T) {
	msg := NewIPCMessage(IPCMsgRequest, map[string]any{"method": "memory:list"})
	msg.CorrelationID = "r-123-1"
	data := EncodeIPCMessage(msg)
	decoded, err := DecodeIPCMessage(newTestBufioReader(data))
	if err != nil {
		t.Fatalf("DecodeIPCMessage: %v", err)
	}
	if decoded.CorrelationID != "r-123-1" {
		t.Fatalf("correlation id esperado r-123-1, got %q", decoded.CorrelationID)
	}
	if decoded.Payload["method"] != "memory:list" {
		t.Fatalf("method esperado memory:list, got %v", decoded.Payload["method"])
	}
}

func TestDispatchHeadlessRequireConfirmationWaitsForToastResponse(t *testing.T) {
	// Revisão 2 — bug B5: no caminho headless, require_confirmation deve
	// aguardar a resposta do toast (janela de timeout) em vez de retornar
	// timeout_policy_applied imediatamente. A resposta chega via Respond
	// (mesmo caminho do hook de ativação do toast).
	var svc *notifications.Service
	svc = notifications.New(notifications.Deps{
		Ctx: nil, // headless: sem UI
		NativeFallback: func(req notifications.DispatchRequest) {
			// Simula o clique do usuário no toast logo após o dispatch.
			go func() {
				time.Sleep(50 * time.Millisecond)
				svc.Respond(req.NotificationID, "approved")
			}()
		},
	})
	resp := svc.Dispatch(notifications.DispatchRequest{
		NotificationID: "toast-test-1",
		Title:          "Teste",
		Message:        "Confirme",
		Mode:           "require_confirmation",
		TimeoutSeconds: 3,
	})
	if resp.Result != "approved" || resp.AgentAction != "user_decision" {
		t.Fatalf("esperado approved/user_decision, got %s/%s", resp.Result, resp.AgentAction)
	}
}

func TestDispatchHeadlessNotifyOnlyApproved(t *testing.T) {
	// Revisão 2: notify_only headless continua approved/headless_logged,
	// e o NativeFallback é invocado (toast informativo).
	called := false
	svc := notifications.New(notifications.Deps{
		Ctx: nil,
		NativeFallback: func(req notifications.DispatchRequest) {
			called = true
		},
	})
	resp := svc.Dispatch(notifications.DispatchRequest{
		NotificationID: "toast-test-2",
		Mode:           "notify_only",
	})
	if resp.Result != "approved" || resp.AgentAction != "headless_logged" {
		t.Fatalf("esperado approved/headless_logged, got %s/%s", resp.Result, resp.AgentAction)
	}
	if !called {
		t.Fatal("NativeFallback deve ser invocado no caminho headless")
	}
}

func TestDispatchWithContextNoNativeFallback(t *testing.T) {
	// Com UI presente (ctx não nil), o NativeFallback NÃO deve disparar —
	// a UI renderiza via emitEvent.
	called := false
	calledEmit := false
	svc := notifications.New(notifications.Deps{
		Ctx: func() interface{ Done() <-chan struct{} } {
			return context.Background()
		},
		NativeFallback: func(req notifications.DispatchRequest) {
			called = true
		},
		EmitEvent: func(name string, _ ...any) {
			if name == "notification:new" {
				calledEmit = true
			}
		},
	})
	resp := svc.Dispatch(notifications.DispatchRequest{
		NotificationID: "toast-test-3",
		Mode:           "notify_only",
	})
	if called {
		t.Fatal("NativeFallback não deve disparar com UI presente")
	}
	if !calledEmit {
		t.Fatal("emitEvent deve ser chamado com UI presente")
	}
	if resp.AgentAction != "rendered" {
		t.Fatalf("esperado rendered, got %s", resp.AgentAction)
	}
}
