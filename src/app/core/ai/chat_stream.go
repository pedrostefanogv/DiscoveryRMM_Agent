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

	"discovery/app/netutil"
	"discovery/app/core/tlsutil"
)

// ─── Stream Types ──────────────────────────────────────────────────

type agentChatStreamEvent struct {
	Type          string          `json:"type"`
	Content       string          `json:"content"`
	SessionID     string          `json:"sessionId"`
	Error         string          `json:"error"`
	LatencyMs     int             `json:"latencyMs"`
	TokensUsed    int             `json:"tokensUsed"`
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	ToolArguments json.RawMessage `json:"toolArguments"`
	// ToolArgumentsDelta captura o nome de campo usado pelo servidor C#.
	// O servidor serializa AiChatStreamChunk.ToolArgumentsDelta como "toolArgumentsDelta"
	// (camelCase), enquanto o campo canônico acima é "toolArguments".
	// Este alias garante compatibilidade com ambas as convenções.
	ToolArgumentsDelta json.RawMessage `json:"toolArgumentsDelta"`
}

// toolArgsString normaliza toolArguments (pode ser string JSON ou objeto JSON).
func toolArgsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	// Desempacota string JSON duplamente codificada (ex: "\"{\\\"query\\\": \\\"Foxit\\\"}\"")
	if s[0] == '"' && len(s) > 1 {
		var unescaped string
		if err := json.Unmarshal(raw, &unescaped); err == nil && unescaped != "" {
			return unescaped
		}
	}
	return s
}

// effectiveToolArgs retorna os argumentos de tool do evento, priorizando toolArguments
// e usando toolArgumentsDelta como fallback (compatibilidade com servidor C#).
func (evt agentChatStreamEvent) effectiveToolArgs() string {
	if len(evt.ToolArguments) > 0 {
		return toolArgsString(evt.ToolArguments)
	}
	if len(evt.ToolArgumentsDelta) > 0 {
		return toolArgsString(evt.ToolArgumentsDelta)
	}
	return ""
}

// toolResultItem sent back to the server in rounds 2+.
type toolResultItem struct {
	CallID string `json:"callId"`
	Name   string `json:"name"`
	Result string `json:"result"`
}

// agentStreamRequest is the JSON body sent to /me/ai-chat/stream.
type agentStreamRequest struct {
	Message     string           `json:"message,omitempty"`
	SessionID   string           `json:"sessionId,omitempty"`
	ToolResults []toolResultItem `json:"toolResults,omitempty"`
	MaxTokens   int              `json:"maxTokens,omitempty"`
	Tools       []map[string]any `json:"tools,omitempty"`
}

// pendingToolCall collects a tool_call event before execution.
type pendingToolCall struct {
	CallID string
	Name   string
	Args   string
}

// ─── Stream Call ───────────────────────────────────────────────────

