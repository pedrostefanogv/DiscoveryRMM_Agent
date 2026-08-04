// Package apimetrics encapsula a coleta de métricas de latência e erro
// por endpoint para telemetria e diagnóstico da comunicação com a API.
package apimetrics

import (
	"sync"
	"time"
)

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

// Service coleta e agrega métricas de chamadas à API.
type Service struct {
	mu        sync.RWMutex
	metrics   map[string]*EndpointMetric
	startedAt time.Time
}

// New cria um coletor de métricas de API.
func New() *Service {
	return &Service{
		metrics:   make(map[string]*EndpointMetric),
		startedAt: time.Now(),
	}
}

// RecordCall registra uma chamada de API bem-sucedida.
func (s *Service) RecordCall(endpoint string, latency time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	metric, ok := s.metrics[endpoint]
	if !ok {
		metric = &EndpointMetric{Endpoint: endpoint}
		s.metrics[endpoint] = metric
	}
	metric.LastCall = time.Now()
	metric.LastLatency = latency
	metric.LastError = ""
	metric.SuccessCount++
	metric.TotalCalls++
	metric.TotalLatency += latency
}

// RecordError registra uma chamada de API com erro.
func (s *Service) RecordError(endpoint string, latency time.Duration, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	metric, ok := s.metrics[endpoint]
	if !ok {
		metric = &EndpointMetric{Endpoint: endpoint}
		s.metrics[endpoint] = metric
	}
	metric.LastCall = time.Now()
	metric.LastLatency = latency
	metric.LastError = errMsg
	metric.ErrorCount++
	metric.TotalCalls++
	metric.TotalLatency += latency
}

// GetSnapshot retorna uma cópia das métricas atuais.
func (s *Service) GetSnapshot() map[string]EndpointMetric {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := make(map[string]EndpointMetric, len(s.metrics))
	for k, v := range s.metrics {
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
func (s *Service) GetErrorRate(endpoint string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	metric, ok := s.metrics[endpoint]
	if !ok || metric.TotalCalls == 0 {
		return 0
	}
	return float64(metric.ErrorCount) / float64(metric.TotalCalls)
}

// Reset limpa todas as métricas.
func (s *Service) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.metrics = make(map[string]*EndpointMetric)
	s.startedAt = time.Now()
}

// GetOverallStats retorna estatísticas agregadas.
func (s *Service) GetOverallStats() (totalCalls, totalErrors int64, avgLatency time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, v := range s.metrics {
		totalCalls += v.TotalCalls
		totalErrors += v.ErrorCount
	}
	if totalCalls > 0 {
		var total time.Duration
		for _, v := range s.metrics {
			total += v.TotalLatency
		}
		avgLatency = total / time.Duration(totalCalls)
	}
	return
}
