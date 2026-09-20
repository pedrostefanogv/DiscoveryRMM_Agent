package remotesession

import (
	"runtime"
	"sync"
	"time"

	"discovery/app/core/platform"
)

// QualityConfig define parametros de qualidade por perfil.
type QualityConfig struct {
	JpegQuality int
	Fps         int
	ScaleFactor float64 // 1.0 = nativa, 0.5 = metade

	// Overrides manuais (têm precedência sobre o perfil)
	// overrideImageQ: 0 = usar perfil; 1-100 = override
	// overrideMaxFps: -1 = usar perfil; 0 = sem limite; >0 = FPS maximo
	overrideImageQ int
	overrideMaxFps int
}

// EffectiveJpegQuality retorna a qualidade JPEG efetiva (override ou perfil).
// Sempre dentro do range 10-90: perfis que excedem o teto (ultra=92) são
// reportados como 90, alinhando o valor exibido/efetivo às opções da UI.
func (qc QualityConfig) EffectiveJpegQuality() int {
	q := qc.overrideImageQ
	if q <= 0 {
		q = qc.JpegQuality
	}
	if q > imageQualityMax {
		q = imageQualityMax
	}
	if q < imageQualityMin {
		q = imageQualityMin
	}
	return q
}

// EffectiveFps retorna o FPS efetivo (override ou perfil).
// 0 = SEM LIMITE (captura o mais rápido possível, sujeito ao backpressure).
func (qc QualityConfig) EffectiveFps() int {
	if qc.overrideMaxFps >= 0 {
		return qc.overrideMaxFps
	}
	return qc.Fps
}

// QualityManager adapta qualidade do stream baseado em metricas.
type QualityManager struct {
	mu      sync.RWMutex
	profile string
	current QualityConfig

	// manualMode: quando true, o usuário definiu qualidade/FPS/codec manualmente
	// e a adaptação automática fica DESABILITADA. O valor definido permanece
	// fixo — a partir do momento em que o viewer envia um override explícito, o
	// agente NÃO deve rebaixar por conta própria.
	manualMode bool

	// Escada adaptativa do modo AUTO (fix 17/09): qualidade de imagem em
	// múltiplos de 10 dentro de [10, 90] — "variando de 10 em 10". A escada
	// ajusta APENAS a compressão (overrideImageQ); FPS/codec continuam do
	// perfil. Alimentada pelas métricas de rede que o VIEWER reporta a cada 2s
	// (netstats via .input: rttMs, recvKbps, recvFrames). Sem netstats (viewer
	// antigo/sem feedback), NÃO adapta às cegas — mantém o perfil.
	autoImageQ int
	goodStreak int

	// Metricas para adaptacao
	frameCount     int
	bytesLastSec   int
	lastAdaptTime  time.Time
	rttMs          float64
	recvKbps       float64
	recvFrames     int
	netstatsSeen   bool
	lastNetstatsAt time.Time
}

// Profiles padrao — alinhados com QualityProfileMapping.cs do backend.
// ScaleFactor SEMPRE 1.0: a resolução é a nativa do monitor (o viewer
// redimensiona via CSS para caber na janela). Reduzir a resolução aqui
// causava tela minúscula no viewer.
// "fast": FPS 20 (fluido com banda moderada).
// "unlimited": SEM LIMITE de FPS (captura o mais rápido possível).
// Qualidades na grade da escada automática: 10 a 90, de 10 em 10.
// (92/75/25 fora da grade não existem mais — o card nunca exibe 75%.)
var defaultProfiles = map[string]QualityConfig{
	"ultra":     {JpegQuality: 90, Fps: 30, ScaleFactor: 1.0},
	"fast":      {JpegQuality: 80, Fps: 20, ScaleFactor: 1.0},
	"high":      {JpegQuality: 70, Fps: 15, ScaleFactor: 1.0},
	"medium":    {JpegQuality: 60, Fps: 12, ScaleFactor: 1.0},
	"low":       {JpegQuality: 40, Fps: 5, ScaleFactor: 1.0},
	"ultralow":  {JpegQuality: 30, Fps: 2, ScaleFactor: 1.0},
	"unlimited": {JpegQuality: 80, Fps: 0, ScaleFactor: 1.0}, // 0 = sem limite
}

