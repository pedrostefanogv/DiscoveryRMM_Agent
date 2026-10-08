package ai

import "testing"

// Mesmo componente, formatado de duas maneiras (e um deles com markdown cru):
// depois de normalizado/canonicalizado deve contar como duplicado.
const guardTextA = `{"version":"v0.9","updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Column","children":["x"]},{"id":"x","component":"Text","text":"**A**"}]}}`
const guardTextAClean = `{"version":"v0.9","updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Column","children":["x"]},{"id":"x","component":"Text","text":"A"}]}}`
const guardTextAReordered = `{"updateComponents":{"components":[{"component":"Column","id":"root","children":["x"]},{"component":"Text","id":"x","text":"A"}],"surfaceId":"s"},"version":"v0.9"}`
const guardTextB = `{"version":"v0.9","updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Column","children":["y"]},{"id":"y","component":"Text","text":"B"}]}}`

func TestA2uiClientGuard_DedupAfterNormalization(t *testing.T) {
	g := newA2uiClientGuard(maxA2uiMessagesPerTurn)
	if _, r := g.Offer(guardTextA); r != a2uiAccept {
		t.Fatalf("1a mensagem deveria ser aceita, got %v", r)
	}
	// Difere só no markdown cru: normalizado vira o mesmo card.
	if _, r := g.Offer(guardTextAClean); r != a2uiDropDuplicate {
		t.Fatalf("copia com markdown deveria ser duplicada, got %v", r)
	}
	// Difere só na ordem das chaves: forma canônica é igual.
	if _, r := g.Offer(guardTextAReordered); r != a2uiDropDuplicate {
		t.Fatalf("copia com chaves reordenadas deveria ser duplicada, got %v", r)
	}
}

func TestA2uiClientGuard_DropsEmptyAndNull(t *testing.T) {
	g := newA2uiClientGuard(maxA2uiMessagesPerTurn)
	for _, in := range []string{"", "   ", "null", " null "} {
		if _, r := g.Offer(in); r != a2uiDropEmpty {
			t.Fatalf("Offer(%q) = %v, quer a2uiDropEmpty", in, r)
		}
	}
}

func TestA2uiClientGuard_EnforcesLimit(t *testing.T) {
	g := newA2uiClientGuard(1)
	if _, r := g.Offer(guardTextA); r != a2uiAccept {
		t.Fatalf("1a deveria ser aceita, got %v", r)
	}
	if _, r := g.Offer(guardTextB); r != a2uiDropLimit {
		t.Fatalf("2a deveria estourar o teto, got %v", r)
	}
}

func TestNewA2uiClientGuard_DefaultLimit(t *testing.T) {
	if g := newA2uiClientGuard(0); g.max != maxA2uiMessagesPerTurn {
		t.Fatalf("max = %d, quer %d", g.max, maxA2uiMessagesPerTurn)
	}
}
