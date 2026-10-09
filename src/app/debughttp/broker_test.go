package debughttp

import "testing"

// B4 (revisão 2026-10-08): os descartes do broker são separados POR CAUSA —
// sobrescrita do buffer circular de polling (WebView atrasado) e canal do
// inscrito SSE cheio (consumidor lento). Antes um único contador somava os dois
// e o diagnóstico não dizia qual lado estava atrasado.
func TestChatEventBroker_PollOverwriteIsCounted(t *testing.T) {
	b := NewChatEventBroker()
	for i := 0; i < b.pollMax+5; i++ {
		b.Publish("chat:token", "x")
	}

	subDropped, overwritten := b.DrainDrops()
	if subDropped != 0 {
		t.Fatalf("subscriberDropped = %d, quer 0 (não há inscritos)", subDropped)
	}
	if overwritten != 5 {
		t.Fatalf("pollOverwritten = %d, quer 5 (buffer de %d)", overwritten, b.pollMax)
	}

	// A leitura zera os contadores.
	if sub2, over2 := b.DrainDrops(); sub2 != 0 || over2 != 0 {
		t.Fatalf("segunda leitura deveria zerar: sub=%d overwritten=%d", sub2, over2)
	}
}

func TestChatEventBroker_SlowSubscriberIsCountedSeparately(t *testing.T) {
	b := NewChatEventBroker()
	_ = b.Subscribe() // canal de 128 — NÃO consumimos de propósito

	for i := 0; i < 200; i++ {
		b.Publish("chat:token", "x")
	}

	subDropped, overwritten := b.DrainDrops()
	if subDropped == 0 {
		t.Fatalf("subscriberDropped = 0, quer > 0 (canal de 128 com 200 eventos)")
	}
	if overwritten != 0 {
		t.Fatalf("pollOverwritten = %d, quer 0 (200 eventos cabem no buffer de %d)", overwritten, b.pollMax)
	}
}
