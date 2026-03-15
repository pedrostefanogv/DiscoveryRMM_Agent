package automation

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
)

type Client struct {
	httpClient *http.Client
}

func NewClient(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{httpClient: &http.Client{Timeout: timeout}}
}

func normalizeBaseURL(endpoint string) (string, error) {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return "", fmt.Errorf("endpoint da automacao nao informado")
	}
	u, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(u.Scheme) == "" || strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("endpoint da automacao invalido")
	}
	path := strings.TrimSpace(u.Path)
	if path == "" || path == "/" {
		u.Path = ""
		u.RawQuery = ""
		u.Fragment = ""
		return strings.TrimRight(u.String(), "/"), nil
	}
	idx := strings.Index(path, "/api/agent-auth")
	if idx >= 0 {
		u.Path = path[:idx]
		u.RawQuery = ""
		u.Fragment = ""
		return strings.TrimRight(u.String(), "/"), nil
	}
	if strings.Contains(path, "/api/") {
		return "", fmt.Errorf("endpoint deve apontar para a base do servidor ou /api/agent-auth")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (c *Client) SyncPolicy(ctx context.Context, cfg RuntimeConfig, reqBody PolicySyncRequest, correlationID string) (*PolicySyncResponse, error) {
	baseURL, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("falha ao serializar policy sync: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	endpoint := baseURL + "/api/agent-auth/me/automation/policy-sync"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("falha ao criar request de policy sync: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.Token))
	req.Header.Set("X-Agent-Token", strings.TrimSpace(cfg.Token))
	if strings.TrimSpace(correlationID) != "" {
		req.Header.Set("X-Correlation-Id", strings.TrimSpace(correlationID))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao chamar policy sync: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("falha ao ler resposta de policy sync: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("nao autorizado (401): verifique token do agente")
		}
		return nil, fmt.Errorf("policy sync retornou status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result PolicySyncResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("falha ao decodificar resposta de policy sync: %w", err)
	}
	if result.TaskCount == 0 && len(result.Tasks) > 0 {
		result.TaskCount = len(result.Tasks)
	}
	return &result, nil
}

func (c *Client) AckExecution(ctx context.Context, cfg RuntimeConfig, commandID string, payload AckRequest, correlationID string) error {
	return c.postExecutionCallback(ctx, cfg, commandID, "ack", payload, correlationID)
}

func (c *Client) ReportExecutionResult(ctx context.Context, cfg RuntimeConfig, commandID string, payload ResultRequest, correlationID string) error {
	return c.postExecutionCallback(ctx, cfg, commandID, "result", payload, correlationID)
}

func (c *Client) postExecutionCallback(ctx context.Context, cfg RuntimeConfig, commandID, suffix string, payload any, correlationID string) error {
	baseURL, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return err
	}
	commandID = strings.TrimSpace(commandID)
	if commandID == "" {
		return fmt.Errorf("commandId obrigatorio para callback de execucao")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("falha ao serializar callback %s: %w", suffix, err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	endpoint := baseURL + "/api/agent-auth/me/automation/executions/" + url.PathEscape(commandID) + "/" + suffix
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("falha ao criar callback %s: %w", suffix, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.Token))
	req.Header.Set("X-Agent-Token", strings.TrimSpace(cfg.Token))
	if strings.TrimSpace(correlationID) != "" {
		req.Header.Set("X-Correlation-Id", strings.TrimSpace(correlationID))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("falha ao enviar callback %s: %w", suffix, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("callback %s retornou status %d: %s", suffix, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}
