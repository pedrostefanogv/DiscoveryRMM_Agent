package remotesession

import (
	"testing"
	"time"
)

// ── Modos de teto de banda: auto / hd / manual (+ teto do servidor) ──
//
// Estes testes cobrem a política que a janela do visualizador alterna.

func TestBandwidthModeHDHasNoCeiling(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthHD, CeilingKbps: 99999})
	if g.CeilingKbps() != 0 {
		t.Fatalf("modo hd deveria zerar o teto, got %v", g.CeilingKbps())
	}
	if g.Enabled() {
		t.Fatal("modo hd não deve ter teto")
	}
	for i := 0; i < 20; i++ {
		g.Observe(999999, 30)
	}
	if g.Paused() {
		t.Fatal("modo hd nunca deve pausar a captura")
	}
}

func TestBandwidthModeManualUsesGivenCeiling(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthManual, CeilingKbps: 4000, CloseAfterWindows: 1})
	if g.CeilingKbps() != 4000 {
		t.Fatalf("teto manual = %v, want 4000", g.CeilingKbps())
	}
	g.Observe(5000, 30)
	if !g.Paused() {
		t.Fatal("manual acima do teto deve pausar")
	}
}

func TestBandwidthModeManualUnlimited(t *testing.T) {
	// 0 = "Sem limite" é um preset VÁLIDO do modo manual.
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthManual, CeilingKbps: 0})
	if g.Enabled() {
		t.Fatal("preset 'sem limite' não deve ter teto")
	}
	for i := 0; i < 10; i++ {
		g.Observe(999999, 30)
	}
	if g.Paused() {
		t.Fatal("sem limite nunca deve pausar")
	}
}

func TestBandwidthModeUnknownFallsBackToDefault(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthMode("banana"), CeilingKbps: 5000})
	if g.Mode() != BandwidthHD {
		t.Fatalf("modo desconhecido deveria cair no default (hd), got %q", g.Mode())
	}
}

// ── Modo AUTO: o teto se ajusta sozinho ──

func TestBandwidthAutoRaisesCeilingWhenViewerKeepsUp(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode:            BandwidthAuto,
		CeilingKbps:     8000,
		AutoMinKbps:     1000,
		AutoMaxKbps:     30000,
		AutoStepKbps:    2000,
		AutoRaiseFactor: 0.75,
		AutoLowerFactor: 0.6,
	})
	if g.CeilingKbps() != 8000 {
		t.Fatalf("teto inicial = %v, want 8000", g.CeilingKbps())
	}

	// Viewer recebe 7000 >= 8000*0.75 (=6000) → há folga → sobe um passo.
	g.Observe(7000, 30)
	if got := g.CeilingKbps(); got != 10000 {
		t.Fatalf("teto deveria subir para 10000, got %v", got)
	}

	// Continua subindo enquanto houver folga...
	g.Observe(9000, 30) // >= 10000*0.75 = 7500
	if got := g.CeilingKbps(); got != 12000 {
		t.Fatalf("teto deveria subir para 12000, got %v", got)
	}
}

func TestBandwidthAutoLowersCeilingOnCongestion(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode:            BandwidthAuto,
		CeilingKbps:     8000,
		AutoMinKbps:     1000,
		AutoMaxKbps:     30000,
		AutoStepKbps:    2000,
		AutoRaiseFactor: 0.75,
		AutoLowerFactor: 0.6,
	})

	// Viewer recebe 3000 < 8000*0.6 (=4800) → link aperta → desce um passo.
	g.Observe(3000, 30)
	if got := g.CeilingKbps(); got != 6000 {
		t.Fatalf("teto deveria descer para 6000, got %v", got)
	}
}

