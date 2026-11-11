package remotesession

import (
	"fmt"
	"testing"
)

// TestBandwidthFixNoRegressionOnExplicitCeiling garante que a correção do
// AutoStartKbps NÃO alterou nenhum caminho que JÁ enviava um teto explícito:
// em todos eles o teto corrente continua sendo o valor enviado.
func TestBandwidthFixNoRegressionOnExplicitCeiling(t *testing.T) {
	cases := []struct {
		name string
		cfg  BandwidthGateConfig
		want float64
		mode BandwidthMode
	}{
		{"manual 4000", BandwidthGateConfig{Mode: BandwidthManual, CeilingKbps: 4000}, 4000, BandwidthManual},
		{"manual 20000", BandwidthGateConfig{Mode: BandwidthManual, CeilingKbps: 20000}, 20000, BandwidthManual},
		{"manual 0 (sem limite)", BandwidthGateConfig{Mode: BandwidthManual, CeilingKbps: 0}, 0, BandwidthManual},
		{"hd 99999 (ignora)", BandwidthGateConfig{Mode: BandwidthHD, CeilingKbps: 99999}, 0, BandwidthHD},
		{"auto 8000", BandwidthGateConfig{Mode: BandwidthAuto, CeilingKbps: 8000}, 8000, BandwidthAuto},
		{"auto 3000", BandwidthGateConfig{Mode: BandwidthAuto, CeilingKbps: 3000}, 3000, BandwidthAuto},
	}
	for _, c := range cases {
		g := NewBandwidthGate(c.cfg)
		if got := g.CeilingKbps(); got != c.want {
			t.Fatalf("%s: teto = %v, want %v", c.name, got, c.want)
		}
		if g.Mode() != c.mode {
			t.Fatalf("%s: modo = %v, want %v", c.name, g.Mode(), c.mode)
		}
		fmt.Printf("  ok %-24s teto=%8.0f modo=%s\n", c.name, g.CeilingKbps(), g.Mode())
	}

	// SetMode com teto explícito continua usando o valor enviado.
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthHD})
	for _, tc := range []struct {
		mode    BandwidthMode
		ceiling float64
		want    float64
	}{
		{BandwidthManual, 10000, 10000},
		{BandwidthManual, 0, 0},
		{BandwidthAuto, 5000, 5000},
		// Sem teto no comando, mas com teto corrente VÁLIDO (5000, vindo do
		// SetMode anterior): PRESERVA — o AutoStart só entra quando não há
		// teto corrente algum.
		{BandwidthAuto, 0, 5000},
		{BandwidthHD, 0, 0},
		{BandwidthAuto, 0, 8000}, // agora o corrente é 0 -> AutoStart
	} {
		g.SetMode(tc.mode, tc.ceiling)
		if got := g.CeilingKbps(); got != tc.want {
			t.Fatalf("SetMode(%s, %.0f) = %.0f, want %.0f", tc.mode, tc.ceiling, got, tc.want)
		}
		fmt.Printf("  ok SetMode(%-6s,%6.0f) => teto=%8.0f\n", tc.mode, tc.ceiling, g.CeilingKbps())
	}
}

// TestBandwidthFixAutoStartMovesFromStart confirma que o teto inicial é só o
// PONTO DE PARTIDA: o auto-ajuste continua movendo-o normalmente a partir dali.
func TestBandwidthFixAutoStartMovesFromStart(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthAuto, CeilingKbps: 0,
		AutoMinKbps: 1000, AutoMaxKbps: 30000, AutoStepKbps: 2000,
		AutoRaiseFactor: 0.75, AutoLowerFactor: 0.6,
	})
	if got := g.CeilingKbps(); got != 8000 {
		t.Fatalf("partida esperada 8000, got %v", got)
	}
	// Folga -> sobe um passo (6000 <= 7000 < ...): 7000 >= 8000*0.75=6000 => sobe.
	g.Observe(7000, 30)
	if got := g.CeilingKbps(); got != 10000 {
		t.Fatalf("deveria subir para 10000, got %v", got)
	}
	// Aperto -> desce.
	g2 := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthAuto, CeilingKbps: 0,
		AutoMinKbps: 1000, AutoMaxKbps: 30000, AutoStepKbps: 2000,
		AutoRaiseFactor: 0.75, AutoLowerFactor: 0.6,
	})
	g2.Observe(1000, 30) // 1000 < 8000*0.6=4800 => desce
	if got := g2.CeilingKbps(); got != 6000 {
		t.Fatalf("deveria descer para 6000, got %v", got)
	}
	fmt.Println("[fix] AutoStart=8000 é ponto de partida: sobe com folga, desce com aperto")
}

// TestBandwidthFixSetLimitsAutoStartConsistency garante que SetLimits usa o
// MESMO critério do SetMode quando o modo auto não tem teto corrente: parte do
// teto inicial do auto (8000), não do piso (1000). Sem isso, um comando que só
// atualiza limites (serverMax, autoMin...) reiniciava o auto no piso.
func TestBandwidthFixSetLimitsAutoStartConsistency(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthHD})
	if g.CeilingKbps() != 0 {
		t.Fatalf("pré-condição: hd sem teto, got %v", g.CeilingKbps())
	}
	// Torna-se auto SEM teto corrente e depois recebe só limites de range.
	g.SetMode(BandwidthAuto, 0)
	g.SetCeiling(0) // volta a "sem teto" mantendo o modo auto
	if g.CeilingKbps() != 0 {
		t.Fatalf("pré-condição: SetCeiling(0) deveria zerar, got %v", g.CeilingKbps())
	}
	g.SetLimits(1000, 30000, 2000, 0) // todos dentro do range, sem teto próprio
	if got := g.CeilingKbps(); got != 8000 {
		t.Fatalf("SetLimits no modo auto sem teto corrente deveria partir de AutoStart=8000, got %v", got)
	}
	fmt.Println("[fix] SetLimits no auto sem teto corrente => AutoStart=8000 (consistente com SetMode)")

	// E o ServerMax continua sobrepondo essa partida.
	g2 := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthAuto})
	g2.SetCeiling(0)
	g2.SetLimits(0, 0, 0, 4000)
	if got := g2.CeilingKbps(); got != 4000 {
		t.Fatalf("ServerMax 4000 deveria limitar o AutoStart, got %v", got)
	}
}

// TestBandwidthFixServerMaxStillWins garante que o teto do servidor continua
// sobrepondo o AutoStart na partida.
func TestBandwidthFixServerMaxStillWins(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthAuto, CeilingKbps: 0, ServerMaxKbps: 3000,
	})
	if got := g.CeilingKbps(); got != 3000 {
		t.Fatalf("ServerMax 3000 deveria limitar o AutoStart 8000, got %v", got)
	}
	g2 := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthHD, ServerMaxKbps: 3000})
	g2.SetMode(BandwidthAuto, 0)
	if got := g2.CeilingKbps(); got != 3000 {
		t.Fatalf("SetMode(auto,0) com ServerMax 3000 = %v, want 3000", got)
	}
	fmt.Println("[fix] ServerMax continua sobrepondo o AutoStart")
}
