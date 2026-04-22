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

// setAutomationHeaders aplica os headers obrigatórios em requests de automação:
// Authorization Bearer, X-Agent-ID e, quando disponível, X-Correlation-Id.
// Mantém X-Agent-Token por compatibilidade com versões antigas do backend.
func setAutomationHeaders(req *http.Request, cfg RuntimeConfig, correlationID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.Token))
	req.Header.Set("X-Agent-Token", strings.TrimSpace(cfg.Token))
	if id := strings.TrimSpace(cfg.AgentID); id != "" {
		req.Header.Set("X-Agent-ID", id)
	}
	if cid := strings.TrimSpace(correlationID); cid != "" {
		req.Header.Set("X-Correlation-Id", cid)
	}
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
	setAutomationHeaders(req, cfg, correlationID)

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
	setAutomationHeaders(req, cfg, correlationID)

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

// GetRuntimeCustomFields consulta os custom fields disponíveis para uma execução.
// taskID e scriptID são opcionais; quando informados, o backend filtra por contexto.
func (c *Client) GetRuntimeCustomFields(ctx context.Context, cfg RuntimeConfig, taskID, scriptID, correlationID string) ([]RuntimeCustomField, error) {
	baseURL, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	endpoint := baseURL + "/api/agent-auth/me/custom-fields/runtime"
	if taskID != "" || scriptID != "" {
		q := url.Values{}
		if taskID != "" {
			q.Set("taskId", taskID)
		}
		if scriptID != "" {
			q.Set("scriptId", scriptID)
		}
		endpoint += "?" + q.Encode()
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar request de runtime custom fields: %w", err)
	}
	setAutomationHeaders(req, cfg, correlationID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao consultar runtime custom fields: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("falha ao ler resposta de runtime custom fields: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("nao autorizado (401): verifique token do agente")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("runtime custom fields retornou status %d", resp.StatusCode)
	}

	var result []RuntimeCustomField
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("falha ao decodificar runtime custom fields: %w", err)
	}
	return result, nil
}

// CollectCustomFieldValue registra um valor coletado durante uma execução.
// Retorna ErrCustomFieldWrite para erros de negócio (HTTP 400) não-retentáveis.
func (c *Client) CollectCustomFieldValue(ctx context.Context, cfg RuntimeConfig, req CollectedValueRequest, correlationID string) (*CollectedValueResponse, error) {
	baseURL, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao serializar collected value: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	endpoint := baseURL + "/api/agent-auth/me/custom-fields/collected"
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("falha ao criar request de collected value: %w", err)
	}
	setAutomationHeaders(httpReq, cfg, correlationID)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("falha ao enviar collected value: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("falha ao ler resposta de collected value: %w", err)
	}

	if resp.StatusCode == http.StatusBadRequest {
		return nil, parseCustomFieldWriteError(body)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("nao autorizado (401): verifique token do agente")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("collected value retornou status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result CollectedValueResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("falha ao decodificar resposta de collected value: %w", err)
	}
	return &result, nil
}

// parseCustomFieldWriteError converte um corpo 400 em ErrCustomFieldWrite classificado.
func parseCustomFieldWriteError(body []byte) *ErrCustomFieldWrite {
	var envelope struct {
		Code    *int   `json:"code"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)

	msg := strings.TrimSpace(envelope.Message)
	if msg == "" {
		msg = strings.TrimSpace(envelope.Error)
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}

	code := WriteErrorUnknown
	if envelope.Code != nil {
		switch *envelope.Code {
		case 1:
			code = WriteErrorNotAllowed
		case 2:
			code = WriteErrorContextDenied
		case 3:
			code = WriteErrorNotFound
		case 4:
			code = WriteErrorInactive
		case 5:
			code = WriteErrorScopeRestriction
		}
	} else {
		lower := strings.ToLower(msg)
		switch {
		case strings.Contains(lower, "not allowed") || strings.Contains(lower, "allow") && strings.Contains(lower, "write"):
			code = WriteErrorNotAllowed
		case strings.Contains(lower, "context") || strings.Contains(lower, "authorized"):
			code = WriteErrorContextDenied
		case strings.Contains(lower, "not found"):
			code = WriteErrorNotFound
		case strings.Contains(lower, "inactive") || strings.Contains(lower, "inativa"):
			code = WriteErrorInactive
		case strings.Contains(lower, "scope"):
			code = WriteErrorScopeRestriction
		}
	}
	return &ErrCustomFieldWrite{Code: code, Message: msg}
}
