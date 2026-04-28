// Package ai: streaming chat via SSE (Server-Sent Events).
package ai

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
)

// ─── Stream Types ──────────────────────────────────────────────────

type agentChatStreamEvent struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	SessionID string `json:"sessionId"`
	Error     string `json:"error"`
	LatencyMs int    `json:"latencyMs"`
}

// ─── Stream Call ───────────────────────────────────────────────────

func (s *Service) callAgentChatStream(
	ctx context.Context,
	cfg Config,
	message string,
	sessionID string,
	onToken func(string),
) (string, string, bool, error) {
	baseURL, err := normalizeAgentChatBaseURL(cfg.Endpoint)
	if err != nil {
		return "", "", false, err
	}

	requestBody := s.buildAgentChatRequest(message, sessionID, cfg.MaxTokens)
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return "", "", false, fmt.Errorf("falha ao serializar request de stream: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 130*time.Second)
	defer cancel()

	endpoint := baseURL + "/api/agent-auth/me/ai-chat/stream"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", "", false, fmt.Errorf("falha ao criar request de stream: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.APIKey))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", false, fmt.Errorf("falha ao chamar stream: %w", err)
	}
	defer resp.Body.Close()

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusUnauthorized {
			return "", "", false, fmt.Errorf("nao autorizado (401): verifique token do agente")
		}
		return "", "", false, fmt.Errorf("stream retornou status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(contentType, "text/event-stream") {
		return "", "", false, fmt.Errorf("resposta sem SSE (content-type: %s)", contentType)
	}

	return s.parseSSEStream(resp.Body, onToken)
}

func (s *Service) parseSSEStream(body io.Reader, onToken func(string)) (string, string, bool, error) {
	var contentBuf strings.Builder
	currentSessionID := ""
	hasToken := false

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}

		var evt agentChatStreamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}

		switch strings.TrimSpace(strings.ToLower(evt.Type)) {
		case "token":
			if evt.Content != "" {
				hasToken = true
				contentBuf.WriteString(evt.Content)
				if onToken != nil {
					onToken(evt.Content)
				}
			}
		case "done":
			if strings.TrimSpace(evt.SessionID) != "" {
				currentSessionID = strings.TrimSpace(evt.SessionID)
			}
			return contentBuf.String(), currentSessionID, hasToken, nil
		case "error":
			msg := strings.TrimSpace(evt.Error)
			if msg == "" {
				msg = "stream retornou erro"
			}
			return contentBuf.String(), currentSessionID, hasToken, fmt.Errorf("%s", msg)
		}
	}

	if err := scanner.Err(); err != nil {
		return contentBuf.String(), currentSessionID, hasToken, fmt.Errorf("erro ao ler stream: %w", err)
	}

	if currentSessionID != "" || contentBuf.Len() > 0 {
		return contentBuf.String(), currentSessionID, hasToken, nil
	}

	return "", "", hasToken, fmt.Errorf("stream encerrado sem evento final")
}

// ─── SendStream ────────────────────────────────────────────────────

// SendStream is like Send but streams the final text response token-by-token via onToken.
// Tool-call intermediate rounds are executed silently; onStatus receives progress updates.
func (s *Service) SendStream(ctx context.Context, userMessage string, onToken func(string), onStatus func(string)) (string, error) {
	streamCtx, streamCancel := context.WithCancel(ctx)
	streamID := s.registerStreamCancel(streamCancel)
	defer func() {
		s.unregisterStreamCancel(streamID)
		streamCancel()
	}()

	s.mu.Lock()
	cfg := s.cfg
	sessionID := s.sessionID
	s.mu.Unlock()
	s.logf("stream: mensagem recebida (%d chars)", len(strings.TrimSpace(userMessage)))

	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return "", fmt.Errorf("configuracao de IA incompleta: defina endpoint e token de agente")
	}
	if err := validateChatMessage(userMessage); err != nil {
		return "", err
	}

	s.mu.Lock()
	s.history = append(s.history, Message{Role: "user", Content: userMessage})
	s.mu.Unlock()

	if onStatus != nil {
		onStatus("Conectando ao servidor...")
	}

	content, streamSessionID, hasToken, err := s.callAgentChatStream(streamCtx, cfg, userMessage, sessionID, onToken)
	if err != nil {
		if streamCtx.Err() != nil {
			return "", streamCtx.Err()
		}
		s.logf("stream: falha (%v), fallback para endpoint sincrono", err)
		if onStatus != nil {
			onStatus("Alternando para resposta padrao...")
		}
		syncResp, syncErr := s.callAgentChatSync(streamCtx, cfg, userMessage, sessionID)
		if syncErr != nil {
			if hasToken && strings.TrimSpace(content) != "" {
				s.mu.Lock()
				s.history = append(s.history, Message{Role: "assistant", Content: content})
				s.mu.Unlock()
				return content, nil
			}
			return "", syncErr
		}
		assistant := strings.TrimSpace(syncResp.AssistantMessage)
		if assistant == "" {
			assistant = "(sem resposta)"
		}
		if onToken != nil {
			onToken(assistant)
		}
		s.mu.Lock()
		if strings.TrimSpace(syncResp.SessionID) != "" {
			s.sessionID = strings.TrimSpace(syncResp.SessionID)
		}
		s.history = append(s.history, Message{Role: "assistant", Content: assistant})
		s.mu.Unlock()
		return assistant, nil
	}

	assistant := strings.TrimSpace(content)
	if assistant == "" {
		assistant = "(sem resposta)"
	}

	s.mu.Lock()
	if strings.TrimSpace(streamSessionID) != "" {
		s.sessionID = strings.TrimSpace(streamSessionID)
	}
	s.history = append(s.history, Message{Role: "assistant", Content: assistant})
	s.mu.Unlock()

	return assistant, nil
}
