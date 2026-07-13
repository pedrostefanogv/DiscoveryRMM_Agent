package app

import (
	"sync"
	"time"
)

// ── API Metrics ──────────────────────────────────────────────────────────
//
// Coleta de métricas de latência e erro por endpoint para telemetria.
// Usado para diagnóstico e monitoramento da comunicação com a API.

// EndpointMetric armazena métricas de um endpoint.
type EndpointMetric struct {
	Endpoint     string
	LastCall     time.Time
	LastLatency  time.Duration
	LastError    string
	SuccessCount int64
	ErrorCount   int64
	TotalCalls   int64
	TotalLatency time.Duration // cumulative for avg calculation
}

// ApiMetrics coleta e agrega métricas de chamadas à API.
type ApiMetrics struct {
	mu        sync.RWMutex
	metrics   map[string]*EndpointMetric
	startedAt time.Time
}

// NewApiMetrics cria um novo coletor de métricas.
func NewApiMetrics() *ApiMetrics {
	return &ApiMetrics{
		metrics:   make(map[string]*EndpointMetric),
		startedAt: time.Now(),
	}
}

// RecordCall registra uma chamada de API bem-sucedida.
func (m *ApiMetrics) RecordCall(endpoint string, latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metric, ok := m.metrics[endpoint]
	if !ok {
		metric = &EndpointMetric{Endpoint: endpoint}
		m.metrics[endpoint] = metric
	}
	metric.LastCall = time.Now()
	metric.LastLatency = latency
	metric.LastError = ""
	metric.SuccessCount++
	metric.TotalCalls++
	metric.TotalLatency += latency
}

// RecordError registra uma chamada de API com erro.
func (m *ApiMetrics) RecordError(endpoint string, latency time.Duration, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metric, ok := m.metrics[endpoint]
	if !ok {
		metric = &EndpointMetric{Endpoint: endpoint}
		m.metrics[endpoint] = metric
	}
	metric.LastCall = time.Now()
	metric.LastLatency = latency
	metric.LastError = errMsg
	metric.ErrorCount++
	metric.TotalCalls++
	metric.TotalLatency += latency
}

// GetSnapshot retorna uma cópia das métricas atuais.
func (m *ApiMetrics) GetSnapshot() map[string]EndpointMetric {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := make(map[string]EndpointMetric, len(m.metrics))
	for k, v := range m.metrics {
		metric := *v
		if metric.TotalCalls > 0 {
			avgLatency := metric.TotalLatency / time.Duration(metric.TotalCalls)
			_ = avgLatency // available for reporting
		}
		snapshot[k] = metric
	}
	return snapshot
}

// GetErrorRate retorna a taxa de erro (0.0 - 1.0) para um endpoint.
func (m *ApiMetrics) GetErrorRate(endpoint string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	metric, ok := m.metrics[endpoint]
	if !ok || metric.TotalCalls == 0 {
		return 0
	}
	return float64(metric.ErrorCount) / float64(metric.TotalCalls)
}

// Reset limpa todas as métricas.
func (m *ApiMetrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.metrics = make(map[string]*EndpointMetric)
	m.startedAt = time.Now()
}

// GetOverallStats retorna estatísticas agregadas.
func (m *ApiMetrics) GetOverallStats() (totalCalls, totalErrors int64, avgLatency time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, v := range m.metrics {
		totalCalls += v.TotalCalls
		totalErrors += v.ErrorCount
	}
	if totalCalls > 0 {
		var total time.Duration
		for _, v := range m.metrics {
			total += v.TotalLatency
		}
		avgLatency = total / time.Duration(totalCalls)
	}
	return
}
