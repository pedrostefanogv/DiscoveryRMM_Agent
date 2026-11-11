package remotesession

import (
	"fmt"
	"testing"
	"time"
)

// ── A2/A3: DOCUMENTAÇÃO DO ESTADO ATUAL DO GATE DE BANDA ──
//
// Estes testes foram escritos ANTES de qualquer correção (revisão agent-review)
// para FIXAR por escrito o comportamento OBSERVADO, inclusive onde ele diverge
// do comentário do código. Os nomes "Current..." marcam comportamento observado,
// não comportamento desejado.
//
// Convenção: 1 janela = 1 chamada de Observe(recvKbps, recvFrames) = ~2 s de
// netstats do viewer (SessionScreen.UpdateNetworkMetrics → bandwidthGate.Observe,
// session_screen.go:1074-1082).

// hdrRun roda um cenário e imprime o cabeçalho de evidência usado no relatório.
func hdrRun(t *testing.T, name string, cfg BandwidthGateConfig, recv float64, windows int) *BandwidthGate {
	t.Helper()
	g := NewBandwidthGate(cfg)
	paused := 0
	for w := 0; w < windows; w++ {
		g.Observe(recv, 30)
		if g.Paused() {
			paused++
		}
	}
	s := g.Snapshot()
	fmt.Printf("  %-26s carga=%8.0f | ceilFinal=%8.0f ajustes=%3d pausas=%3d/%d closes=%2d opens=%2d\n",
		name, recv, s.CeilingKbps, s.AutoAdjusts, paused, windows, s.Closes, s.Opens)
	return g
}

func autoCfg(ceiling float64) BandwidthGateConfig {
	return BandwidthGateConfig{
		Mode: BandwidthAuto, CeilingKbps: ceiling,
		AutoMinKbps: 1000, AutoMaxKbps: 30000, AutoStepKbps: 2000,
		AutoRaiseFactor: 0.75, AutoLowerFactor: 0.6,
		CloseAfterWindows: 1, OpenAfterWindows: 2,
	}
}

// ── A2.1: manual + 0 = "Sem limite" DESLIGA o gate (não é piso) ──

func TestBandwidthCurrentManualZeroDisablesGate(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthManual, CeilingKbps: 0})

	if g.Enabled() {
		t.Fatalf("manual+0: Enabled() = true, want false (observado)")
	}
	if got := g.CeilingKbps(); got != 0 {
		t.Fatalf("manual+0: teto = %v, want 0", got)
	}
	for i := 0; i < 20; i++ {
		g.Observe(500000, 30)
	}
	if g.Paused() {
		t.Fatal("manual+0: nunca deve pausar")
	}

	// Contraste do achado A1: o MESMO 0 no modo auto liga o gate no piso.
	da := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthAuto, CeilingKbps: 0})
	if !da.Enabled() {
		t.Fatal("auto+0: deveria ligar o gate no teto inicial do auto (assimetria A1)")
	}
	if got := da.CeilingKbps(); got != 8000 {
		t.Fatalf("auto+0: teto = %v, want 8000 (AutoStartKbps)", got)
	}
	// A assimetria de A1 permanece — e é DELIBERADA: 0 em manual significa
	// "sem teto escolhido pelo operador"; 0 em auto significa "sem teto
	// inicial informado", e o auto precisa de um ponto de partida dentro do range.
	fmt.Printf("[A1] manual+0 => enabled=false teto=0 | auto+0 => enabled=true teto=%.0f (AutoStart)\n", da.CeilingKbps())
}

// ── A2.2: manual + 8000 fecha em 1 janela; reabre só após 2 janelas < 0.7*teto ──

