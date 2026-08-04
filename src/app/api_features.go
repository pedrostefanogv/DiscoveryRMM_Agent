package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"discovery/app/netutil"
	"discovery/app/core/tlsutil"
)

// ── API Feature Detection ─────────────────────────────────────────────────

// ApiVersionInfo contém informações da versão e capacidades da API.
type ApiVersionInfo struct {
	Detected  bool
	Version   string
	Features  []string
	BaseURL   string
	CheckedAt string
}

// DetectApiFeatures testa a conectividade com a API e detecta quais features estão disponíveis.
// Faz um GET para /me/configuration e testa endpoints opcionais.
func (a *App) DetectApiFeatures(ctx context.Context) *ApiVersionInfo {
	info := &ApiVersionInfo{
		Features:  make([]string, 0),
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}

	cfg := a.GetDebugConfig()
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)

	if apiServer == "" || token == "" || agentID == "" {
		a.logs.append("[feature-detect] credenciais insuficientes; ignorando deteccao de API")
		return info
	}

	info.BaseURL = apiScheme + "://" + apiServer
	a.logs.append(fmt.Sprintf("[feature-detect] iniciando deteccao em %s", info.BaseURL))

	// Test 1: GET /me/configuration (sempre obrigatório)
	if err := a.checkAPIEndpoint(ctx, "GET", "/api/v1/agent-auth/me/configuration", nil); err != nil {
		a.logs.append(fmt.Sprintf("[feature-detect] endpoint base falhou: %v", err))
		return info
	}
	info.Detected = true
	info.Features = append(info.Features, "configuration")

	// Test 2: Detect hierarchical config (v1) vs flat config (legacy)
	body, err := a.fetchAPIBody(ctx, "GET", "/api/v1/agent-auth/me/configuration")
	if err == nil {
		var raw map[string]interface{}
		if json.Unmarshal(body, &raw) == nil {
			if _, hasServer := raw["server"]; hasServer {
				info.Version = "v1"
				info.Features = append(info.Features, "config-hierarchical")
				a.logs.append("[feature-detect] detectada configuração hierárquica API v1")
			} else {
				info.Version = "legacy"
				info.Features = append(info.Features, "config-flat")
				a.logs.append("[feature-detect] detectada configuração flat legada")
			}
		}
	}

	// Test 3: REST commands endpoint
	if err := a.checkAPIEndpoint(ctx, "GET", "/api/v1/agent-auth/me/commands?limit=1", nil); err == nil {
		info.Features = append(info.Features, "rest-commands")
		a.logs.append("[feature-detect] endpoint REST commands disponivel")
	}

	// Test 4: Tickets endpoint
	if err := a.checkAPIEndpoint(ctx, "GET", "/api/v1/agent-auth/me/tickets?limit=1", nil); err == nil {
		info.Features = append(info.Features, "tickets")
		a.logs.append("[feature-detect] endpoint tickets disponivel")
	}

	// Test 5: Custom fields collected
	if err := a.checkAPIEndpoint(ctx, "GET", "/api/v1/agent-auth/me/custom-fields/collected", nil); err == nil {
		info.Features = append(info.Features, "custom-fields-collected")
		a.logs.append("[feature-detect] endpoint custom-fields collected disponivel")
	}

	a.logs.append(fmt.Sprintf("[feature-detect] deteccao concluida: version=%s features=%v", info.Version, info.Features))
	return info
}

// checkAPIEndpoint verifica se um endpoint responde com status 2xx.
func (a *App) checkAPIEndpoint(ctx context.Context, method, path string, body io.Reader) error {
	cfg := a.GetDebugConfig()
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)

	endpoint := apiScheme + "://" + apiServer + path

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, endpoint, body)
	if err != nil {
		return err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return err
	}

	resp, err := tlsutil.NewHTTPClient(5 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// fetchAPIBody faz um GET e retorna o body.
func (a *App) fetchAPIBody(ctx context.Context, method, path string) ([]byte, error) {
	cfg := a.GetDebugConfig()
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)

	endpoint := apiScheme + "://" + apiServer + path

	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return nil, err
	}

	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}
