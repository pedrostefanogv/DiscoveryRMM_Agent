package supportchat

import (
	"context"
	"testing"
	"time"
)

// ── Espera pelo estado da sessão depois do aceite ──────────────────────────
//
// O POST de aceite e o GET /active da janela corriam em paralelo. Quando a
// janela abria antes de o servidor refletir o aceite, o snapshot voltava ainda
// "waiting" (RefreshKeepingInvite o preserva) e a janela pintava o aviso de
// reconexão numa conversa que já estava aberta. AwaitSessionAfterAccept espera o
// estado, com teto de tempo — nunca bloqueia a abertura.

func TestAwaitSessionAfterAccept_RetornaQuandoASessaoAbre(t *testing.T) {
	svc := New(Deps{})
	svc.mu.Lock()
	svc.state.Status = StatusWaiting
	svc.state.SessionID = "sess-1"
	svc.mu.Unlock()

	go func() {
		time.Sleep(50 * time.Millisecond)
		svc.mu.Lock()
		svc.state.Status = StatusActive
		svc.mu.Unlock()
	}()

	start := time.Now()
	state := svc.AwaitSessionAfterAccept(context.Background(), "sess-1", 2*time.Second)
	elapsed := time.Since(start)

	if state.Status != StatusActive {
		t.Fatalf("deveria devolver o estado já aberto; obtido %q", state.Status)
	}
	if elapsed > time.Second {
		t.Fatalf("a espera deveria terminar assim que a sessão abre (levou %s)", elapsed)
	}
}

func TestAwaitSessionAfterAccept_TimeoutDevolveEstadoAtual(t *testing.T) {
	svc := New(Deps{})
	svc.mu.Lock()
	svc.state.Status = StatusWaiting
	svc.state.SessionID = "sess-1"
	svc.mu.Unlock()

	start := time.Now()
	state := svc.AwaitSessionAfterAccept(context.Background(), "sess-1", 120*time.Millisecond)
	elapsed := time.Since(start)

	if state.Status != StatusWaiting {
		t.Fatalf("sem evento o estado atual deve ser devolvido; obtido %q", state.Status)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("a espera foi curta demais (não esperou o prazo): %s", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("a espera precisa respeitar o teto (levou %s)", elapsed)
	}
}

func TestAwaitSessionAfterAccept_SemSessaoNaoEspera(t *testing.T) {
	svc := New(Deps{})
	start := time.Now()
	svc.AwaitSessionAfterAccept(context.Background(), "", time.Second)
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("sem sessionId não há o que esperar; levou %s", elapsed)
	}
}