// NewQualityManager cria um gerenciador de qualidade.
// A escada automática é SEMEADA pela POTÊNCIA DA MÁQUINA (SeedAutoFromHost),
// não pelo perfil: no modo auto o ponto de partida reflete o hardware/uso
// local, e a rede (netstats do viewer) refinA de 10 em 10 em runtime.
func NewQualityManager(cfg QualityConfig) QualityManager {
	if cfg.JpegQuality == 0 {
		cfg = defaultProfiles["high"]
	}
	cfg.overrideMaxFps = -1 // -1 = usar perfil (não setado); 0 = sem limite; >0 = max
	return QualityManager{
		profile:       "high",
		current:       cfg,
		autoImageQ:    clampQualityLadder(SeedAutoImageQuality()),
		lastAdaptTime: time.Now(),
	}
}

// SeedAutoImageQuality calcula a qualidade inicial do modo AUTO pelo PODER DA
// MÁQUINA: uso de CPU no momento + memória disponível + nº de núcleos.
// Chamado no start da sessão (fix card 75%: antes a sessão nascia em modo
// manual com o override do perfil, ex. unlimited=75, e a escada 10-90 de 10
// em 10 nunca rodava). Retorna 0 quando não consegue medir nada.
func SeedAutoImageQuality() int {
	return SeedAutoImageQualityFrom(runtime.NumCPU(), platform.SampleCPUPercent(), platform.SampleMemoryPercent())
}

// SeedAutoImageQualityFrom é a versão testável do cálculo da semente.
// cores: nº de núcleos (proxy do poder de processamento) · cpu: uso atual
// 0-100 (-1 desconhecido) · mem: uso de memória 0-100 (-1 desconhecido).
// Ajusta a qualidade pela CARGA NO MOMENTO: máquina ocupada começa mais leve.
func SeedAutoImageQualityFrom(cores int, cpu, mem float64) int {
	base := 50 // default conservador
	switch {
	case cores <= 2:
		base = 30 // máquina fraca: começa leve
	case cores <= 4:
		base = 40
	case cores <= 8:
		base = 50
	case cores > 8:
		base = 60 // máquina forte
	}
	score := base
	if cpu >= 0 {
		switch {
		case cpu >= 85:
			score = base - 20
		case cpu >= 60:
			score = base - 10
		case cpu <= 15:
			score = base + 10
		}
	}
	if mem >= 0 {
		switch {
		case mem >= 90:
			score -= 10
		case mem <= 40:
			score += 10
		}
	}
	return clampQualityLadder(score)
}

// Current retorna a configuracao atual.
func (qm *QualityManager) Current() QualityConfig {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.current
}

// SetProfile atualiza para um perfil pre-definido, preservando overrides.
func (qm *QualityManager) SetProfile(profile string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	if cfg, ok := defaultProfiles[profile]; ok {
		qm.profile = profile
		// Preserva overrides manuais ao trocar de perfil
		oldOverrideQ := qm.current.overrideImageQ
		oldOverrideFps := qm.current.overrideMaxFps
		qm.current = cfg
		qm.current.overrideImageQ = oldOverrideQ
		qm.current.overrideMaxFps = oldOverrideFps
		// Re-semeia a escada automática a partir do novo perfil.
		qm.autoImageQ = clampQualityLadder(cfg.JpegQuality)
		qm.goodStreak = 0
	}
}

// Profile retorna o nome do perfil atual.
func (qm *QualityManager) Profile() string {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.profile
}

// imageQualityMin/imageQualityMax definem o range aceito para o override
// MANUAL de qualidade de imagem (via UI do viewer) e para a escada AUTOMÁTICA
// (10→90, de 10 em 10). Fora desse range o valor é rejeitado — evita JPEG 1%
// (inútil) e 100% (banda explosiva).
const (
	imageQualityMin = 10
	imageQualityMax = 90
)

