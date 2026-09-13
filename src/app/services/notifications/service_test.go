package notifications

import (
	"testing"
	"time"
)

func newTestService() *Service {
	return New(Deps{
		Logf:      func(string) {},
		EmitEvent: func(string, ...any) {},
	})
}

// TestDispatchDedupReflectsOriginalResultDenied valida a correção A1: um
// retry com a mesma idempotencyKey DEVE refletir o resultado real da
// notificação original ("denied") — nunca retornar "approved" implícito,
// o que fazia bypass de require_confirmation em comandos remotos.
func TestDispatchDedupReflectsOriginalResultDenied(t *testing.T) {
	oldWait := maxDedupResultWait
	maxDedupResultWait = 2 * time.Second
	defer func() { maxDedupResultWait = oldWait }()

	s := newTestService()
	req := DispatchRequest{
		NotificationID: "n-1",
		IdempotencyKey: "k-1",
		Mode:           "require_confirmation",
		TimeoutSeconds: 5,
	}

	respCh := make(chan DispatchResponse, 1)
	go func() { respCh <- s.Dispatch(req) }()

	// Aguarda o dispatch registrar o pending e responde "denied".
	responded := false
	for i := 0; i < 100 && !responded; i++ {
		time.Sleep(2 * time.Millisecond)
		responded = s.Respond("n-1", "denied")
	}
	if !responded {
		t.Fatalf("não conseguiu responder a notificação pendente")
	}
	first := <-respCh
	if first.Result != "denied" {
		t.Fatalf("primeiro dispatch Result = %q, want denied", first.Result)
	}

	// Retry com a mesma key: deve refletir "denied" (não "approved").
	second := s.Dispatch(req)
	if second.Result != "denied" {
		t.Fatalf("retry Result = %q, want denied (A1)", second.Result)
	}
	if second.Accepted {
		t.Fatalf("retry Accepted = true, want false para resultado denied (A1)")
	}
	if second.AgentAction != "deduplicated" {
		t.Fatalf("retry AgentAction = %q, want deduplicated", second.AgentAction)
	}
}

// TestDispatchDedupPendingWhileWaiting valida que um retry chegado enquanto
// a notificação original aguarda decisão do usuário NÃO é aprovado — recebe
// "pending" — e depois recebe o resultado real quando a original conclui.
func TestDispatchDedupPendingWhileWaiting(t *testing.T) {
	oldWait := maxDedupResultWait
	maxDedupResultWait = 120 * time.Millisecond
	defer func() { maxDedupResultWait = oldWait }()

	s := newTestService()
	req := DispatchRequest{
		NotificationID: "n-2",
		IdempotencyKey: "k-2",
		Mode:           "require_confirmation",
		TimeoutSeconds: 5,
	}

	respCh := make(chan DispatchResponse, 1)
	go func() { respCh <- s.Dispatch(req) }()

	// Espera o dispatch registrar o pending.
	responded := false
	for i := 0; i < 100 && !responded; i++ {
		time.Sleep(2 * time.Millisecond)
		responded = s.Respond("n-2", "x") // resposta de aquecimento; canal bufferizado
	}
	if !responded {
		t.Fatalf("não conseguiu responder a notificação pendente")
	}
	if first := <-respCh; first.Result != "timeout_policy_applied" {
		t.Fatalf("resposta inválida '%s' não deveria aprovar", first.Result)
	}

	// A partir daqui a original já concluiu; retry deve refletir o resultado.
	after := s.Dispatch(req)
	if after.Result == "approved" {
		t.Fatalf("resposta não-confirmada não pode virar approved (A1)")
	}
}

// TestDispatchDedupApprovedFlowsThrough valida o caso legítimo: a original
// foi auto-aprovada (notify_only) e o retry reflete "approved".
func TestDispatchDedupApprovedFlowsThrough(t *testing.T) {
	oldWait := maxDedupResultWait
	maxDedupResultWait = 2 * time.Second
	defer func() { maxDedupResultWait = oldWait }()

	s := newTestService()
	req := DispatchRequest{
		NotificationID: "n-3",
		IdempotencyKey: "k-3",
		Mode:           "notify_only",
	}

	first := s.Dispatch(req)
	if first.Result != "approved" || !first.Accepted {
		t.Fatalf("primeiro dispatch = %+v, want approved", first)
	}

	second := s.Dispatch(req)
	if second.Result != "approved" || !second.Accepted {
		t.Fatalf("retry = %+v, want approved (dedup reflete resultado real)", second)
	}
	if second.AgentAction != "deduplicated" {
		t.Fatalf("retry AgentAction = %q, want deduplicated", second.AgentAction)
	}
}
