package remotesession

import (
	"testing"
	"time"
)

// fixedClock devolve um relógio controlável para os testes de carência.
func fixedClock(t0 time.Time) (*time.Time, func() time.Time) {
	cur := t0
	return &cur, func() time.Time { return cur }
}

// manualGate cria um gate em modo MANUAL com teto fixo — o cenário de um
// preset escolhido pelo operador. É o modo usado para testar a MECÂNICA do gate
// (fechar/abrir/histerese), porque nele o teto não se move sozinho.
func manualGate(ceiling float64, closeAfter int) *BandwidthGate {
	return NewBandwidthGate(BandwidthGateConfig{
		Mode:              BandwidthManual,
		CeilingKbps:       ceiling,
		CloseAfterWindows: closeAfter,
	})
}

func TestBandwidthGateDisabledByDefault(t *testing.T) {
	// Default do agent = modo HD, sem teto (comportamento anterior preservado).
	g := NewBandwidthGate(defaultBandwidthGateConfig())
	if g.Enabled() {
		t.Fatal("gate deveria estar desligado no default (modo hd)")
	}
	for i := 0; i < 10; i++ {
		g.Observe(999999, 30)
	}
	if g.Paused() {
		t.Fatal("gate desligado nunca deve pausar a captura")
	}
	if g.Mode() != BandwidthHD {
		t.Fatalf("modo default = %q, want hd", g.Mode())
	}
}

func TestBandwidthGateClosesAboveCeiling(t *testing.T) {
	g := manualGate(5000, 1)
	g.Observe(6000, 30)
	if !g.Paused() {
		t.Fatal("acima do teto com frames chegando deve fechar o gate")
	}
}

func TestBandwidthGateStaysOpenBelowCeiling(t *testing.T) {
	g := manualGate(5000, 1)
	for i := 0; i < 5; i++ {
		g.Observe(3000, 30)
	}
	if g.Paused() {
		t.Fatal("abaixo do teto não deve pausar")
	}
}

// Tela ociosa (recvFrames == 0) NÃO é congestionamento: bitrate baixo com a tela
// parada é o esperado. A janela deve ser ignorada.
func TestBandwidthGateIgnoresIdleScreen(t *testing.T) {
	g := manualGate(5000, 1)
	g.Observe(0, 0) // ocioso, bitrate zero
	if g.Paused() {
		t.Fatal("tela ociosa não deve fechar o gate")
	}
	// E um gate JÁ fechado não deve reabrir por causa de janela ociosa
	// (não há evidência de que o link voltou).
	g.Observe(9000, 30)
	if !g.Paused() {
		t.Fatal("pré-condição: deveria estar pausado")
	}
	for i := 0; i < 5; i++ {
		g.Observe(0, 0)
	}
	if !g.Paused() {
		t.Fatal("janela ociosa não deve reabrir um gate fechado")
	}
}

func TestBandwidthGateHysteresisReopensOnlyWellBelow(t *testing.T) {
	// Teto 10000, reabre abaixo de 70% = 7000.
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode:              BandwidthManual,
		CeilingKbps:       10000,
		ReopenFactor:      0.7,
		CloseAfterWindows: 1,
		OpenAfterWindows:  2,
	})

	g.Observe(11000, 30)
	if !g.Paused() {
		t.Fatal("deveria fechar acima do teto")
	}

	// 8000 está abaixo do teto mas ACIMA do limite de reabertura (7000):
	// fica na zona de histerese e NÃO reabre.
	for i := 0; i < 5; i++ {
		g.Observe(8000, 30)
	}
	if !g.Paused() {
		t.Fatal("zona de histerese (entre 7000 e 10000) não deve reabrir")
	}

	// 6000 < 7000: reabre após OpenAfterWindows janelas.
	g.Observe(6000, 30)
	if !g.Paused() {
		t.Fatal("uma única janela abaixo ainda não deve reabrir (reabre devagar)")
	}
	g.Observe(6000, 30)
	if g.Paused() {
		t.Fatal("duas janelas abaixo do limite devem reabrir")
	}
}

func TestBandwidthGateCloseAfterWindows(t *testing.T) {
	g := manualGate(5000, 3)
	g.Observe(6000, 30)
	if g.Paused() {
		t.Fatal("1/3 janelas acima não deve fechar ainda")
	}
	g.Observe(6000, 30)
	if g.Paused() {
		t.Fatal("2/3 janelas acima não deve fechar ainda")
	}
	g.Observe(6000, 30)
	if !g.Paused() {
		t.Fatal("3/3 janelas acima deve fechar")
	}
}

