package remotesession

import (
	"sync"
	"time"
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
func (qc QualityConfig) EffectiveJpegQuality() int {
	if qc.overrideImageQ > 0 {
		return qc.overrideImageQ
	}
	return qc.JpegQuality
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
	// e a adaptação automática (adapt/downgrade) fica DESABILITADA. O valor
	// definido permanece fixo — a partir do momento em que o viewer envia um
	// override explícito, o agente NÃO deve rebaixar por conta própria.
	manualMode bool

	// Metricas para adaptacao
	frameCount    int
	bytesLastSec  int
	lastAdaptTime time.Time
	rttMs         float64
	lossPercent   float64
}

// Profiles padrao — alinhados com QualityProfileMapping.cs do backend.
// ScaleFactor SEMPRE 1.0: a resolução é a nativa do monitor (o viewer
// redimensiona via CSS para caber na janela). Reduzir a resolução aqui
// causava tela minúscula no viewer.
// "fast": FPS 20 (fluido com banda moderada).
// "unlimited": SEM LIMITE de FPS (captura o mais rápido possível).
var defaultProfiles = map[string]QualityConfig{
	"ultra":     {JpegQuality: 92, Fps: 30, ScaleFactor: 1.0},
	"fast":      {JpegQuality: 80, Fps: 20, ScaleFactor: 1.0},
	"high":      {JpegQuality: 75, Fps: 15, ScaleFactor: 1.0},
	"medium":    {JpegQuality: 60, Fps: 12, ScaleFactor: 1.0},
	"low":       {JpegQuality: 40, Fps: 5, ScaleFactor: 1.0},
	"ultralow":  {JpegQuality: 25, Fps: 2, ScaleFactor: 1.0},
	"unlimited": {JpegQuality: 75, Fps: 0, ScaleFactor: 1.0}, // 0 = sem limite
}

// NewQualityManager cria um gerenciador de qualidade.
func NewQualityManager(cfg QualityConfig) QualityManager {
	if cfg.JpegQuality == 0 {
		cfg = defaultProfiles["high"]
	}
	cfg.overrideMaxFps = -1 // -1 = usar perfil (não setado); 0 = sem limite; >0 = max
	return QualityManager{
		profile:       "high",
		current:       cfg,
		lastAdaptTime: time.Now(),
	}
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
	}
}

// Profile retorna o nome do perfil atual.
func (qm *QualityManager) Profile() string {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.profile
}

// SetImageQuality define override de compressão (1-100). 0 = usar perfil.
// Um override manual desabilita a adaptação automática.
func (qm *QualityManager) SetImageQuality(q int) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.current.overrideImageQ = q
	qm.manualMode = true
}

// ClearImageQuality remove o override de qualidade de imagem (volta ao perfil).
func (qm *QualityManager) ClearImageQuality() {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.current.overrideImageQ = 0
	qm.manualMode = false
}

// SetMaxFps define override de FPS. 0 = sem limite (captura o mais rápido
// possível); -1 = limpar override (voltar ao perfil).
// Um override manual desabilita a adaptação automática.
func (qm *QualityManager) SetMaxFps(fps int) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	if fps < 0 {
		qm.current.overrideMaxFps = -1
		qm.manualMode = false
		return
	}
	qm.current.overrideMaxFps = fps
	qm.manualMode = true
}

// ClearMaxFps remove o override de FPS (volta ao perfil).
func (qm *QualityManager) ClearMaxFps() {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.current.overrideMaxFps = -1
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

// UpdateNetworkMetrics atualiza metricas de rede (chamado pelo viewer via ack).
func (qm *QualityManager) UpdateNetworkMetrics(rttMs, lossPercent float64) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.rttMs = rttMs
	qm.lossPercent = lossPercent
}

func (qm *QualityManager) adapt() {
	// NUNCA adapta automaticamente quando o usuário está em modo manual.
	if qm.manualMode {
		return
	}

	// Adaptacao baseada em RTT e perda
	if qm.rttMs > 300 || qm.lossPercent > 10 {
		qm.downgrade()
		return
	}

	// Banda estimada: bytes/seg
	bandwidthKbps := float64(qm.bytesLastSec*8) / 2.0 / 1000.0 // media em 2s
	if bandwidthKbps < 500 {
		qm.current = defaultProfiles["ultralow"]
		qm.profile = "ultralow-adapted"
	} else if bandwidthKbps < 800 {
		qm.current = defaultProfiles["low"]
		qm.profile = "low-adapted"
	}
}

func (qm *QualityManager) downgrade() {
	switch qm.profile {
	case "high", "ultra":
		qm.current = defaultProfiles["medium"]
		qm.profile = "medium-adapted"
	case "medium":
		qm.current = defaultProfiles["low"]
		qm.profile = "low-adapted"
	case "low":
		qm.current = defaultProfiles["ultralow"]
		qm.profile = "ultralow-adapted"
	}
}