// ── Parâmetros da escada adaptativa (modo auto) ──
// A fonte de verdade é o VIEWER (netstats via .input a cada 2s): recvKbps é o
// bitrate EFETIVAMENTE recebido (fim-a-fim) e recvFrames detecta tela ociosa.
// RTT absoluto NÃO é usado para subir/descer (relógios de máquinas diferentes
// têm skew — Date.now()-frame.ts não é latência real); só um teto extremo
// (>3s) derruba a qualidade, imune a skew razoável.
const (
	// Abaixo disso com frames chegando → link não sustenta → desce 10.
	adaptiveMinRecvKbps = 800
	// Sustentado por >=2 janelas de 2s → sobe 10 (recuperação gradual).
	adaptiveGoodRecvKbps = 4000
	// RTT extremo (skew-imune) → desce.
	adaptiveRttDownMs = 3000
	// Janela sem netstats → consideramos o feedback stale (não adapta).
	netstatsStaleAfter = 8 * time.Second
)

// clampQualityLadder aproxima q para a grade da escada (múltiplos de 10) e
// clampa em [10, 90] — ex.: 75→80, 92→90, 25→30, 40→40, 7→10, 500→90.
func clampQualityLadder(q int) int {
	if q <= 0 {
		return imageQualityMin
	}
	// arredonda para o múltiplo de 10 mais próximo
	rounded := ((q + 5) / 10) * 10
	if rounded < imageQualityMin {
		rounded = imageQualityMin
	}
	if rounded > imageQualityMax {
		rounded = imageQualityMax
	}
	return rounded
}

// SetImageQuality define override de compressão MANUAL (viewer). Aceita
// 10-90 — valores fora do range são clampeados (evita JPEG 1% inútil e
// 100% com banda explosiva). 0/negativo = limpar override (voltar ao perfil).
// NÃO use para aplicar valores de perfil no modo automático: use
// SetImageQualityAuto, que não clampa (perfis vão de 25 a 92).
func (qm *QualityManager) SetImageQuality(q int) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	if q <= 0 {
		// 0/negativo = limpar override (voltar ao perfil)
		qm.current.overrideImageQ = 0
		return
	}
	if q < imageQualityMin {
		q = imageQualityMin
	}
	if q > imageQualityMax {
		q = imageQualityMax
	}
	qm.current.overrideImageQ = q
}

// SetImageQualityAuto semeia a ESCADA automática com o valor informado
// (aproximado à grade de 10 e clampado em [10,90]) e aplica como override
// corrente. É o ponto de partida da adaptação — o adapt() refinA de 10 em 10.
// NÃO altera o modo manual — o caller controla isso via SetManualMode.
func (qm *QualityManager) SetImageQualityAuto(q int) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	if q <= 0 {
		qm.current.overrideImageQ = 0
		return
	}
	qm.autoImageQ = clampQualityLadder(q)
	qm.current.overrideImageQ = qm.autoImageQ
	qm.goodStreak = 0
}

// ResetAutoToProfile limpa overrides manuais e reativa a escada automática
// (chamado quando o viewer volta ao modo auto). Se a sessão já tem histórico
// de adaptação (netstats do viewer), a escada APRENDIDA é preservada; sem
// histórico, a semente é a POTÊNCIA DA MÁQUINA — nunca o valor do perfil
// (75% do "unlimited" era o card travado).
func (qm *QualityManager) ResetAutoToProfile() {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.current.overrideImageQ = 0
	if !qm.netstatsSeen {
		if seed := SeedAutoImageQuality(); seed > 0 {
			qm.autoImageQ = clampQualityLadder(seed)
		}
	}
	qm.goodStreak = 0
}

// ClearImageQuality remove o override de qualidade de imagem (volta ao perfil).
func (qm *QualityManager) ClearImageQuality() {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.current.overrideImageQ = 0
}

// SetMaxFps define override de FPS. 0 = sem limite (captura o mais rápido
// possível); -1 = limpar override (voltar ao perfil).
func (qm *QualityManager) SetMaxFps(fps int) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	if fps < 0 {
		qm.current.overrideMaxFps = -1
		return
	}
	qm.current.overrideMaxFps = fps
}

