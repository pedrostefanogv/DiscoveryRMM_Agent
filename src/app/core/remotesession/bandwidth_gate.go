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

// BandwidthMode é o modo de teto de banda escolhido pelo operador na janela do
// visualizador remoto. Espelha os modos de qualidade da UI (Auto / HD / Manual).
type BandwidthMode string

const (
	// BandwidthAuto: o agente AJUSTA o teto sozinho, subindo quando o viewer
	// recebe com folga e descendo quando o link aperta, dentro de [AutoMin, AutoMax].
	BandwidthAuto BandwidthMode = "auto"

	// BandwidthHD: SEM teto. Prioriza fluidez/fidelidade; a banda é
	// responsabilidade da rede e da escada de qualidade.
	BandwidthHD BandwidthMode = "hd"

	// BandwidthManual: teto FIXO escolhido pelo operador (preset).
	BandwidthManual BandwidthMode = "manual"
)

// BandwidthGateConfig parametriza o gate.
type BandwidthGateConfig struct {
	// Mode: auto | hd | manual. Vazio é tratado como "hd" (sem teto), que é o
	// comportamento histórico — nunca pausar.
	Mode BandwidthMode

	// Teto em kbps. <= 0 DESLIGA o gate (comportamento anterior preservado).
	// No modo auto este é o valor CORRENTE (ajustado em runtime por adjustAuto).
	CeilingKbps float64

	// ── Modo AUTO: limites e passo do ajuste automático do teto ──
	AutoMinKbps  float64 // piso do teto automático (0 = 1000)
	AutoMaxKbps  float64 // teto máximo do automático (0 = 30000)
	AutoStepKbps float64 // passo do ajuste (0 = 2000)
	// AutoStartKbps: teto INICIAL do modo auto quando a política automática
	// começa sem um teto corrente. É o valor de PARTIDA; o auto-ajuste o move
	// dentro de [AutoMinKbps, AutoMaxKbps] a partir da primeira janela.
	// Espelha AutoStartKbps do servidor (RemoteAccessOptions.cs).
	// 0 (ou ausente) = default 8000, clampeado ao range.
	AutoStartKbps float64
	// AutoRaiseFactor: fração do teto que o viewer precisa receber para o teto
	// SUBIR. AutoLowerFactor: abaixo dela o teto DESCE. Raise > Lower cria
	// histerese (evita oscilar).
	AutoRaiseFactor float64
	AutoLowerFactor float64

	// ServerMaxKbps: teto ABSOLUTO imposto pelo servidor, aplicável a QUALQUER
	// modo (inclusive HD). 0 = sem restrição. Protege a rede corporativa de um
	// operador que escolha "sem limite".
	ServerMaxKbps float64

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
		Mode:          BandwidthHD, // sem teto por padrão: preserva o comportamento atual
		CeilingKbps:   0,
		AutoMinKbps:   1000,
		AutoMaxKbps:   30000,
		AutoStepKbps:  2000,
		AutoStartKbps: 8000, // espelha AutoStartKbps do servidor (RemoteAccessOptions.cs)

		AutoRaiseFactor:   0.75,
		AutoLowerFactor:   0.6,
		ServerMaxKbps:     0,
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
	closeCount  int
	openCount   int
	autoAdjusts int // quantas vezes o teto do modo AUTO foi ajustado
	lastKbps    float64

	// autoStartKbps: teto de partida do modo auto já normalizado (nunca <= 0).
	autoStartKbps float64
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
	cfg = normalizeBandwidthConfig(cfg, def)
	return &BandwidthGate{cfg: cfg, autoStartKbps: cfg.AutoStartKbps, now: time.Now}
}

