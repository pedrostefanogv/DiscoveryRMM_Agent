// Package agentconfig encapsula os tipos e a lógica pura de configuração
// do agente (parsing, merge hierárquico e normalização), separados do App.
package agentconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"discovery/app/core/tlsutil"
	"discovery/app/debug"
	"discovery/app/netutil"
)

// ConfigurationEndpoint é o endpoint da API que retorna a configuração do agente.
const ConfigurationEndpoint = "/api/v1/agent-auth/me/configuration"

// FetchResult representa o resultado de um fetch de configuração.
type FetchResult struct {
	// RawBody é o corpo bruto da resposta (para cache).
	RawBody []byte
	// Config é a configuração parseada e normalizada.
	Config AgentConfiguration
	// ZeroTouchPending indica se o dispositivo aguarda aprovação de TI.
	ZeroTouchPending bool
	// HasZeroTouchPendingFlag indica se o campo zeroTouchPending estava presente.
	HasZeroTouchPendingFlag bool
}

// FetchDeps são as dependências injetadas no Service.
type FetchDeps struct {
	// GetDebugConfig retorna a configuração de conexão do agente.
	GetDebugConfig func() debug.Config
}

// Service encapsula o fetch da configuração do agente via API.
type Service struct {
	getDebugConfig func() debug.Config
}

// New cria um Service de configuração.
func New(deps FetchDeps) *Service {
	return &Service{getDebugConfig: deps.GetDebugConfig}
}

// Fetch busca a configuração do agente na API e a parseia.
// Retorna o corpo bruto (para cache) e a configuração normalizada.
func (s *Service) Fetch(ctx context.Context) (*FetchResult, error) {
	cfg := s.getDebugConfig()
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.APIScheme()))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	if apiServer == "" || token == "" {
		return nil, fmt.Errorf("configuração de servidor API incompleta")
	}
	if apiScheme != "http" && apiScheme != "https" {
		return nil, fmt.Errorf("apiScheme inválido")
	}

	target := apiScheme + "://" + apiServer + ConfigurationEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, cfg.AgentID); err != nil {
		return nil, err
	}

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("configuration retornou HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	cfgParsed, err := ParseAgentConfiguration(body)
	if err != nil {
		return nil, err
	}

	result := &FetchResult{
		RawBody: body,
		Config:  cfgParsed,
	}
	result.ZeroTouchPending, result.HasZeroTouchPendingFlag = ParseZeroTouchPending(body)
	return result, nil
}

// ParseZeroTouchPending extrai o flag zeroTouchPending do corpo da resposta.
func ParseZeroTouchPending(body []byte) (pending bool, hasFlag bool) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return false, false
	}
	v, ok := raw["zeroTouchPending"]
	if !ok {
		return false, false
	}
	return ToBool(v), true
}