// ClearMaxFps remove o override de FPS (volta ao perfil).
func (qm *QualityManager) ClearMaxFps() {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.current.overrideMaxFps = -1
}

// SetManualMode liga/desliga o modo manual. Quando manual, a adaptação
// automática (adapt/downgrade) fica desabilitada e o valor definido
// permanece fixo. É a fonte da verdade para o flag `auto` enviado pelo
// viewer — separado dos overrides de qualidade/FPS, que podem ser aplicados
// no start (defaults) sem desligar a adaptação automática.
func (qm *QualityManager) SetManualMode(mode bool) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.manualMode = mode
}

// RecordFrame registra metricas de um frame para adaptacao.
func (qm *QualityManager) RecordFrame(bytes int, ts time.Time) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	qm.frameCount++
	qm.bytesLastSec += bytes

	// Adapta a cada 2 segundos
	if ts.Sub(qm.lastAdaptTime) >= 2*time.Second {
		qm.adapt()
		qm.frameCount = 0
		qm.bytesLastSec = 0
		qm.lastAdaptTime = ts
	}
}

// UpdateNetworkMetrics recebe as métricas de rede medidas no VIEWER
// (mensagem netstats via .input a cada 2s): rttMs = latência heurística
// (Date.now()-frame.ts — contém skew de relógio entre máquinas; usar apenas
// para extremos), recvKbps = bitrate recebido fim-a-fim, recvFrames = quadros
// recebidos na janela (0 = tela ociosa — NÃO é congestionamento).
func (qm *QualityManager) UpdateNetworkMetrics(rttMs, recvKbps float64, recvFrames int) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.rttMs = rttMs
	qm.recvKbps = recvKbps
	qm.recvFrames = recvFrames
	qm.netstatsSeen = true
	qm.lastNetstatsAt = time.Now()
}

// adapt roda a cada 2s (RecordFrame) e move a escada de qualidade do modo
// AUTO em passos de 10: degrada rápido (1 janela ruim = -10) e recupera
// devagar (2 janelas boas consecutivas = +10), evitando oscilação.
func (qm *QualityManager) adapt() {
	// NUNCA adapta automaticamente quando o usuário está em modo manual.
	if qm.manualMode {
		return
	}

	// Sem feedback do viewer (nunca chegou netstats ou está stale): NÃO adapta
	// às cegas — mantém a qualidade do perfil. O antigo "adapt por banda
	// medida no agent" trocava o perfil inteiro (FPS junto) em degraus grosseiros
	// (75/60/40/25) e nunca subia de volta.
	now := time.Now()
	if !qm.netstatsSeen || now.Sub(qm.lastNetstatsAt) > netstatsStaleAfter {
		return
	}

	// Tela ociosa (nenhum frame chegou ao viewer): não é congestionamento —
	// bitrate baixo com tela estática é esperado. Mantém.
	if qm.recvFrames <= 0 {
		return
	}

	// ── Dimensão máquina (carga local): CPU alta no momento força degrau
	// extra para baixo — a sessão não deve engasgar o host. Medição nativa
	// (GetSystemTimes, janela de 2s); falha (-1) é ignorada.
	hostHeavy := false
	if cpu := platform.SampleCPUPercent(); cpu >= 85 {
		hostHeavy = true
	}

	down := qm.recvKbps < adaptiveMinRecvKbps || qm.rttMs > adaptiveRttDownMs || hostHeavy
	good := qm.recvKbps > adaptiveGoodRecvKbps && !hostHeavy

	switch {
	case down:
		if qm.autoImageQ > imageQualityMin {
			qm.autoImageQ -= 10
		}
		qm.goodStreak = 0
	case good:
		qm.goodStreak++
		if qm.goodStreak >= 2 && qm.autoImageQ < imageQualityMax {
			qm.autoImageQ += 10
			qm.goodStreak = 0
		}
	default:
		qm.goodStreak = 0
	}

	qm.current.overrideImageQ = qm.autoImageQ
}