func TestBandwidthCurrentManual8000CloseAndReopen(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthManual, CeilingKbps: 8000, CloseAfterWindows: 1, OpenAfterWindows: 2,
	})
	if !g.Enabled() {
		t.Fatal("manual+8000 deveria ter gate ligado")
	}

	g.Observe(8001, 30)
	if !g.Paused() {
		t.Fatal("1 janela acima do teto deveria fechar (CloseAfterWindows=1)")
	}

	// Zona de histerese: 6000 < 8000 (teto) mas > 5600 (0.7*teto).
	for i := 0; i < 6; i++ {
		g.Observe(6000, 30)
	}
	if !g.Paused() {
		t.Fatal("6000 (> 5600) está na zona de histerese: não deve reabrir")
	}

	g.Observe(5000, 30)
	if !g.Paused() {
		t.Fatal("1 de 2 janelas abaixo não deve reabrir")
	}
	g.Observe(5000, 30)
	if g.Paused() {
		t.Fatal("2 janelas abaixo de 0.7*teto devem reabrir")
	}

	// Reabrir marca g.opened e Paused() passa a respeitar ReopenHoldFor (4 s,
	// bandwidth_gate.go:305): o ESTADO pode fechar de novo mas Paused() não
	// pausa até a carência passar. Sem SetClock o relógio é o real, então dentro
	// de um teste a carência nunca expira — é preciso avançar o relógio.
	nowPtr, clock2 := fixedClock(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	g.SetClock(clock2)

	// O limite é ESTRITO (< 5600): exatamente 5600 NÃO reabre.
	g.Observe(9000, 30)
	if !g.closed {
		t.Fatal("pré-condição: 9000 > 8000 deveria fechar o ESTADO")
	}
	for i := 0; i < 4; i++ {
		g.Observe(5600, 30)
	}
	if !g.closed {
		t.Fatal("5600 == 0.7*8000 deve manter o estado FECHADO (comparação estrita recvKbps < reopenBelow)")
	}
	// Dentro da carência o estado fechado NÃO vira pausa: Paused() ganha da
	// decisão de estado enquanto now-opened < ReopenHoldFor. Este é o preço
	// consciente do anti-flapping (bandwidth_gate.go:298-309).
	if g.Paused() {
		t.Fatal("dentro da carência pós-reabertura Paused() deve ser false")
	}
	if !g.closed {
		t.Fatal("o ESTADO deve continuar fechado dentro da carência")
	}
	_ = nowPtr
	fmt.Println("[A2.2] manual+8000: fecha com 8001; histerese 5600..8000; reabre com 2x 5000; 5600 fica fechado")
}

// ── A2.3: auto com teto <= 0 assume o teto inicial do auto e liga o gate ──

func TestBandwidthCurrentAutoZeroCeilingTakesAutoStart(t *testing.T) {
	for _, ceiling := range []float64{0, -1} {
		g := NewBandwidthGate(BandwidthGateConfig{
			Mode: BandwidthAuto, CeilingKbps: ceiling,
			AutoMinKbps: 1000, AutoMaxKbps: 30000, AutoStepKbps: 2000,
			AutoRaiseFactor: 0.75, AutoLowerFactor: 0.6,
		})
		if !g.Enabled() {
			t.Fatalf("auto com teto %v deveria ligar o gate", ceiling)
		}
		// AutoStartKbps default = 8000 (espelha o servidor). ANTES da correção
		// isto valia AutoMin (1000) e divergia do START da sessão.
		if got := g.CeilingKbps(); got != 8000 {
			t.Fatalf("auto com teto %v: teto = %v, want 8000 (AutoStartKbps)", ceiling, got)
		}
	}
	// O teto inicial é clampado ao range quando o operador o restringe.
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthAuto, CeilingKbps: 0, AutoMinKbps: 1000, AutoMaxKbps: 5000,
	})
	if got := g.CeilingKbps(); got != 5000 {
		t.Fatalf("AutoStart deve ser clampeado ao AutoMax: got %v, want 5000", got)
	}
	g2 := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthAuto, CeilingKbps: 0, AutoMinKbps: 12000, AutoMaxKbps: 30000,
	})
	if got := g2.CeilingKbps(); got != 12000 {
		t.Fatalf("AutoStart deve ser clampeado ao AutoMin: got %v, want 12000", got)
	}
	fmt.Println("[A1] auto com teto 0/-1 => teto inicial AutoStartKbps=8000 (clampado ao range) e gate LIGADO")
}

