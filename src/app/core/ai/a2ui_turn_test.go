package ai

import (
	"testing"
)

// Regressão do ciclo de vida das ações A2UI (bug exposto quando as surfaces
// passaram a renderizar): o clique podia ser perdido e a sentinela
// "__a2ui_action__" ia para o LLM como mensagem do usuário.
func TestResolveA2uiTurn_SentinelConsumesQueuedAction(t *testing.T) {
	s := NewService(nil)
	s.SubmitA2uiAction("surf-1", "open_ticket", map[string]any{"id": "42"})

	action, isA2uiTurn, err := s.resolveA2uiTurn(A2uiActionSentinel)
	if err != nil {
		t.Fatalf("sentinela com ação pendente não deveria falhar: %v", err)
	}
	if !isA2uiTurn {
		t.Fatal("turno deveria ser reconhecido como A2UI")
	}
	if action == nil || action.Name != "open_ticket" || action.SurfaceID != "surf-1" {
		t.Fatalf("ação errada consumida: %#v", action)
	}
	if s.peekA2uiAction() {
		t.Fatal("a fila deveria ficar vazia após consumir a ação")
	}
}

func TestResolveA2uiTurn_SentinelWithoutActionFails(t *testing.T) {
	s := NewService(nil)

	_, isA2uiTurn, err := s.resolveA2uiTurn(A2uiActionSentinel)
	if !isA2uiTurn {
		t.Fatal("sentinela deveria ser reconhecida mesmo sem ação")
	}
	if err == nil {
		t.Fatal("sentinela órfã deveria devolver erro claro em vez de seguir para o LLM")
	}
}

func TestResolveA2uiTurn_TypedMessageDiscardsOrphanAction(t *testing.T) {
	s := NewService(nil)
	s.SubmitA2uiAction("surf-1", "stale_click", nil)

	action, isA2uiTurn, err := s.resolveA2uiTurn("qual o uso de memoria?")
	if err != nil {
		t.Fatalf("mensagem digitada não deveria falhar: %v", err)
	}
	if isA2uiTurn || action != nil {
		t.Fatalf("mensagem digitada não pode virar turno A2UI (action=%#v)", action)
	}
	if s.peekA2uiAction() {
		t.Fatal("ação órfã deveria ser descartada no início de um turno digitado")
	}
}

func TestResolveA2uiTurn_TypedMessageWithoutQueue(t *testing.T) {
	s := NewService(nil)

	action, isA2uiTurn, err := s.resolveA2uiTurn("bom dia")
	if err != nil || isA2uiTurn || action != nil {
		t.Fatalf("turno normal: action=%#v isA2ui=%v err=%v", action, isA2uiTurn, err)
	}
}

// A sentinela não pode vazar para o LLM nem pelos caminhos que não usam
// resolveA2uiTurn (Send síncrono / stream single-round).
func TestValidateChatMessageRejectsA2uiSentinel(t *testing.T) {
	if err := validateChatMessage(A2uiActionSentinel); err == nil {
		t.Fatal("validateChatMessage deveria rejeitar a sentinela interna")
	}
	if err := validateChatMessage("  " + A2uiActionSentinel + "  "); err == nil {
		t.Fatal("validateChatMessage deveria rejeitar a sentinela com espaços")
	}
}
