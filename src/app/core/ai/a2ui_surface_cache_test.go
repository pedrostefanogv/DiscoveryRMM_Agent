package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regressão 2026-10-08 (stepper): clicar em "Avançar" devolvia só prosa e a
// surface não mudava de passo. O tool result do clique precisa (1) instruir o
// LLM a reemitir a MESMA surface em updateComponents quando a ação é de
// navegação e (2) levar o snapshot da última definição — o histórico persistido
// no servidor não tem o bloco a2ui para o modelo se basear.
func TestBuildA2uiActionToolResult_CarriesSnapshotAndNavigationHint(t *testing.T) {
	surface := `{"version":"v0.9","updateComponents":{"surfaceId":"stepper_demo","components":[{"id":"root","component":"Column"}]}}`
	action := &A2uiAction{SurfaceID: "stepper_demo", Name: "step_next", Context: map[string]any{}}

	raw := buildA2uiActionToolResult(action, surface)
	if !json.Valid([]byte(raw)) {
		t.Fatalf("tool result não é JSON válido: %s", raw)
	}

	var payload struct {
		SurfaceID   string          `json:"surfaceId"`
		Name        string          `json:"name"`
		Instruction string          `json:"instruction"`
		Surface     json.RawMessage `json:"surface"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.SurfaceID != "stepper_demo" || payload.Name != "step_next" {
		t.Fatalf("identificação da ação perdida: %#v", payload)
	}
	if !strings.Contains(payload.Instruction, "updateComponents") {
		t.Fatalf("instrução não cobre navegação/updateComponents: %q", payload.Instruction)
	}
	if !strings.Contains(payload.Instruction, "NUNCA") {
		t.Fatalf("instrução não proíbe afirmar mudança sem bloco a2ui: %q", payload.Instruction)
	}
	if len(payload.Surface) == 0 || !json.Valid(payload.Surface) {
		t.Fatalf("snapshot da surface ausente/inválido: %s", raw)
	}
}

func TestBuildA2uiActionToolResult_WithoutSnapshot(t *testing.T) {
	raw := buildA2uiActionToolResult(&A2uiAction{SurfaceID: "surf", Name: "upgrade_all"}, "")
	if strings.Contains(raw, `"surface"`) {
		t.Fatalf("sem snapshot o campo surface não deveria existir: %s", raw)
	}
	if !json.Valid([]byte(raw)) {
		t.Fatalf("tool result inválido: %s", raw)
	}
}

func TestRememberA2uiSurface_KeepsDefinitionAndForgetsDeleted(t *testing.T) {
	s := NewService(nil)
	def := `{"version":"v0.9","updateComponents":{"surfaceId":"stepper_demo","components":[]}}`
	s.rememberA2uiSurface(`{"version":"v0.9","createSurface":{"surfaceId":"stepper_demo"}}`)
	s.rememberA2uiSurface(def)

	if got := s.a2uiSurfaceSnapshot("stepper_demo"); got != def {
		t.Fatalf("snapshot = %q, esperado a definição", got)
	}

	s.rememberA2uiSurface(`{"version":"v0.9","deleteSurface":{"surfaceId":"stepper_demo"}}`)
	if got := s.a2uiSurfaceSnapshot("stepper_demo"); got != "" {
		t.Fatalf("deleteSurface deveria limpar o cache, obtido %q", got)
	}
}

func TestRememberA2uiSurface_EvictsOldest(t *testing.T) {
	s := NewService(nil)
	for i := 0; i < maxA2uiSurfaceCache+2; i++ {
		id := "surf-" + string(rune('a'+i))
		s.rememberA2uiSurface(`{"version":"v0.9","updateComponents":{"surfaceId":"` + id + `"}}`)
	}
	if got := s.a2uiSurfaceSnapshot("surf-a"); got != "" {
		t.Fatalf("surface mais antiga deveria ter sido evictada, obtido %q", got)
	}
	if got := s.a2uiSurfaceSnapshot("surf-j"); got == "" {
		t.Fatalf("surface mais recente deveria estar no cache")
	}
}