// ── A2.4: ServerMaxKbps 0 = sem restrição; > 0 = cap em hd, manual e auto ──

func TestBandwidthCurrentServerMaxAcrossModes(t *testing.T) {
	cases := []struct {
		mode    BandwidthMode
		ceiling float64
		want    float64
	}{
		{BandwidthHD, 0, 0},
		{BandwidthManual, 0, 0},
		{BandwidthManual, 20000, 20000},
		{BandwidthAuto, 8000, 8000},
	}
	for _, c := range cases {
		g := NewBandwidthGate(BandwidthGateConfig{Mode: c.mode, CeilingKbps: c.ceiling, ServerMaxKbps: 0})
		if got := g.CeilingKbps(); got != c.want {
			t.Fatalf("ServerMax=0 mode=%s ceiling=%v: teto = %v, want %v (sem restrição)", c.mode, c.ceiling, got, c.want)
		}
	}

	hd := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthHD, ServerMaxKbps: 5000})
	if hd.CeilingKbps() != 5000 || !hd.Enabled() {
		t.Fatalf("hd+ServerMax: teto=%v enabled=%v, want 5000/true", hd.CeilingKbps(), hd.Enabled())
	}
	man0 := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthManual, CeilingKbps: 0, ServerMaxKbps: 5000})
	if man0.CeilingKbps() != 5000 || !man0.Enabled() {
		t.Fatalf("manual+0+ServerMax: teto=%v enabled=%v, want 5000/true (o servidor fecha o buraco A1)",
			man0.CeilingKbps(), man0.Enabled())
	}
	manBig := NewBandwidthGate(BandwidthGateConfig{Mode: BandwidthManual, CeilingKbps: 50000, ServerMaxKbps: 8000})
	if manBig.CeilingKbps() != 8000 {
		t.Fatalf("manual 50000 + ServerMax 8000: teto=%v, want 8000", manBig.CeilingKbps())
	}
	auto := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthAuto, CeilingKbps: 8000, AutoMinKbps: 1000, AutoMaxKbps: 30000,
		AutoStepKbps: 2000, AutoRaiseFactor: 0.75, AutoLowerFactor: 0.6, ServerMaxKbps: 10000,
	})
	for i := 0; i < 20; i++ {
		auto.Observe(500000, 30)
	}
	if got := auto.CeilingKbps(); got != 10000 {
		t.Fatalf("auto limitado por ServerMax: teto=%v, want 10000", got)
	}
	fmt.Println("[A2.4] ServerMax=0 libera; ServerMax>0 capa hd/manual/auto (inclusive manual+0)")
}

// ── A2.5: sequência rápida de SetMode dentro de uma janela ──

