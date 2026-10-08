package ai

import (
	"testing"
	"time"
)

// Regressão 2026-10-08 (revisão de bugs): um clique A2UI podia ficar na fila
// enquanto um turno longo rodava (ou num card antigo esquecido no chat). Sem
// TTL, esse clique era executado minutos depois, quando o usuário já tinha
// mudado de contexto.
func TestTakeA2uiAction_DiscardsExpired(t *testing.T) {
	s := NewService(nil)
	s.SubmitA2uiAction("surf", "step_next", map[string]any{})

	s.a2uiActionMu.Lock()
	s.a2uiActions[0].At = time.Now().Add(-3 * time.Minute)
	s.a2uiActionMu.Unlock()

	if got := s.takeA2uiAction(); got != nil {
		t.Fatalf("ação expirada deveria ser descartada, obtida %#v", got)
	}
}

func TestTakeA2uiAction_KeepsFreshAfterExpired(t *testing.T) {
	s := NewService(nil)
	s.SubmitA2uiAction("surf", "old", map[string]any{})
	s.SubmitA2uiAction("surf", "new", map[string]any{})

	s.a2uiActionMu.Lock()
	s.a2uiActions[0].At = time.Now().Add(-5 * time.Minute)
	s.a2uiActionMu.Unlock()

	got := s.takeA2uiAction()
	if got == nil || got.Name != "new" {
		t.Fatalf("esperada a ação fresca 'new', obtida %#v", got)
	}
}
