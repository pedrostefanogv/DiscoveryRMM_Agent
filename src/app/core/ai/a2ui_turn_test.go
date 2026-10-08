package ai

import (
	"strings"
	"testing"
)

// Regressão 2026-10-08: o CallID do tool result A2UI era "a2ui_<surfaceId>",
// repetido a cada clique na mesma surface. Ids repetidos na sessão quebram o
// pareamento tool_call/tool_message do provedor — cada clique precisa do seu.
func TestA2uiActionCallID_UniquePerClick(t *testing.T) {
	s := NewService(nil)
	ctx := map[string]any{"id": "Initex.YogaDNS"}
	s.SubmitA2uiAction("updates_card", "upgrade_package", ctx)
	s.SubmitA2uiAction("updates_card", "upgrade_package", ctx)

	first := s.takeA2uiAction()
	second := s.takeA2uiAction()
	if first == nil || second == nil {
		t.Fatalf("as duas ações deveriam estar na fila (first=%#v second=%#v)", first, second)
	}

	id1, id2 := a2uiActionCallID(first), a2uiActionCallID(second)
	if id1 == id2 {
		t.Fatalf("callIds devem ser únicos por clique, ambos = %q", id1)
	}
	if !strings.HasPrefix(id1, "a2ui_updates_card_") {
		t.Fatalf("callId inesperado: %q", id1)
	}
	if first.Name != "upgrade_package" || first.SurfaceID != "updates_card" {
		t.Fatalf("ação alterada: %#v", first)
	}
}

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
