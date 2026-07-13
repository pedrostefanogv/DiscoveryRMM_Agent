package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"discovery/app/netutil"
	"discovery/internal/tlsutil"
)

// ── Chat AI Bridge (API v1) ────────────────────────────────────────────────
//
// Implementa os novos endpoints de chat AI da API v1:
//   POST /api/v1/agent-auth/me/ai-chat         — síncrono
//   POST /api/v1/agent-auth/me/ai-chat/async   — assíncrono (job polling)
//   POST /api/v1/agent-auth/me/ai-chat/stream  — streaming via SSE
//   GET  /api/v1/agent-auth/me/ai-chat/jobs/{jobId} — status do job assíncrono

// ChatRequest é o payload comum para os endpoints de chat AI.
type ChatAIRequest struct {
	Message      string `json:"message"`
	SessionID    string `json:"sessionId"`
	MaxTokens    int    `json:"maxTokens"`
	DepartmentID string `json:"departmentId"`
	ClientIP     string `json:"clientIp,omitempty"`
}

// ChatAIResponse é a resposta do endpoint síncrono.
type ChatAIResponse struct {
	Message   string `json:"message"`
	SessionID string `json:"sessionId"`
}

// ChatJobStatus é a resposta de polling do job assíncrono.
type ChatJobStatus struct {
	JobID     string `json:"jobId"`
	Status    string `json:"status"` // "pending", "running", "completed", "failed"
	Message   string `json:"message,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SendChatSync envia mensagem de chat síncrona via API v1.
func (a *App) SendChatSync(ctx context.Context, message, sessionID string, maxTokens int) (*ChatAIResponse, error) {
	cfg := a.GetDebugConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	token := strings.TrimSpace(cfg.AuthToken)

	reqBody := ChatAIRequest{
		Message:   message,
		SessionID: sessionID,
		MaxTokens: maxTokens,
	}
	payload, _ := json.Marshal(reqBody)

	endpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/ai-chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := tlsutil.NewHTTPClient(120 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("chat sync: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("chat sync HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result ChatAIResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("chat sync parse: %w", err)
	}
	return &result, nil
}

// SendChatAsync envia mensagem de chat assíncrona e retorna o jobID para polling.
func (a *App) SendChatAsync(ctx context.Context, message, sessionID string, maxTokens int) (string, error) {
	cfg := a.GetDebugConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	token := strings.TrimSpace(cfg.AuthToken)

	reqBody := ChatAIRequest{
		Message:   message,
		SessionID: sessionID,
		MaxTokens: maxTokens,
	}
	payload, _ := json.Marshal(reqBody)

	endpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/ai-chat/async"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return "", fmt.Errorf("chat async: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("chat async HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var job ChatJobStatus
	if err := json.Unmarshal(body, &job); err != nil {
		return "", fmt.Errorf("chat async parse: %w", err)
	}
	return job.JobID, nil
}

// PollChatJob consulta o status de um job assíncrono de chat.
func (a *App) PollChatJob(ctx context.Context, jobID string) (*ChatJobStatus, error) {
	cfg := a.GetDebugConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	token := strings.TrimSpace(cfg.AuthToken)

	endpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/ai-chat/jobs/" + jobID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return nil, err
	}

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("chat job poll: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("chat job poll HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var job ChatJobStatus
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, fmt.Errorf("chat job poll parse: %w", err)
	}
	return &job, nil
}

// SendChatStream envia mensagem de chat via streaming SSE e chama o callback para cada token.
func (a *App) SendChatStream(ctx context.Context, message, sessionID string, maxTokens int, onToken func(string), onDone func()) error {
	cfg := a.GetDebugConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	token := strings.TrimSpace(cfg.AuthToken)

	reqBody := ChatAIRequest{
		Message:   message,
		SessionID: sessionID,
		MaxTokens: maxTokens,
	}
	payload, _ := json.Marshal(reqBody)

	endpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/ai-chat/stream"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := tlsutil.NewHTTPClient(130 * time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("chat stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return fmt.Errorf("chat stream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				if onDone != nil {
					onDone()
				}
				return nil
			}
			if onToken != nil {
				onToken(data)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("chat stream read: %w", err)
	}
	if onDone != nil {
		onDone()
	}
	return nil
}
