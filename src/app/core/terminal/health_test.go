//go:build windows

package terminal

import (
	"testing"
	"time"
)

func resetConptyHealthForTest() {
	conptyHealth.mu.Lock()
	conptyHealth.blockedUntil = time.Time{}
	conptyHealth.lastReason = ""
	conptyHealth.mu.Unlock()
}

func TestConptyHealthBlockedThenTTL(t *testing.T) {
	resetConptyHealthForTest()
	defer resetConptyHealthForTest()

	MarkConptyBlocked("0xC0000142")
	if !ConptyBlocked() {
		t.Fatal("esperava ConptyBlocked=true após MarkConptyBlocked")
	}
	if got := ConptyBlockedUntil(); got.Before(time.Now().Add(ConptyBlockedTTL - time.Minute)) {
		t.Fatalf("blockedUntil muito no passado: %s", got)
	}

	// Força expiração sem esperar 5 min.
	SetConptyBlockedUntil(time.Now().Add(-time.Second))
	if ConptyBlocked() {
		t.Fatal("esperava ConptyBlocked=false após expirar o hint")
	}
}

func TestConptyHealthHealthyClearsHint(t *testing.T) {
	resetConptyHealthForTest()
	defer resetConptyHealthForTest()

	SetConptyBlockedUntil(time.Now().Add(time.Minute))
	if !ConptyBlocked() {
		t.Fatal("esperava hint ativo")
	}
	MarkConptyHealthy()
	if ConptyBlocked() {
		t.Fatal("MarkConptyHealthy deveria limpar o hint")
	}
	if !ConptyBlockedUntil().IsZero() {
		t.Fatalf("blockedUntil deveria ser zero, veio %s", ConptyBlockedUntil())
	}
}

// TestConptyBlockedHintIsProcessWide garante que SetConptyBlockedUntil (usado
// pelo worker com o hint do serviço) é consultável por ConptyBlocked.
func TestConptyBlockedHintIsProcessWide(t *testing.T) {
	resetConptyHealthForTest()
	defer resetConptyHealthForTest()

	until := time.Now().Add(2 * time.Minute)
	SetConptyBlockedUntil(until)
	if !ConptyBlocked() {
		t.Fatal("hint setado deveria bloquear")
	}
	if diff := ConptyBlockedUntil().Sub(until); diff > time.Millisecond || diff < -time.Millisecond {
		t.Fatalf("blockedUntil divergente: %s vs %s", ConptyBlockedUntil(), until)
	}
}