func (s *Service) callAgentChatStream(
	ctx context.Context,
	cfg Config,
	message string,
	sessionID string,
	onToken func(string),
) (string, string, bool, error) {
	startTime := time.Now()
	baseURL, err := normalizeAgentChatBaseURL(cfg.Endpoint)
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:    "chat_request",
			Method:  "stream",
			Error:   err.Error(),
			UserMsg: TruncateForLog(message, 2000),
		})
		return "", "", false, err
	}

	requestBody := s.buildAgentChatRequest(message, sessionID, cfg.MaxTokens)
	payload, err := json.Marshal(requestBody)
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:    "chat_request",
			Method:  "stream",
			Error:   fmt.Sprintf("marshal error: %v", err),
			UserMsg: TruncateForLog(message, 2000),
		})
		return "", "", false, fmt.Errorf("falha ao serializar request de stream: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 130*time.Second)
	defer cancel()

	endpoint := baseURL + "/api/v1/agent-auth/me/ai-chat/stream"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:       "chat_request",
			Method:     "stream",
			Endpoint:   endpoint,
			MessageLen: len(message),
			SessionID:  sessionID,
			Error:      fmt.Sprintf("request create error: %v", err),
		})
		return "", "", false, fmt.Errorf("falha ao criar request de stream: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.APIKey, cfg.AgentID); err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:       "chat_request",
			Method:     "stream",
			Endpoint:   endpoint,
			MessageLen: len(message),
			SessionID:  sessionID,
			Error:      fmt.Sprintf("auth header error: %v", err),
		})
		return "", "", false, err
	}

	resp, err := tlsutil.NewHTTPClient(130 * time.Second).Do(req)
	if err != nil {
		elapsed := time.Since(startTime)
		s.logChatEntry(ChatLogEntry{
			Type:       "chat_response",
			Method:     "stream",
			Endpoint:   endpoint,
			MessageLen: len(message),
			SessionID:  sessionID,
			Error:      fmt.Sprintf("request failed: %v", err),
			LatencyMs:  int(elapsed.Milliseconds()),
		})
		return "", "", false, fmt.Errorf("falha ao chamar stream: %w", err)
	}
	defer resp.Body.Close()

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		elapsed := time.Since(startTime)
		s.logChatEntry(ChatLogEntry{
			Type:       "chat_response",
			Method:     "stream",
			Endpoint:   endpoint,
			MessageLen: len(message),
			SessionID:  sessionID,
			StatusCode: resp.StatusCode,
			Error:      strings.TrimSpace(string(body)),
			LatencyMs:  int(elapsed.Milliseconds()),
		})
		if resp.StatusCode == http.StatusUnauthorized {
			return "", "", false, fmt.Errorf("nao autorizado (401): verifique token do agente")
		}
		return "", "", false, fmt.Errorf("stream retornou status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(contentType, "text/event-stream") {
		elapsed := time.Since(startTime)
		s.logChatEntry(ChatLogEntry{
			Type:       "chat_response",
			Method:     "stream",
			Endpoint:   endpoint,
			MessageLen: len(message),
			SessionID:  sessionID,
			StatusCode: resp.StatusCode,
			Error:      fmt.Sprintf("content-type inesperado: %s", contentType),
			LatencyMs:  int(elapsed.Milliseconds()),
		})
		return "", "", false, fmt.Errorf("resposta sem SSE (content-type: %s)", contentType)
	}

	content, streamSessionID, hasToken, err := s.parseSSEStream(resp.Body, onToken)
	elapsed := time.Since(startTime)

	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:        "chat_response",
			Method:      "stream",
			Endpoint:    endpoint,
			MessageLen:  len(message),
			SessionID:   streamSessionID,
			StatusCode:  resp.StatusCode,
			Error:       err.Error(),
			LatencyMs:   int(elapsed.Milliseconds()),
			ResponseLen: len(content),
			HasTokens:   hasToken,
		})
		return content, streamSessionID, hasToken, err
	}

	s.logChatEntry(ChatLogEntry{
		Type:        "chat_response",
		Method:      "stream",
		Endpoint:    endpoint,
		MessageLen:  len(message),
		SessionID:   streamSessionID,
		StatusCode:  resp.StatusCode,
		LatencyMs:   int(elapsed.Milliseconds()),
		ResponseLen: len(content),
		HasTokens:   hasToken,
		StreamDone:  true,
		UserMsg:     TruncateForLog(message, 2000),
		Assistant:   TruncateForLog(content, 4000),
	})
	return content, streamSessionID, hasToken, nil
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
		err := fmt.Errorf("configuracao de IA incompleta: defina endpoint e token de agente")
		s.logChatEntry(ChatLogEntry{
			Type:    "chat_request",
			Method:  "stream",
			Error:   err.Error(),
			UserMsg: TruncateForLog(userMessage, 2000),
		})
		return "", err
	}
	if err := validateChatMessage(userMessage); err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:    "chat_request",
			Method:  "stream",
			Error:   err.Error(),
			UserMsg: TruncateForLog(userMessage, 2000),
		})
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
