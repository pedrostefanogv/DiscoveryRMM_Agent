package screen

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAnnotationStore_AddUndoClear(t *testing.T) {
	store := NewAnnotationStore()
	now := time.Unix(1000, 0)

	tools := []AnnotationTool{
		AnnotationToolArrow,
		AnnotationToolRect,
		AnnotationToolEllipse,
		AnnotationToolFreehand,
		AnnotationToolLaser,
	}

	for i, tool := range tools {
		cmd := AnnotationCommand{
			Op:     "add",
			ID:     string(rune('a' + i)),
			Tool:   string(tool),
			Color:  "#0A84FF",
			Width:  6,
			Points: []AnnotationPoint{{X: -0.5, Y: 0.2}, {X: 1.5, Y: 0.8}},
		}
		raw, err := json.Marshal(cmd)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		changed, err := store.Apply(raw, now)
		if err != nil {
			t.Fatalf("Apply(%s) falhou: %v", tool, err)
		}
		if !changed {
			t.Fatalf("Apply(%s) esperava changed=true", tool)
		}
	}

	shapes, nextWake := store.Snapshot(now)
	if len(shapes) != len(tools) {
		t.Fatalf("esperava %d shapes, obteve %d", len(tools), len(shapes))
	}
	if nextWake <= 0 {
		t.Fatalf("laser deveria agendar nextWake > 0, obteve %v", nextWake)
	}
	if shapes[0].Points[0].X != 0 || shapes[0].Points[1].X != 1 {
		t.Fatalf("coordenadas não foram clampeadas para [0,1]: %+v", shapes[0].Points)
	}
	if shapes[0].ColorRGB != 0x0A84FF {
		t.Fatalf("cor inesperada: %#x", shapes[0].ColorRGB)
	}

	// Undo por ID específico
	changed, err := store.Apply([]byte(`{"op":"undo","id":"b"}`), now)
	if err != nil || !changed {
		t.Fatalf("undo por id falhou: changed=%v err=%v", changed, err)
	}
	shapes, _ = store.Snapshot(now)
	if len(shapes) != len(tools)-1 {
		t.Fatalf("esperava %d shapes após undo por ID, obteve %d", len(tools)-1, len(shapes))
	}

	// Undo da última forma
	changed, err = store.Apply([]byte(`{"op":"undo"}`), now)
	if err != nil || !changed {
		t.Fatalf("undo última falhou: changed=%v err=%v", changed, err)
	}
	shapes, _ = store.Snapshot(now)
	if len(shapes) != len(tools)-2 {
		t.Fatalf("esperava %d shapes após undo, obteve %d", len(tools)-2, len(shapes))
	}

	// Clear
	changed, err = store.Apply([]byte(`{"op":"clear"}`), now)
	if err != nil || !changed {
		t.Fatalf("clear falhou: changed=%v err=%v", changed, err)
	}
	shapes, nextWake = store.Snapshot(now)
	if len(shapes) != 0 || nextWake != 0 {
		t.Fatalf("esperava store vazio após clear (len=%d nextWake=%v)", len(shapes), nextWake)
	}
}

func TestAnnotationStore_LimitsAndValidation(t *testing.T) {
	store := NewAnnotationStore()
	now := time.Unix(1000, 0)

	// Payload acima do teto
	oversized := strings.Repeat("a", MaxAnnotationPayloadBytes+1)
	if _, err := store.Apply([]byte(oversized), now); err == nil {
		t.Fatalf("esperava erro para payload acima do limite")
	}

	// Tool inválida
	if _, err := store.Apply([]byte(`{"op":"add","tool":"polygon","points":[{"x":0,"y":0},{"x":1,"y":1}]}`), now); err == nil {
		t.Fatalf("esperava erro para tool inválida")
	}

	// Menos de 2 pontos
	if _, err := store.Apply([]byte(`{"op":"add","tool":"arrow","points":[{"x":0.5,"y":0.5}]}`), now); err == nil {
		t.Fatalf("esperava erro para < 2 pontos")
	}

	// Downsample > MaxAnnotationPoints
	pts := make([]AnnotationPoint, MaxAnnotationPoints+100)
	for i := range pts {
		pts[i] = AnnotationPoint{X: float64(i) / float64(len(pts)), Y: 0.5}
	}
	raw, _ := json.Marshal(AnnotationCommand{
		Op:     "add",
		ID:     "big",
		Tool:   "freehand",
		Points: pts,
	})
	if _, err := store.Apply(raw, now); err != nil {
		t.Fatalf("downsample falhou: %v", err)
	}
	shapes, _ := store.Snapshot(now)
	if len(shapes) != 1 || len(shapes[0].Points) != MaxAnnotationPoints {
		t.Fatalf("esperava pontos limitados a %d, obteve %d", MaxAnnotationPoints, len(shapes[0].Points))
	}

	// Limite de MaxActiveAnnotations descarta a mais antiga
	store.Clear()
	for i := 0; i < MaxActiveAnnotations+5; i++ {
		raw, _ := json.Marshal(AnnotationCommand{
			Op:     "add",
			ID:     string(rune('0' + (i % 10))),
			Tool:   "rect",
			Points: []AnnotationPoint{{X: 0.1, Y: 0.1}, {X: 0.2, Y: 0.2}},
		})
		if _, err := store.Apply(raw, now); err != nil {
			t.Fatalf("add #%d falhou: %v", i, err)
		}
	}
	shapes, _ = store.Snapshot(now)
	if len(shapes) != MaxActiveAnnotations {
		t.Fatalf("esperava teto de %d formas, obteve %d", MaxActiveAnnotations, len(shapes))
	}
}

func TestAnnotationStore_TTLFadeAndPrune(t *testing.T) {
	store := NewAnnotationStore()
	t0 := time.Unix(1000, 0)

	raw := []byte(`{"op":"add","id":"ttl1","tool":"arrow","ttlMs":2000,"points":[{"x":0.1,"y":0.1},{"x":0.5,"y":0.5}]}`)
	if _, err := store.Apply(raw, t0); err != nil {
		t.Fatalf("Apply falhou: %v", err)
	}

	// Antes da janela de fade (1000ms restantes, janela de 500ms => wake em ~500ms): alpha = 1
	shapes, nextWake := store.Snapshot(t0.Add(1000 * time.Millisecond))
	if len(shapes) != 1 || nextWake != 500*time.Millisecond || shapes[0].Alpha != 1.0 {
		t.Fatalf("esperava alpha=1.0 e nextWake=500ms, obteve nextWake=%v shapes=%+v", nextWake, shapes)
	}

	// Metade da janela de fade (250ms antes de expirar): alpha ≈ 0.5 e nextWake = 33ms
	shapes, nextWake = store.Snapshot(t0.Add(1750 * time.Millisecond))
	if len(shapes) != 1 || nextWake != 33*time.Millisecond || shapes[0].Alpha < 0.45 || shapes[0].Alpha > 0.55 {
		t.Fatalf("esperava alpha ~0.5 e nextWake=33ms no fade, obteve nextWake=%v alpha=%v", nextWake, shapes[0].Alpha)
	}

	// Após expiração: removido
	if !store.Prune(t0.Add(2000 * time.Millisecond)) {
		t.Fatalf("Prune deveria retornar true ao expirar")
	}
	shapes, nextWake = store.Snapshot(t0.Add(2000 * time.Millisecond))
	if len(shapes) != 0 || nextWake != 0 {
		t.Fatalf("esperava 0 shapes e nextWake=0 após expirar")
	}
}