// normalizeBandwidthConfig aplica defaults e a regra de teto do servidor.
//
// Regras:
//   - Mode vazio/desconhecido → "hd" (sem teto; comportamento histórico).
//   - modo hd → CeilingKbps = 0 (sem teto), exceto se ServerMaxKbps impõe um.
//   - ServerMaxKbps é um TETO ABSOLUTO: limita QUALQUER modo, inclusive manual
//     e hd. É o único ponto em que o servidor sobrepõe a escolha do operador.
func normalizeBandwidthConfig(cfg, def BandwidthGateConfig) BandwidthGateConfig {
	switch cfg.Mode {
	case BandwidthAuto, BandwidthHD, BandwidthManual:
	default:
		cfg.Mode = def.Mode
	}

	if cfg.AutoMinKbps <= 0 {
		cfg.AutoMinKbps = def.AutoMinKbps
	}
	if cfg.AutoMaxKbps <= 0 {
		cfg.AutoMaxKbps = def.AutoMaxKbps
	}
	if cfg.AutoMaxKbps < cfg.AutoMinKbps {
		cfg.AutoMaxKbps = cfg.AutoMinKbps
	}
	if cfg.AutoStepKbps <= 0 {
		cfg.AutoStepKbps = def.AutoStepKbps
	}
	if cfg.AutoStartKbps <= 0 {
		cfg.AutoStartKbps = def.AutoStartKbps
	}
	// O teto inicial vive dentro do range, como qualquer outro teto do auto.
	cfg.AutoStartKbps = clampKbps(cfg.AutoStartKbps, cfg.AutoMinKbps, cfg.AutoMaxKbps)
	if cfg.AutoRaiseFactor <= 0 || cfg.AutoRaiseFactor > 1 {
		cfg.AutoRaiseFactor = def.AutoRaiseFactor
	}
	if cfg.AutoLowerFactor <= 0 || cfg.AutoLowerFactor >= cfg.AutoRaiseFactor {
		// Garante histerese: o limite de descida TEM de ser menor que o de subida.
		cfg.AutoLowerFactor = cfg.AutoRaiseFactor * 0.8
	}

	if cfg.Mode == BandwidthHD {
		cfg.CeilingKbps = 0
	}
	// No modo auto, o teto corrente começa no teto INICIAL do auto se vier
	// zerado — não no piso (o piso é o limite inferior do auto-ajuste, não o
	// ponto de partida da política automática).
	if cfg.Mode == BandwidthAuto && cfg.CeilingKbps <= 0 {
		cfg.CeilingKbps = clampKbps(cfg.AutoStartKbps, cfg.AutoMinKbps, cfg.AutoMaxKbps)
	}

	cfg.CeilingKbps = applyServerCap(cfg.CeilingKbps, cfg)
	return cfg
}

// applyServerCap aplica o teto absoluto do servidor a um teto pretendido.
// ServerMaxKbps = 0 significa "sem restrição do servidor".
func applyServerCap(ceiling float64, cfg BandwidthGateConfig) float64 {
	if cfg.ServerMaxKbps <= 0 {
		return ceiling
	}
	// Sem teto (0 = ilimitado) vira o teto do servidor: é justamente o caso
	// que o ServerMaxKbps existe para conter.
	if ceiling <= 0 {
		return cfg.ServerMaxKbps
	}
	if ceiling > cfg.ServerMaxKbps {
		return cfg.ServerMaxKbps
	}
	return ceiling
}

// clampKbps limita v ao intervalo [lo, hi]; hi <= 0 ignora o limite superior.
func clampKbps(v, lo, hi float64) float64 {
	if v < lo {
		v = lo
	}
	if hi > 0 && v > hi {
		v = hi
	}
	return v
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
		// fechado (não há evidência de que o link voltou). Também NÃO ajusta o
		// teto: bitrate baixo com a tela parada não diz nada sobre a capacidade
		// do link, e usá-lo derrubaria o teto injustificadamente.
		return
	}

	// Modo AUTO: primeiro move o teto conforme o tráfego real, depois avalia o
	// gate com o teto já atualizado.
	g.adjustAuto(recvKbps)

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

// Snapshot devolve o estado completo do gate para log/diagnóstico e para o
// viewer (que pode exibir o teto corrente no modo auto).
type BandwidthSnapshot struct {
	Mode        BandwidthMode `json:"mode"`
	CeilingKbps float64       `json:"ceilingKbps"`
	Paused      bool          `json:"paused"`
	Closes      int           `json:"closes"`
	Opens       int           `json:"opens"`
	AutoAdjusts int           `json:"autoAdjusts"`
	LastKbps    float64       `json:"lastKbps"`
}