func TestBandwidthAutoDeadZoneKeepsCeiling(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode:            BandwidthAuto,
		CeilingKbps:     10000,
		AutoMinKbps:     1000,
		AutoMaxKbps:     30000,
		AutoStepKbps:    2000,
		AutoRaiseFactor: 0.75, // sobe em >= 7500
		AutoLowerFactor: 0.6,  // desce em < 6000
	})

	// 7000 está entre 6000 e 7500: ZONA MORTA → não mexe.
	g.Observe(7000, 30)
	if got := g.CeilingKbps(); got != 10000 {
		t.Fatalf("zona morta não deveria mexer no teto, got %v", got)
	}
}

func TestBandwidthAutoRespectsMinAndMax(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode:            BandwidthAuto,
		CeilingKbps:     2000,
		AutoMinKbps:     2000,
		AutoMaxKbps:     30000,
		AutoStepKbps:    2000,
		AutoRaiseFactor: 0.75,
		AutoLowerFactor: 0.6,
	})
	// Já está no piso: não desce abaixo de AutoMin mesmo sob aperto.
	g.Observe(100, 30)
	if got := g.CeilingKbps(); got != 2000 {
		t.Fatalf("teto não deveria descer abaixo do piso (2000), got %v", got)
	}

	// Sobe até o topo e para lá.
	g2 := NewBandwidthGate(BandwidthGateConfig{
		Mode:            BandwidthAuto,
		CeilingKbps:     30000,
		AutoMinKbps:     1000,
		AutoMaxKbps:     30000,
		AutoStepKbps:    2000,
		AutoRaiseFactor: 0.75,
		AutoLowerFactor: 0.6,
	})
	g2.Observe(29999, 30)
	if got := g2.CeilingKbps(); got != 30000 {
		t.Fatalf("teto não deveria passar do AutoMax (30000), got %v", got)
	}
}

// Tela ociosa não deve mover o teto automático: bitrate baixo com a tela parada
// não diz nada sobre a capacidade do link.
func TestBandwidthAutoIgnoresIdleScreen(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode:            BandwidthAuto,
		CeilingKbps:     8000,
		AutoMinKbps:     1000,
		AutoMaxKbps:     30000,
		AutoStepKbps:    2000,
		AutoRaiseFactor: 0.75,
		AutoLowerFactor: 0.6,
	})
	for i := 0; i < 10; i++ {
		g.Observe(0, 0) // ocioso
	}
	if got := g.CeilingKbps(); got != 8000 {
		t.Fatalf("tela ociosa não deveria mover o teto, got %v", got)
	}
}

// ── Teto ABSOLUTO do servidor: sobrepõe qualquer modo ──

func TestBandwidthServerMaxCapsHD(t *testing.T) {
	// Operador escolhe "sem limite" (HD), mas o servidor impõe 5000.
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthHD, ServerMaxKbps: 5000, CloseAfterWindows: 1})
	if got := g.CeilingKbps(); got != 5000 {
		t.Fatalf("ServerMax deveria impor teto no modo hd, got %v", got)
	}
	g.Observe(9000, 30)
	if !g.Paused() {
		t.Fatal("com ServerMax o modo hd PODE pausar")
	}
}

func TestBandwidthServerMaxCapsManualAbove(t *testing.T) {
	// Preset manual de 20 Mbps, mas o servidor limita a 8 Mbps.
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthManual, CeilingKbps: 20000, ServerMaxKbps: 8000,
	})
	if got := g.CeilingKbps(); got != 8000 {
		t.Fatalf("ServerMax deveria reduzir o preset manual, got %v", got)
	}
}

func TestBandwidthServerMaxDoesNotRaiseBelow(t *testing.T) {
	// ServerMax NÃO sobe um teto mais restritivo escolhido pelo operador.
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthManual, CeilingKbps: 2000, ServerMaxKbps: 8000,
	})
	if got := g.CeilingKbps(); got != 2000 {
		t.Fatalf("ServerMax não deve ELEVAR o teto, got %v", got)
	}
}

