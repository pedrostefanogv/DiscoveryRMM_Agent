package remotesession

import (
	"sync"
	"time"
)

// ── Teto de banda (gate de produção de frames) ──
//
// POR QUE ISTO EXISTE, SEPARADO DA QUALIDADE:
// a escada AUTO (quality.go) REDUZ a compressão quando a rede piora, mas
// continua PRODUZINDO frames no mesmo ritmo. Se o link não escoa, baixar a
// qualidade sem frear a produção apenas enche a fila: a latência cresce e o
// frame entregue fica cada vez mais velho (o pior cenário num RMM — o operador
// clica onde a tela JÁ NÃO ESTÁ).
//
// O ControlR resolve isso com DOIS controles independentes
// (FrameBasedCapturer.cs): GetEffectiveQuality interpola a qualidade, e
// MonitorBandwidth fecha um gate que PAUSA a captura ao exceder MaxBandwidthMbps.
// Este arquivo implementa o segundo controle.
//
// FONTE DA MEDIÇÃO: o bitrate REALMENTE RECEBIDO no viewer (netstats via
// .input), não o que o agente acha que enviou. É o único número fim-a-fim
// disponível e é imune a buffering do caminho.
//
// COMPORTAMENTO: histerese assimétrica — fecha o gate acima do teto e só o
// reabre depois de o tráfego cair bem abaixo dele (com janelas de carência).
// Sem histerese o gate oscilaria a cada janela de 2 s, picotando o stream.

// BandwidthGateConfig parametriza o gate.
type BandwidthGateConfig struct {
	// Teto em kbps. <= 0 DESLIGA o gate (comportamento anterior preservado).
	CeilingKbps float64

	// Fator de reabertura: o gate só reabre quando o bitrate cai abaixo de
	// CeilingKbps*ReopenFactor. <1 cria histerese.
	ReopenFactor float64

	// Janelas consecutivas acima do teto para FECHAR. Reage rápido (1 é o
	// default prático): banda estourada já é sinal forte.
	CloseAfterWindows int

	// Janelas consecutivas abaixo do limite de reabertura para ABRIR.
	// Reabre devagar para não oscilar.
	OpenAfterWindows int

	// Após reabrir, período mínimo de espera antes de poder fechar de novo.
	// Impede "flapping" quando o link está exatamente no limite.
	ReopenHoldFor time.Duration
}

// defaultBandwidthGateConfig devolve os defaults usados quando o teto é ligado.
func defaultBandwidthGateConfig() BandwidthGateConfig {
	return BandwidthGateConfig{
		CeilingKbps:       0, // desligado por padrão (preserva o comportamento atual)
		ReopenFactor:      0.7,
		CloseAfterWindows: 1,
		OpenAfterWindows:  2,
		ReopenHoldFor:     4 * time.Second,
	}
}

// BandwidthGate decide se a captura deve PAUSAR por excesso de banda.
//
// É seguro para uso concorrente: a decisão é lida no loop de captura e
// atualizada pela ingestão de netstats (caminho do viewer).
type BandwidthGate struct {
	mu     sync.Mutex
	cfg    BandwidthGateConfig
	closed bool
	over   int       // janelas consecutivas acima do teto
	under  int       // janelas consecutivas abaixo do limite de reabertura
	opened time.Time // quando reabriu pela última vez
	now    func() time.Time

	// Observabilidade: contadores para log/diagnóstico.
	closeCount int
	openCount  int
	lastKbps   float64
}

// NewBandwidthGate cria o gate com a configuração dada. Um teto <= 0 deixa o
// gate permanentemente ABERTO (nunca pausa) — é o default seguro.
func NewBandwidthGate(cfg BandwidthGateConfig) *BandwidthGate {
	def := defaultBandwidthGateConfig()
	if cfg.ReopenFactor <= 0 || cfg.ReopenFactor >= 1 {
		cfg.ReopenFactor = def.ReopenFactor
	}
	if cfg.CloseAfterWindows <= 0 {
		cfg.CloseAfterWindows = def.CloseAfterWindows
	}
	if cfg.OpenAfterWindows <= 0 {
		cfg.OpenAfterWindows = def.OpenAfterWindows
	}
	if cfg.ReopenHoldFor <= 0 {
		cfg.ReopenHoldFor = def.ReopenHoldFor
	}
	return &BandwidthGate{cfg: cfg, now: time.Now}
}

// SetClock substitui a fonte de tempo (testes).
func (g *BandwidthGate) SetClock(fn func() time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if fn != nil {
		g.now = fn
	}
}

// Enabled informa se há teto configurado.
func (g *BandwidthGate) Enabled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.CeilingKbps > 0
}

// Observe ingere o bitrate recebido (kbps) numa janela. Não faz nada se o gate
// está desligado (teto <= 0), preservando o comportamento anterior.
//
// recvFrames == 0 significa TELA OCIOSA, não congestionamento: bitrate baixo
// com a tela parada é o esperado, então a janela é ignorada (mesma regra da
// escada de qualidade em adapt()).
func (g *BandwidthGate) Observe(recvKbps float64, recvFrames int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.cfg.CeilingKbps <= 0 {
		return
	}
	g.lastKbps = recvKbps

	if recvFrames <= 0 {
		// Tela ociosa: não altera o estado. Se o gate estava fechado, mantém
		// fechado (não há evidência de que o link voltou).
		return
	}

	reopenBelow := g.cfg.CeilingKbps * g.cfg.ReopenFactor

	if !g.closed {
		if recvKbps > g.cfg.CeilingKbps {
			g.over++
			if g.over >= g.cfg.CloseAfterWindows {
				g.closed = true
				g.closeCount++
				g.under = 0
			}
		} else {
			g.over = 0
		}
		return
	}

	// Gate FECHADO: avalia reabertura.
	if recvKbps < reopenBelow {
		g.under++
		if g.under >= g.cfg.OpenAfterWindows {
			g.closed = false
			g.openCount++
			g.opened = g.now()
			g.over = 0
		}
	} else {
		g.under = 0
	}
}

// Paused informa se a captura deve ser PAUSADA neste ciclo.
//
// Respeita a carência pós-reabertura: logo após abrir, o gate não pode fechar
// de novo até ReopenHoldFor ter passado — evita oscilação em link no limite.
func (g *BandwidthGate) Paused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cfg.CeilingKbps <= 0 || !g.closed {
		return false
	}
	// Carência: se reabriu há pouco tempo, não fecha de novo imediatamente.
	if !g.opened.IsZero() && g.now().Sub(g.opened) < g.cfg.ReopenHoldFor {
		return false
	}
	return true
}

// Stats devolve contadores de observabilidade (log/diagnóstico).
func (g *BandwidthGate) Stats() (closes, opens int, lastKbps float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closeCount, g.openCount, g.lastKbps
}

// SetCeiling ajusta o teto em runtime (0 desliga e reabre o gate).
func (g *BandwidthGate) SetCeiling(kbps float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cfg.CeilingKbps = kbps
	if kbps <= 0 {
		g.closed = false
		g.over, g.under = 0, 0
	}
}