// Snapshot devolve o estado atual do gate numa struct serializável.
func (g *BandwidthGate) Snapshot() BandwidthSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return BandwidthSnapshot{
		Mode:        g.cfg.Mode,
		CeilingKbps: g.cfg.CeilingKbps,
		Paused:      g.closed,
		Closes:      g.closeCount,
		Opens:       g.openCount,
		AutoAdjusts: g.autoAdjusts,
		LastKbps:    g.lastKbps,
	}
}

// SetCeiling ajusta o teto em runtime (0 desliga e reabre o gate).
// Aplica o teto absoluto do servidor, se houver.
func (g *BandwidthGate) SetCeiling(kbps float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cfg.CeilingKbps = applyServerCap(kbps, g.cfg)
	if g.cfg.CeilingKbps <= 0 {
		g.closed = false
		g.over, g.under = 0, 0
	}
}

// SetLimits atualiza os limites do modo AUTO e o teto absoluto do servidor em
// runtime, sem trocar o modo. Campo <= 0 preserva o valor corrente.
//
// O teto corrente é revalidado: se o novo ServerMax ficou abaixo dele, o teto
// desce imediatamente (a política nova vale já na próxima janela).
func (g *BandwidthGate) SetLimits(autoMin, autoMax, autoStep, serverMax float64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if autoMin > 0 {
		g.cfg.AutoMinKbps = autoMin
	}
	if autoMax > 0 {
		g.cfg.AutoMaxKbps = autoMax
	}
	if g.cfg.AutoMaxKbps < g.cfg.AutoMinKbps {
		g.cfg.AutoMaxKbps = g.cfg.AutoMinKbps
	}
	if autoStep > 0 {
		g.cfg.AutoStepKbps = autoStep
	}
	if serverMax > 0 {
		g.cfg.ServerMaxKbps = serverMax
	}
	// O teto inicial acompanha o range: trocar o range não pode deixar a
	// partida do auto fora de [AutoMin, AutoMax].
	g.autoStartKbps = clampKbps(g.autoStartKbps, g.cfg.AutoMinKbps, g.cfg.AutoMaxKbps)

	// Revalida o teto corrente contra o novo range/limite.
	//
	// Três casos:
	//   - teto > 0: apenas clampado ao range e ao teto do servidor;
	//   - teto == 0 no modo AUTO: parte do piso do range;
	//   - teto == 0 nos modos HD/MANUAL: segue SEM teto PRÓPRIO, mas o
	//     ServerMaxKbps (se houver) passa a valer — foi o bug que o teste
	//     TestBandwidthSetLimitsUpdatesServerCap pegou: em HD com ServerMax
	//     novo, o teto ficava 0 e o limite do servidor era ignorado.
	if g.cfg.CeilingKbps > 0 {
		c := clampKbps(g.cfg.CeilingKbps, g.cfg.AutoMinKbps, g.cfg.AutoMaxKbps)
		g.cfg.CeilingKbps = applyServerCap(c, g.cfg)
	} else if g.cfg.Mode == BandwidthAuto {
		// Sem teto corrente no modo auto: parte do teto INICIAL do auto (o mesmo
		// critério do SetMode), não do piso.
		g.cfg.CeilingKbps = applyServerCap(g.autoStartKbps, g.cfg)
	} else {
		// HD/MANUAL sem teto próprio: o servidor ainda pode impor um.
		g.cfg.CeilingKbps = applyServerCap(0, g.cfg)
	}
}

