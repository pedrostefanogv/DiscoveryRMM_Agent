// Package ai: multi-round agent loop — Server-Managed Agent Loop client side.
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
	"discovery/internal/tlsutil"
)

func (s *Service) SendStreamMultiRound(
	ctx context.Context,
	userMessage string,
	onToken func(string),
	onStatus func(string),
	mcpExecutor func(ctx context.Context, toolName, argsJSON string) (string, error),
) (string, error) {
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	s.mu.Lock()
	cfg := s.cfg
	sessionID := s.sessionID
	s.mu.Unlock()

	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return "", fmt.Errorf("configuracao de IA incompleta")
	}
	if err := validateChatMessage(userMessage); err != nil {
		return "", err
	}

	s.mu.Lock()
	s.history = append(s.history, Message{Role: "user", Content: userMessage})
	s.mu.Unlock()

	pendingCalls := make([]pendingToolCall, 0)
	req := agentStreamRequest{Message: userMessage, SessionID: sessionID}
	// Injetar tools MCP no primeiro round (round 0).
	s.mu.RLock()
	if s.registry != nil {
		tools := s.registry.OpenAIFunctions()
		if len(tools) > 0 {
			req.Tools = tools
			s.logf(fmt.Sprintf("[chat] %d tools enviadas para o servidor", len(tools)))
		} else {
			s.logf("[chat] aviso: nenhuma tool MCP registrada — chat funcionara sem function calling")
		}
	} else {
		s.logf("[chat] aviso: registry MCP nao disponivel")
	}
	s.mu.RUnlock()
	var currentSessionID string
	var err error

	for round := 0; round < 5; round++ {
		if onStatus != nil {
			if round == 0 {
				onStatus("Conectando ao servidor...")
			} else {
				onStatus(fmt.Sprintf("Round %d — processando tools...", round+1))
			}
		}
		currentSessionID, err = s.executeRound(streamCtx, cfg, req, onToken, &pendingCalls)
		if err != nil {
			return s.fallbackToSync(streamCtx, cfg, userMessage, sessionID, onToken)
		}
		if len(pendingCalls) == 0 {
			s.logf(fmt.Sprintf("[chat] round %d: LLM respondeu sem tool_call (resposta direta)", round))
			break
		}
		if onStatus != nil {
			onStatus(fmt.Sprintf("Executando %d ferramenta(s)...", len(pendingCalls)))
		}
		var toolResults []toolResultItem
		for _, tc := range pendingCalls {
			if mcpExecutor == nil {
				toolResults = append(toolResults, toolResultItem{CallID: tc.CallID, Name: tc.Name, Result: `{"error":"MCP indisponivel"}`})
				continue
			}
			result, execErr := mcpExecutor(streamCtx, tc.Name, tc.Args)
			if execErr != nil {
				result = fmt.Sprintf(`{"error":"%s"}`, strings.ReplaceAll(execErr.Error(), `"`, `'`))
			}
			toolResults = append(toolResults, toolResultItem{CallID: tc.CallID, Name: tc.Name, Result: result})
		}
		pendingCalls = nil
		req = agentStreamRequest{SessionID: currentSessionID, ToolResults: toolResults}
	}

	assistant := s.lastAssistantContent()
	if assistant == "" {
		assistant = "(sem resposta)"
	}
	s.mu.Lock()
	if currentSessionID != "" {
		s.sessionID = currentSessionID
	}
	s.mu.Unlock()
	return assistant, nil
}

func (s *Service) lastAssistantContent() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.history) - 1; i >= 0; i-- {
		if s.history[i].Role == "assistant" && s.history[i].Content != "" {
			return s.history[i].Content
		}
	}
	return ""
}

func (s *Service) executeRound(ctx context.Context, cfg Config, req agentStreamRequest, onToken func(string), pendingCalls *[]pendingToolCall) (string, error) {
	baseURL, err := normalizeAgentChatBaseURL(cfg.Endpoint)
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(req)
	reqCtx, cancel := context.WithTimeout(ctx, 130*time.Second)
	defer cancel()

	endpoint := baseURL + "/api/v1/agent-auth/me/ai-chat/stream"
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("criar request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	netutil.SetAgentAuthHeadersWithAgentID(httpReq, cfg.APIKey, cfg.AgentID)

	resp, err := tlsutil.NewHTTPClient(130 * time.Second).Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("stream status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	sessionID, _, err := s.parseMultiRoundSSE(resp.Body, onToken, pendingCalls)
	return sessionID, err
}

func (s *Service) parseMultiRoundSSE(body io.Reader, onToken func(string), pendingCalls *[]pendingToolCall) (string, bool, error) {
	var contentBuf strings.Builder
	currentSessionID := ""
	done := false

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
			s.logf(fmt.Sprintf("[chat] SSE parse error: %v (raw=%s)", err, TruncateForLog(data, 200)))
			continue
		}
		switch strings.ToLower(evt.Type) {
		case "token":
			if evt.Content != "" {
				contentBuf.WriteString(evt.Content)
				if onToken != nil {
					onToken(evt.Content)
				}
			}
		case "tool_call":
			if evt.ToolCallID != "" && evt.ToolName != "" {
				*pendingCalls = append(*pendingCalls, pendingToolCall{CallID: evt.ToolCallID, Name: evt.ToolName, Args: evt.ToolArguments})
			}
		case "round_end":
			if evt.SessionID != "" {
				currentSessionID = evt.SessionID
			}
			if contentBuf.Len() > 0 {
				s.mu.Lock()
				s.history = append(s.history, Message{Role: "assistant", Content: contentBuf.String()})
				s.mu.Unlock()
			}
			return currentSessionID, false, nil
		case "done":
			if evt.SessionID != "" {
				currentSessionID = evt.SessionID
			}
			done = true
			if contentBuf.Len() > 0 {
				s.mu.Lock()
				s.history = append(s.history, Message{Role: "assistant", Content: contentBuf.String()})
				s.mu.Unlock()
			}
			return currentSessionID, true, nil
		case "error":
			msg := strings.TrimSpace(evt.Error)
			if msg == "" {
				msg = "stream erro"
			}
			return currentSessionID, false, fmt.Errorf("%s", msg)
		}
	}
	if err := scanner.Err(); err != nil {
		return currentSessionID, false, fmt.Errorf("ler stream: %w", err)
	}
	return currentSessionID, done, nil
}

func (s *Service) fallbackToSync(ctx context.Context, cfg Config, message, sessionID string, onToken func(string)) (string, error) {
	resp, err := s.callAgentChatSync(ctx, cfg, message, sessionID)
	if err != nil {
		return "", err
	}
	assistant := strings.TrimSpace(resp.AssistantMessage)
	if assistant == "" {
		assistant = "(sem resposta)"
	}
	if onToken != nil {
		onToken(assistant)
	}
	s.mu.Lock()
	if strings.TrimSpace(resp.SessionID) != "" {
		s.sessionID = strings.TrimSpace(resp.SessionID)
	}
	s.history = append(s.history, Message{Role: "assistant", Content: assistant})
	s.mu.Unlock()
	return assistant, nil
}