func TestBandwidthServerMaxCapsAutoGrowth(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode:            BandwidthAuto,
		CeilingKbps:     8000,
		AutoMinKbps:     1000,
		AutoMaxKbps:     30000, // agent permitiria até 30 Mbps
		AutoStepKbps:    2000,
		AutoRaiseFactor: 0.75,
		AutoLowerFactor: 0.6,
		ServerMaxKbps:   10000, // servidor limita a 10 Mbps
	})
	// Sobe enquanto houver folga, mas para no teto do SERVIDOR.
	for i := 0; i < 10; i++ {
		g.Observe(50000, 30)
	}
	if got := g.CeilingKbps(); got != 10000 {
		t.Fatalf("auto não deveria passar do ServerMax (10000), got %v", got)
	}
}

// ── Troca de modo em RUNTIME (o que a janela do visualizador faz) ──

func TestBandwidthSetModeRuntime(t *testing.T) {
	nowPtr, clock := fixedClock(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	_ = nowPtr
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthManual, CeilingKbps: 4000, CloseAfterWindows: 1})
	g.SetClock(clock)

	// manual 4 Mbps: fecha acima.
	g.Observe(9000, 30)
	if !g.Paused() {
		t.Fatal("pré-condição: manual deveria pausar")
	}

	// Operador muda para HD: SEM teto e o gate reabre na hora.
	g.SetMode(BandwidthHD, 0)
	if g.Mode() != BandwidthHD {
		t.Fatalf("modo = %q, want hd", g.Mode())
	}
	if g.Paused() {
		t.Fatal("trocar para hd deveria reabrir o gate")
	}
	if g.CeilingKbps() != 0 {
		t.Fatalf("hd deveria zerar o teto, got %v", g.CeilingKbps())
	}

	// Operador volta para manual com 10 Mbps.
	g.SetMode(BandwidthManual, 10000)
	if got := g.CeilingKbps(); got != 10000 {
		t.Fatalf("manual = %v, want 10000", got)
	}

	// Operador muda para AUTO: passa a ajustar sozinho.
	g.SetMode(BandwidthAuto, 0)
	if g.Mode() != BandwidthAuto {
		t.Fatalf("modo = %q, want auto", g.Mode())
	}
	g.Observe(50000, 30) // folga → sobe
	if got := g.CeilingKbps(); got <= 10000 {
		t.Fatalf("auto deveria ter subido o teto, got %v", got)
	}
}

func TestBandwidthSetModeRejectsUnknown(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthManual, CeilingKbps: 4000})
	g.SetMode(BandwidthMode("invalido"), 0)
	if g.Mode() != BandwidthManual {
		t.Fatalf("modo inválido não deveria trocar o modo, got %q", g.Mode())
	}
	if g.CeilingKbps() != 4000 {
		t.Fatalf("modo inválido não deveria mexer no teto, got %v", g.CeilingKbps())
	}
}

func TestBandwidthSetLimitsUpdatesServerCap(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthHD})
	if g.Enabled() {
		t.Fatal("pré-condição: hd sem teto")
	}
	// Servidor passa a impor um teto absoluto.
	g.SetLimits(0, 0, 0, 5000)
	if got := g.CeilingKbps(); got != 5000 {
		t.Fatalf("SetLimits deveria aplicar ServerMax, got %v", got)
	}
	if !g.Enabled() {
		t.Fatal("com ServerMax o gate deveria ficar ativo")
	}
}

func TestBandwidthSnapshotReportsState(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthManual, CeilingKbps: 4000, CloseAfterWindows: 1})
	g.Observe(9000, 30)

	s := g.Snapshot()
	if s.Mode != BandwidthManual {
		t.Fatalf("snapshot mode = %q, want manual", s.Mode)
	}
	if s.CeilingKbps != 4000 {
		t.Fatalf("snapshot ceiling = %v, want 4000", s.CeilingKbps)
	}
	if !s.Paused {
		t.Fatal("snapshot deveria indicar paused")
	}
	if s.Closes != 1 {
		t.Fatalf("snapshot closes = %d, want 1", s.Closes)
	}
	if s.LastKbps != 9000 {
		t.Fatalf("snapshot lastKbps = %v, want 9000", s.LastKbps)
	}
}
