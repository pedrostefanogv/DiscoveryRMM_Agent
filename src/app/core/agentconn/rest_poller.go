package agentconn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"discovery/app/netutil"
	"discovery/internal/tlsutil"
)

// RestCommandPoller implementa polling REST para comandos via GET /api/v1/agent-auth/me/commands
// como fallback quando NATS está indisponível.
type RestCommandPoller struct {
	httpClient *http.Client
	config     RestPollerConfig
}

// RestPollerConfig contém a configuração do poller REST.
type RestPollerConfig struct {
	BaseURL      string
	Token        string
	AgentID      string
	Limit        int
	PollInterval time.Duration
}

// RestCommand representa um comando recebido via REST polling.
type RestCommand struct {
	CommandID     string          `json:"commandId"`
	CommandType   string          `json:"commandType"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     string          `json:"createdAt"`
	ExpiresAt     string          `json:"expiresAt"`
	CorrelationID string          `json:"correlationId"`
}

// RestCommandsResponse é a resposta de GET /me/commands.
type RestCommandsResponse struct {
	Commands []RestCommand `json:"commands"`
	Total    int           `json:"total"`
	HasMore  bool          `json:"hasMore"`
}

// NewRestCommandPoller cria um novo poller REST.
func NewRestCommandPoller(cfg RestPollerConfig) *RestCommandPoller {
	if cfg.Limit <= 0 {
		cfg.Limit = 50
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	return &RestCommandPoller{
		httpClient: tlsutil.NewHTTPClient(20 * time.Second),
		config:     cfg,
	}
}

// PollCommands busca comandos pendentes via REST.
func (p *RestCommandPoller) PollCommands(ctx context.Context) ([]RestCommand, error) {
	baseURL := strings.TrimRight(p.config.BaseURL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("baseURL não configurada para REST command poller")
	}

	endpoint := baseURL + "/api/v1/agent-auth/me/commands"
	if p.config.Limit > 0 {
		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", p.config.Limit))
		endpoint += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("rest poller: falha ao criar request: %w", err)
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, p.config.Token, p.config.AgentID); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rest poller: falha HTTP: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("rest poller: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result RestCommandsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("rest poller: falha ao decodificar: %w", err)
	}
	return result.Commands, nil
}

// AckCommand confirma recebimento de um comando via REST.
func (p *RestCommandPoller) AckCommand(ctx context.Context, commandID string) error {
	baseURL := strings.TrimRight(p.config.BaseURL, "/")
	endpoint := baseURL + "/api/v1/agent-auth/me/automation/executions/" + url.PathEscape(commandID) + "/ack"

	payload, _ := json.Marshal(map[string]interface{}{
		"agentId":       p.config.AgentID,
		"commandId":     commandID,
		"request":       map[string]interface{}{},
		"correlationId": "",
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, p.config.Token, p.config.AgentID); err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ack command HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