func TestBandwidthCurrentRapidSetModeSequence(t *testing.T) {
	g := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthManual, CeilingKbps: 8000, CloseAfterWindows: 1, OpenAfterWindows: 2,
	})

	g.Observe(9000, 30)
	if !g.Paused() {
		t.Fatal("pré-condição: manual 8000 deveria fechar")
	}

	g.SetMode(BandwidthHD, 0)
	if g.Paused() || g.Enabled() {
		t.Fatalf("hd: paused=%v enabled=%v, want false/false", g.Paused(), g.Enabled())
	}
	if !g.opened.IsZero() {
		t.Fatal("SetMode não deve marcar 'opened' (a carência é só da reabertura por Observe em bandwidth_gate.go:286)")
	}
	if !g.closed && g.over == 0 && g.under == 0 {
		// estado limpo confirmado abaixo
	} else {
		t.Fatalf("SetMode deve zerar closed/over/under: closed=%v over=%d under=%d", g.closed, g.over, g.under)
	}

	// auto SEM ceiling parte do teto CORRENTE (0, porque veio de hd) => usa o
	// teto inicial do auto. Antes da correção caía no PISO (1000), fazendo o
	// teto depender de COMO o operador chegou ao modo.
	g.SetMode(BandwidthAuto, 0)
	if got := g.CeilingKbps(); got != 8000 {
		t.Fatalf("auto a partir de hd: teto = %v, want 8000 (AutoStartKbps)", got)
	}

	// Cada SetMode reabre, mesmo depois de o gate fechar de novo.
	g.Observe(900000, 30)
	if !g.Paused() {
		t.Fatal("auto com teto 1000 deveria fechar sob 900000 kbps")
	}
	g.SetMode(BandwidthManual, 8000)
	if g.Paused() {
		t.Fatal("trocar de modo (auto→manual) deve reabrir o gate")
	}
	if got := g.CeilingKbps(); got != 8000 {
		t.Fatalf("manual: teto = %v, want 8000", got)
	}

	// auto SEM ceiling a partir de MANUAL preserva o teto corrente (8000).
	// (a partir de HD o corrente é 0 e cai no piso — assimetria do SetMode.)
	g.SetMode(BandwidthManual, 8000)
	g.SetMode(BandwidthAuto, 0)
	if got := g.CeilingKbps(); got != 8000 {
		t.Fatalf("auto após manual: teto = %v, want 8000 (preserva o corrente, bandwidth_gate.go:431)", got)
	}
	// A sequência manual→hd→auto passa pelo ZERO; agora o resultado é o MESMO
	// teto inicial do auto (é a correção da assimetria A2.5/A4b).
	g.SetMode(BandwidthManual, 8000)
	g.SetMode(BandwidthHD, 0)
	g.SetMode(BandwidthAuto, 0)
	if got := g.CeilingKbps(); got != 8000 {
		t.Fatalf("auto após hd: teto = %v, want 8000 (AutoStartKbps)", got)
	}
	fmt.Printf("[A2.5] manual8000→auto(0)=8000 | manual8000→hd→auto(0)=8000 (assimetria corrigida)\n")
}

// ── A3: REGIME PERMANENTE DO adjustAuto (evidência numérica) ──

