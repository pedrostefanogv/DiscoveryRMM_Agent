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

// Profiles padrao
var defaultProfiles = map[string]QualityConfig{
	"ultra":     {JpegQuality: 90, Fps: 30, ScaleFactor: 1.0},
	"high":      {JpegQuality: 85, Fps: 20, ScaleFactor: 1.0},
	"medium":    {JpegQuality: 75, Fps: 15, ScaleFactor: 0.75},
	"low":       {JpegQuality: 60, Fps: 10, ScaleFactor: 0.50},
	"ultra-low": {JpegQuality: 50, Fps: 5, ScaleFactor: 0.50},
	"ultralow":  {JpegQuality: 50, Fps: 5, ScaleFactor: 0.50},
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

// SetProfile atualiza para um perfil pre-definido.
func (qm *QualityManager) SetProfile(profile string) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	if cfg, ok := defaultProfiles[profile]; ok {
		qm.profile = profile
		qm.current = cfg
	}
}

// Profile retorna o nome do perfil atual.
func (qm *QualityManager) Profile() string {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.profile
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
