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
	overrideImageQ int // 0 = usar perfil
	overrideMaxFps int // 0 = usar perfil
}

// EffectiveJpegQuality retorna a qualidade JPEG efetiva (override ou perfil).
func (qc QualityConfig) EffectiveJpegQuality() int {
	if qc.overrideImageQ > 0 {
		return qc.overrideImageQ
	}
	return qc.JpegQuality
}

// EffectiveFps retorna o FPS efetivo (override ou perfil).
func (qc QualityConfig) EffectiveFps() int {
	if qc.overrideMaxFps > 0 {
		return qc.overrideMaxFps
	}
	return qc.Fps
}

// QualityManager adapta qualidade do stream baseado em metricas.
type QualityManager struct {
	mu      sync.RWMutex
	profile string
	current QualityConfig

	// Metricas para adaptacao
	frameCount    int
	bytesLastSec  int
	lastAdaptTime time.Time
	rttMs         float64
	lossPercent   float64
}

// Profiles padrao — alinhados com QualityProfileMapping.cs do backend
var defaultProfiles = map[string]QualityConfig{
	"ultra":    {JpegQuality: 92, Fps: 30, ScaleFactor: 1.0},
	"high":     {JpegQuality: 75, Fps: 15, ScaleFactor: 1.0},
	"medium":   {JpegQuality: 60, Fps: 10, ScaleFactor: 0.75},
	"low":      {JpegQuality: 40, Fps: 5, ScaleFactor: 0.50},
	"ultralow": {JpegQuality: 25, Fps: 2, ScaleFactor: 0.30},
}

// NewQualityManager cria um gerenciador de qualidade.
func NewQualityManager(cfg QualityConfig) QualityManager {
	if cfg.JpegQuality == 0 {
		cfg = defaultProfiles["high"]
	}
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
func (qm *QualityManager) SetImageQuality(q int) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.current.overrideImageQ = q
}

// ClearImageQuality remove o override de qualidade de imagem (volta ao perfil).
func (qm *QualityManager) ClearImageQuality() {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.current.overrideImageQ = 0
}

// SetMaxFps define override de FPS (1-60). 0 = usar perfil.
func (qm *QualityManager) SetMaxFps(fps int) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.current.overrideMaxFps = fps
}

// ClearMaxFps remove o override de FPS (volta ao perfil).
func (qm *QualityManager) ClearMaxFps() {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.current.overrideMaxFps = 0
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
	// Adaptacao baseada em RTT e perda
	if qm.rttMs > 300 || qm.lossPercent > 10 {
		qm.downgrade()
		return
	}

	// Banda estimada: bytes/seg
	bandwidthKbps := float64(qm.bytesLastSec*8) / 2.0 / 1000.0 // media em 2s
	if bandwidthKbps < 500 {
		qm.current = defaultProfiles["ultra-low"]
		qm.profile = "ultra-low-adapted"
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
		qm.current = defaultProfiles["ultra-low"]
		qm.profile = "ultra-low-adapted"
	}
}