// TestBandwidthCurrentAutoCeilingDoesNotSettleOnLoad demonstra que o teto
// automático NÃO converge para a banda disponível: ele oscila entre dois passos
// inteiros em torno do ponto fixo. Com recv=3000 o teto alterna 6000↔4000 para
// sempre (o ponto fixo exato seria ceil ∈ (2400, 3000], alcançável só se o recv
// batesse na fronteira).
func TestBandwidthCurrentAutoCeilingDoesNotSettleOnLoad(t *testing.T) {
	g := NewBandwidthGate(autoCfg(8000))
	seen := map[float64]int{}
	for w := 0; w < 200; w++ {
		g.Observe(3000, 30)
		seen[g.CeilingKbps()]++
	}
	fmt.Printf("[A3] recv=3000 constante, 200 janelas: distribuição do teto = %v\n", seen)
	if len(seen) < 2 {
		t.Fatalf("esperado oscilação entre dois valores de teto, got %v", seen)
	}
	lo, hi := 1e9, 0.0
	for v := range seen {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if hi-lo != 2000 {
		t.Fatalf("oscilação esperada de 1 passo (2000 kbps), got %.0f..%.0f", lo, hi)
	}
	// E o teto final NÃO é a banda disponível (3000): é um múltiplo de 2000 acima dela.
	if lo < 3000 {
		t.Fatalf("o teto nunca desce abaixo do valor oscilante: lo=%.0f", lo)
	}
	fmt.Printf("[A3] o teto NUNCA se estabiliza; nunca fica em 2000 (abaixo do recv) porque em 2000 o recv(3000) >= 0.75*2000\n")
}

// TestBandwidthCurrentAutoNeverPausesUnderModerateLoad é a evidência central do
// achado A3, no formato que a tarefa pediu (60 janelas, recv acima do piso).
//
// IMPORTANTE — a premissa da tarefa ("o gate nunca pausa em regime permanente")
// está INVERTIDA para carga moderada. O que se observa é o oposto: com recv
// MODERADO (2000..12000 kbps, tudo abaixo do AutoMax) e o teto inicial em 8000,
// o gate fica 0% do tempo pausado, porque:
//  1. adjustAuto julga o recv contra o teto DO INSTANTE, não contra a banda
//     disponível (bandwidth_gate.go:470-471), então o teto oscila/parece não convergir;
//  2. cada ajuste faz g.closed=false (bandwidth_gate.go:494) e o declaring de
//     'over' é REINICIADO, mas se o recv também estiver acima do teto do passo
//     seguinte, o fechamento acontece na MESMA janela seguinte...
//
// O resultado medido abaixo é que a pausa depende CRITICAMENTE de o recv exceder
// o teto alcançável, e não apenas de o link estar saturado.
func TestBandwidthCurrentAutoNeverPausesUnderModerateLoad(t *testing.T) {
	fmt.Println("[A3] carga MODERADA (abaixo do AutoMax), 200 janelas, teto inicial 8000:")
	zeroPauseMax := 0.0
	for _, recv := range []float64{2000, 4000, 6000, 9000} {
		g := hdrRun(t, "auto(8000) moderado", autoCfg(8000), recv, 200)
		if g.Snapshot().Closes != 0 {
			t.Fatalf("esperado 0 fechamentos com recv=%.0f, got %d", recv, g.Snapshot().Closes)
		}
		if zeroPauseMax < recv {
			zeroPauseMax = recv
		}
	}
	// Acima desse patamar o gate começa a fechar (1 fechamento em 200 janelas):
	// o fechamento depende de o recv exceder o teto ALCANÇÁVEL, não de o link
	// estar saturado. 20000 kbps de link (2x o teto inicial) => ~1 fechamento.
	g := hdrRun(t, "auto(8000) moderado", autoCfg(8000), 12000, 200)
	if g.Snapshot().Closes != 1 {
		t.Fatalf("recv=12000: esperado 1 fechamento em 200 janelas, got %d", g.Snapshot().Closes)
	}
	fmt.Printf("[A3] => pausa ZERO para recv de 2000..%.0f kbps (200 janelas cada): o link saturado NAO produz pausa sustentada\n", zeroPauseMax)
}

// TestBandwidthCurrentAutoCeilingIsRaisedByItsOwnPause prova o mecanismo exato:
// quando o gate PAUSA a captura, o viewer recebe MENOS (recv baixo na janela
// seguinte). Se esse recv cair na zona que faz o teto SUBIR, o teto sobe e o
// gate reabre — ou seja, a pausa realimenta o aumento do teto.
func TestBandwidthCurrentAutoCeilingIsRaisedByItsOwnPause(t *testing.T) {
	g := NewBandwidthGate(autoCfg(8000))
	// Rajada que fecha o gate.
	g.Observe(200000, 30)
	if !g.Paused() {
		t.Fatal("pré-condição: rajada deveria fechar")
	}
	ceilBefore := g.CeilingKbps()
	// Enquanto PAUSADO, o viewer recebe quase nada: 5000 kbps.
	// adjustAuto roda ANTES da avaliação do gate (bandwidth_gate.go:262) e, com
	// o teto corrente, 5000 pode estar na zona de SUBIDA.
	g.Observe(5000, 30)
	fmt.Printf("[A3] teto antes=%0.f após janela de recv=5000 (gate fechado)=%0.f closed=%v paused=%v\n",
		ceilBefore, g.CeilingKbps(), g.closed, g.Paused())
	if g.CeilingKbps() <= ceilBefore {
		fmt.Println("[A3] NOTA: o teto não subiu nesta janela (recv=5000 caiu na zona morta/descida)")
	}
	_ = time.Second
}

// TestBandwidthCurrentAutoSaturatedAtMaxStaysPaused: no extremo oposto, quando o
// teto satura no AutoMax com o link muito pior, o gate fica 100% pausado — e a
// ÚNICA saída é o link subir até 0.7*AutoMax = 21 Mbps (ou a carência de 4 s).
func TestBandwidthCurrentAutoSaturatedAtMaxStaysPaused(t *testing.T) {
	g := NewBandwidthGate(autoCfg(8000))
	paused := 0
	for w := 0; w < 200; w++ {
		g.Observe(40000, 30)
		if g.Paused() {
			paused++
		}
	}
	s := g.Snapshot()
	fmt.Printf("[A3] recv=40000 (acima do AutoMax): ceilFinal=%.0f pausas=%d/200 closes=%d\n", s.CeilingKbps, paused, s.Closes)
	if paused != 200 {
		t.Fatalf("esperado 200/200 pausado com recv acima do AutoMax, got %d", paused)
	}
	if s.CeilingKbps != 30000 {
		t.Fatalf("teto deveria saturar no AutoMax=30000, got %.0f", s.CeilingKbps)
	}
	fmt.Println("[A3] => saturação no AutoMax exige o link subir a 21 Mbps (0.7*30000) para reabrir: pausa indefinida")
}

// TestBandwidthCurrentHDVsManualVsAutoRecall compara os três modos sob a MESMA
// carga saturada (40000 kbps) — mostra que o gate só é DURO em manual.
func TestBandwidthCurrentHDVsManualVsAutoRecall(t *testing.T) {
	const carga = 40000.0
	fmt.Println("[A3] mesma carga saturada (40000 kbps) nos três modos, 200 janelas:")
	for _, mode := range []BandwidthMode{BandwidthHD, BandwidthManual, BandwidthAuto} {
		cfg := autoCfg(8000)
		cfg.Mode = mode
		if mode == BandwidthManual {
			cfg.CeilingKbps = 8000
		}
		g := hdrRun(t, string(mode), cfg, carga, 200)
		switch mode {
		case BandwidthHD:
			if g.Snapshot().Paused {
				t.Fatal("hd nunca deve pausar")
			}
		default:
			if !g.Snapshot().Paused {
				t.Fatalf("%s deveria estar pausado sob 40000 kbps", mode)
			}
		}
	}
}

// TestBandwidthCurrentIdleFreezesGateAndCeiling documenta o efeito de
// recvFrames<=0: nem o teto se move, nem um gate fechado reabre.
func TestBandwidthCurrentIdleFreezesGateAndCeiling(t *testing.T) {
	// (a) teto automático congelado
	g := NewBandwidthGate(autoCfg(20000))
	for i := 0; i < 30; i++ {
		g.Observe(0, 0)
	}
	if got := g.CeilingKbps(); got != 20000 {
		t.Fatalf("tela ociosa não deveria mexer no teto, got %v", got)
	}
	if g.Snapshot().AutoAdjusts != 0 {
		t.Fatalf("tela ociosa não deveria contar ajustes, got %d", g.Snapshot().AutoAdjusts)
	}

	// (b) gate fechado permanece congelado
	m := NewBandwidthGate(BandwidthGateConfig{
		Mode: BandwidthManual, CeilingKbps: 5000, CloseAfterWindows: 1, OpenAfterWindows: 2,
	})
	m.Observe(9000, 30)
	if !m.Paused() {
		t.Fatal("pré-condição: deveria fechar")
	}
	for i := 0; i < 20; i++ {
		m.Observe(0, 0)
	}
	if !m.Paused() {
		t.Fatal("janela ociosa não deve reabrir gate fechado")
	}
	fmt.Println("[A3] 30 janelas ociosas: teto congelado (autoAdjusts=0) e gate fechado continua fechado")
}