// SetMode troca o MODO em runtime (chamado pelo canal .control quando o
// operador muda Auto/HD/Manual na janela do visualizador).
//
//   - hd:     zera o teto (sem limite, sujeito ao ServerMaxKbps);
//   - manual: usa ceilingKbps (o preset escolhido; 0 = "Sem limite");
//   - auto:   parte de ceilingKbps, se dado; senão mantém o corrente se ele for
//     um teto VÁLIDO (> 0) e deixa a adaptação automática ajustá-lo
//     dentro de [AutoMin, AutoMax]; se o corrente for 0 (sem teto),
//     começa no teto inicial do auto.
//
// Reabre o gate ao trocar de modo: a decisão anterior pertencia a outra
// política e não deve arrastar para a nova.
func (g *BandwidthGate) SetMode(mode BandwidthMode, ceilingKbps float64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch mode {
	case BandwidthAuto, BandwidthHD, BandwidthManual:
		g.cfg.Mode = mode
	default:
		return // modo desconhecido: ignora (não derruba a sessão por isso)
	}

	switch mode {
	case BandwidthHD:
		g.cfg.CeilingKbps = applyServerCap(0, g.cfg)
	case BandwidthManual:
		g.cfg.CeilingKbps = applyServerCap(ceilingKbps, g.cfg)
	case BandwidthAuto:
		switch {
		case ceilingKbps > 0:
			// Teto explícito: é o ponto de partida, clampeado ao range.
			g.cfg.CeilingKbps = clampKbps(ceilingKbps, g.cfg.AutoMinKbps, g.cfg.AutoMaxKbps)
		case g.cfg.CeilingKbps > 0:
			// Sem teto no comando e já há um teto corrente válido: PRESERVA.
			// (Só faz sentido clampar: o range pode ter mudado antes do modo.)
			g.cfg.CeilingKbps = clampKbps(g.cfg.CeilingKbps, g.cfg.AutoMinKbps, g.cfg.AutoMaxKbps)
		default:
			// Sem teto no comando e corrente == 0 (sem teto algum): começa no
			// teto inicial do auto, não no piso. O piso é o MÍNIMO que o
			// auto-ajuste pode atingir, não o valor de partida da política
			// automática — usá-lo como partida faz o teto depender de COMO o
			// operador chegou ao modo (ex.: manual→auto preservava um teto alto,
			// manual→hd→auto caía no piso, com o mesmo comando do servidor).
			g.cfg.CeilingKbps = clampKbps(g.autoStartKbps, g.cfg.AutoMinKbps, g.cfg.AutoMaxKbps)
		}
		g.cfg.CeilingKbps = applyServerCap(g.cfg.CeilingKbps, g.cfg)
	}

	// Nova política, estado limpo.
	g.closed = false
	g.over, g.under = 0, 0
}

// Mode devolve o modo corrente (observabilidade/testes).
func (g *BandwidthGate) Mode() BandwidthMode {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.Mode
}

// CeilingKbps devolve o teto corrente em kbps (0 = sem teto).
func (g *BandwidthGate) CeilingKbps() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.CeilingKbps
}

// adjustAuto move o teto automático uma vez, com base no bitrate recebido.
// Deve ser chamado com g.mu travado.
//
// Lógica (só no modo auto, e só com teto ativo):
//   - viewer recebe >= teto*AutoRaiseFactor  → há folga  → sobe um passo;
//   - viewer recebe <  teto*AutoLowerFactor  → link aperta → desce um passo;
//   - entre os dois                          → zona morta → não mexe.
//
// A zona morta entre Lower e Raise é a histerese: sem ela o teto oscilaria a
// cada janela e o stream ficaria instável.
func (g *BandwidthGate) adjustAuto(recvKbps float64) {
	if g.cfg.Mode != BandwidthAuto || g.cfg.CeilingKbps <= 0 {
		return
	}
	raiseAt := g.cfg.CeilingKbps * g.cfg.AutoRaiseFactor
	lowerAt := g.cfg.CeilingKbps * g.cfg.AutoLowerFactor

	next := g.cfg.CeilingKbps
	switch {
	case recvKbps >= raiseAt:
		next = g.cfg.CeilingKbps + g.cfg.AutoStepKbps
	case recvKbps < lowerAt:
		next = g.cfg.CeilingKbps - g.cfg.AutoStepKbps
	default:
		return // zona morta: mantém
	}

	next = clampKbps(next, g.cfg.AutoMinKbps, g.cfg.AutoMaxKbps)
	next = applyServerCap(next, g.cfg)
	if next == g.cfg.CeilingKbps {
		return
	}
	g.cfg.CeilingKbps = next
	g.autoAdjusts++

	// O teto mudou: o estado de fechamento anterior não vale mais para o novo
	// valor. Reabre para reavaliar com a política nova (o Observe seguinte
	// decide de novo com base no tráfego real).
	g.closed = false
	g.over, g.under = 0, 0
}
