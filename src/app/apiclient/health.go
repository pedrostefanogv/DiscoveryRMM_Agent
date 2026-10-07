package apiclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"discovery/app/core/tlsutil"
	"discovery/app/netutil"
)

// apiHealthFailureThreshold é o nº de falhas CONSECUTIVAS do probe antes de
// considerar a API inacessível — um timeout isolado não derruba o indicador.
const apiHealthFailureThreshold = 2

// healthState guarda o estado de saúde da API HTTP. Vive no Service (não no
// app) para poder ser consultado por qualquer bridge de API.
type healthState struct {
	mu         sync.Mutex
	reachable  bool
	downSince  time.Time
	lastOK     time.Time
	failStreak int
}

// APIReachable informa se a API respondeu dentro da janela de probes.
// É otimista no boot (true) até a primeira evidência de queda — sem isso o
// agent apareceria offline antes do primeiro health-check.
func (s *Service) APIReachable() bool {
	if s == nil {
		return true
	}
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	return s.health.reachable
}

// APIHealth devolve (reachable, downSince, failStreak) para diagnóstico.
func (s *Service) APIHealth() (bool, time.Time, int) {
	if s == nil {
		return true, time.Time{}, 0
	}
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	return s.health.reachable, s.health.downSince, s.health.failStreak
}

// ProbeAPI faz um GET curto e autenticado (/me/commands?limit=1) e atualiza o
// estado de saúde da API. Sem credenciais configuradas não altera o estado —
// ausência de configuração não é "queda".
//
// Só conta como queda falha de transporte ou HTTP 5xx. 4xx (401/403/404) é o
// servidor RESPONDENDO (problema de credencial/rota é outro diagnóstico) — não
// pode derrubar o agent para offline.
func (s *Service) ProbeAPI(ctx context.Context) error {
	if s == nil || s.getDebugConfig == nil {
		return nil
	}
	cfg := s.getDebugConfig()
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" || strings.TrimSpace(cfg.AgentID) == "" {
		return nil
	}
	status, err := s.probeAPIStatus(ctx, cfg)
	if err != nil {
		s.recordAPIProbe(err)
		return err
	}
	if status >= 500 {
		wrapped := fmt.Errorf("HTTP %d", status)
		s.recordAPIProbe(wrapped)
		return wrapped
	}
	s.recordAPIProbe(nil)
	return nil
}

// probeAPIStatus devolve o status HTTP do endpoint de health-check.
func (s *Service) probeAPIStatus(ctx context.Context, cfg DebugConfig) (int, error) {
	scheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	endpoint := scheme + "://" + strings.TrimSpace(cfg.ApiServer) + "/api/v1/agent-auth/me/commands?limit=1"

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return 0, err
	}
	resp, err := tlsutil.NewHTTPClient(5 * time.Second).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	return resp.StatusCode, nil
}

func (s *Service) recordAPIProbe(err error) {
	s.health.mu.Lock()
	defer s.health.mu.Unlock()
	if err != nil {
		s.health.failStreak++
		if !s.health.reachable {
			return
		}
		if s.health.failStreak >= apiHealthFailureThreshold {
			s.health.reachable = false
			s.health.downSince = time.Now().UTC()
		}
		return
	}
	s.health.failStreak = 0
	s.health.reachable = true
	s.health.downSince = time.Time{}
	s.health.lastOK = time.Now().UTC()
}