// Uma janela boa no meio zera a contagem de excesso (não é "acumulativo").
func TestBandwidthGateOverCounterResets(t *testing.T) {
	g := manualGate(5000, 2)
	g.Observe(6000, 30) // over=1
	g.Observe(1000, 30) // bom => over=0
	g.Observe(6000, 30) // over=1
	if g.Paused() {
		t.Fatal("contador de excesso deveria ter resetado com a janela boa")
	}
}

func TestBandwidthGateReopenHoldAntiFlap(t *testing.T) {
	nowPtr, clock := fixedClock(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode:              BandwidthManual,
		CeilingKbps:       10000,
		ReopenFactor:      0.7,
		CloseAfterWindows: 1,
		OpenAfterWindows:  1,
		ReopenHoldFor:     4 * time.Second,
	})
	g.SetClock(clock)

	g.Observe(11000, 30) // fecha
	if !g.Paused() {
		t.Fatal("deveria fechar")
	}

	g.Observe(1000, 30) // reabre (OpenAfterWindows=1), marca opened=now
	if g.Paused() {
		t.Fatal("deveria ter reaberto")
	}

	// Logo depois, o link estoura de novo: dentro da carência o gate NÃO pode
	// fechar de novo (evita flapping).
	*nowPtr = nowPtr.Add(1 * time.Second)
	g.Observe(20000, 30)
	if g.Paused() {
		t.Fatal("dentro da carência pós-reabertura não deve fechar de novo")
	}

	// Passada a carência, pode fechar normalmente.
	*nowPtr = nowPtr.Add(5 * time.Second)
	g.Observe(20000, 30)
	if !g.Paused() {
		t.Fatal("após a carência deve voltar a fechar")
	}
}

func TestBandwidthGateStats(t *testing.T) {
	nowPtr, clock := fixedClock(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthManual, CeilingKbps: 5000, ReopenFactor: 0.7,
		CloseAfterWindows: 1, OpenAfterWindows: 1,
		ReopenHoldFor: time.Millisecond,
	})
	g.SetClock(clock)

	g.Observe(6000, 30) // fecha
	g.Observe(1000, 30) // reabre
	*nowPtr = nowPtr.Add(time.Second)
	g.Observe(6000, 30) // fecha de novo

	closes, opens, last := g.Stats()
	if closes != 2 {
		t.Fatalf("closes = %d, want 2", closes)
	}
	if opens != 1 {
		t.Fatalf("opens = %d, want 1", opens)
	}
	if last != 6000 {
		t.Fatalf("lastKbps = %v, want 6000", last)
	}
}

func TestBandwidthGateSetCeilingRuntime(t *testing.T) {
	g := manualGate(5000, 1)
	g.Observe(9000, 30)
	if !g.Paused() {
		t.Fatal("deveria fechar")
	}
	// Desligar o teto reabre imediatamente.
	g.SetCeiling(0)
	if g.Paused() {
		t.Fatal("teto 0 deve reabrir o gate na hora")
	}
	if g.Enabled() {
		t.Fatal("Enabled() deve ser false com teto 0")
	}
	// Religar com teto mais alto: 9000 agora está abaixo do novo teto.
	g.SetCeiling(20000)
	g.Observe(9000, 30)
	if g.Paused() {
		t.Fatal("9000 está abaixo de 20000: não deve pausar")
	}
}

func TestBandwidthGateInvalidConfigGetsDefaults(t *testing.T) {
	// ReopenFactor fora de (0,1) e contadores <=0 devem cair nos defaults.
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode:              BandwidthManual,
		CeilingKbps:       10000,
		ReopenFactor:      1.5, // inválido
		CloseAfterWindows: 0,   // inválido
		OpenAfterWindows:  -3,  // inválido
	})
	if g.cfg.ReopenFactor != 0.7 {
		t.Fatalf("ReopenFactor = %v, want 0.7 (default)", g.cfg.ReopenFactor)
	}
	if g.cfg.CloseAfterWindows != 1 {
		t.Fatalf("CloseAfterWindows = %d, want 1", g.cfg.CloseAfterWindows)
	}
	if g.cfg.OpenAfterWindows != 2 {
		t.Fatalf("OpenAfterWindows = %d, want 2", g.cfg.OpenAfterWindows)
	}
	// E ainda funciona: fecha acima, histerese preservada.
	g.Observe(11000, 30)
	if !g.Paused() {
		t.Fatal("deveria fechar com defaults aplicados")
	}
}
