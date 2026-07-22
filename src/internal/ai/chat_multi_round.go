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

	startTime := time.Now()

	s.mu.Lock()
	cfg := s.cfg
	sessionID := s.sessionID
	s.mu.Unlock()

	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		err := fmt.Errorf("configuracao de IA incompleta")
		s.logChatEntry(ChatLogEntry{
			Type:    "chat_request",
			Method:  "multi_round",
			Error:   err.Error(),
			UserMsg: TruncateForLog(userMessage, 2000),
		})
		return "", err
	}
	if err := validateChatMessage(userMessage); err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:    "chat_request",
			Method:  "multi_round",
			Error:   err.Error(),
			UserMsg: TruncateForLog(userMessage, 2000),
		})
		return "", err
	}

	s.mu.Lock()
	s.history = append(s.history, Message{Role: "user", Content: userMessage})
	s.mu.Unlock()

	pendingCalls := make([]pendingToolCall, 0)
	req := agentStreamRequest{Message: userMessage, SessionID: sessionID}
	// Injetar tools MCP no primeiro round (round 0).
	s.mu.RLock()
	toolCount := 0
	if s.registry != nil {
		tools := s.registry.OpenAIFunctions()
		toolCount = len(tools)
		if toolCount > 0 {
			req.Tools = tools
			s.logf("[chat] %d tools enviadas para o servidor", toolCount)
		} else {
			s.logf("[chat] aviso: nenhuma tool MCP registrada — chat funcionara sem function calling")
		}
	} else {
		s.logf("[chat] aviso: registry MCP nao disponivel")
	}
	s.mu.RUnlock()

	// Log de início do fluxo multi-round
	s.logChatEntry(ChatLogEntry{
		Type:       "multi_round_start",
		Method:     "multi_round",
		SessionID:  sessionID,
		MessageLen: len(userMessage),
		ToolCount:  toolCount,
		UserMsg:    TruncateForLog(userMessage, 2000),
	})

	var currentSessionID string
	var err error
	totalToolCalls := 0
	allCalledTools := make([]string, 0)

	for round := 0; round < 5; round++ {
		roundStart := time.Now()
		if onStatus != nil {
			if round == 0 {
				onStatus("Conectando ao servidor...")
			} else {
				onStatus(fmt.Sprintf("Round %d — processando tools...", round+1))
			}
		}

		// Log de início do round
		roundToolCount := 0
		if req.Tools != nil {
			roundToolCount = len(req.Tools)
		}
		s.logChatEntry(ChatLogEntry{
			Type:       "round_start",
			Method:     "multi_round",
			SessionID:  currentSessionID,
			Round:      round,
			ToolCount:  roundToolCount,
			MessageLen: len(req.Message),
		})

		currentSessionID, err = s.executeRound(streamCtx, cfg, req, onToken, &pendingCalls)
		roundElapsed := time.Since(roundStart)

		if err != nil {
			s.logChatEntry(ChatLogEntry{
				Type:      "round_error",
				Method:    "multi_round",
				SessionID: currentSessionID,
				Round:     round,
				Error:     err.Error(),
				LatencyMs: int(roundElapsed.Milliseconds()),
			})
			return s.fallbackToSync(streamCtx, cfg, userMessage, sessionID, onToken)
		}

		hasToolCalls := len(pendingCalls) > 0
		calledTools := make([]string, 0, len(pendingCalls))
		for _, tc := range pendingCalls {
			calledTools = append(calledTools, tc.Name)
		}

		if !hasToolCalls {
			s.logf("[chat] round %d: LLM respondeu sem tool_call (resposta direta)", round)
			s.logChatEntry(ChatLogEntry{
				Type:         "round_end",
				Method:       "multi_round",
				SessionID:    currentSessionID,
				Round:        round,
				HasToolCalls: false,
				LatencyMs:    int(roundElapsed.Milliseconds()),
			})
			// Diagnóstico: detectar perguntas que provavelmente precisariam de tools
			if round == 0 {
				diagnoseMissingToolCall(s, userMessage)
			}
			break
		}

		// Log das tool calls recebidas
		s.logChatEntry(ChatLogEntry{
			Type:         "tool_calls_received",
			Method:       "multi_round",
			SessionID:    currentSessionID,
			Round:        round,
			HasToolCalls: true,
			ToolCalls:    calledTools,
			ToolArgs:     toolArgsForLog(pendingCalls),
			LatencyMs:    int(roundElapsed.Milliseconds()),
		})

		totalToolCalls += len(pendingCalls)
		allCalledTools = append(allCalledTools, calledTools...)

		if onStatus != nil {
			names := make([]string, len(pendingCalls))
			for i, tc := range pendingCalls {
				names[i] = tc.Name
			}
			onStatus(fmt.Sprintf("Executando: %s...", strings.Join(names, ", ")))
		}
		var toolResults []toolResultItem
		toolResultNames := make([]string, 0, len(pendingCalls))
		for _, tc := range pendingCalls {
			toolExecStart := time.Now()
			var result string
			var execErr error
			if mcpExecutor == nil {
				result = `{"error":"MCP indisponivel"}`
				execErr = fmt.Errorf("MCP indisponivel")
			} else {
				result, execErr = mcpExecutor(streamCtx, tc.Name, tc.Args)
			}
			toolElapsed := time.Since(toolExecStart)

			if execErr != nil {
				result = fmt.Sprintf(`{"error":"%s"}`, strings.ReplaceAll(execErr.Error(), `"`, `'`))
				s.logChatEntry(ChatLogEntry{
					Type:      "tool_exec_error",
					Method:    "tool_exec",
					SessionID: currentSessionID,
					Round:     round,
					ToolCalls: []string{tc.Name},
					ToolArgs:  []string{TruncateForLog(tc.Args, 300)},
					Error:     execErr.Error(),
					LatencyMs: int(toolElapsed.Milliseconds()),
				})
				toolResultNames = append(toolResultNames, fmt.Sprintf("%s=err", tc.Name))
			} else {
				s.logChatEntry(ChatLogEntry{
					Type:        "tool_exec_ok",
					Method:      "tool_exec",
					SessionID:   currentSessionID,
					Round:       round,
					ToolCalls:   []string{tc.Name},
					ToolArgs:    []string{TruncateForLog(tc.Args, 300)},
					ToolResults: []string{TruncateForLog(result, 500)},
					LatencyMs:   int(toolElapsed.Milliseconds()),
				})
				toolResultNames = append(toolResultNames, fmt.Sprintf("%s=ok", tc.Name))
			}

			toolResults = append(toolResults, toolResultItem{CallID: tc.CallID, Name: tc.Name, Result: result})
		}

		s.logChatEntry(ChatLogEntry{
			Type:         "round_end",
			Method:       "multi_round",
			SessionID:    currentSessionID,
			Round:        round,
			HasToolCalls: true,
			ToolCalls:    calledTools,
			ToolResults:  toolResultNames,
			LatencyMs:    int(roundElapsed.Milliseconds()),
		})

		pendingCalls = nil
		// Reenviar tools nos rounds 2+ para que o LLM mantenha contexto
		// das ferramentas disponíveis. Modelos menores (ex: gpt-oss-20b)
		// podem "esquecer" as tools entre rounds se não reenviadas.
		s.mu.RLock()
		tools := s.registry.OpenAIFunctions()
		s.mu.RUnlock()
		req = agentStreamRequest{
			SessionID:   currentSessionID,
			ToolResults: toolResults,
			Tools:       tools,
		}
	}

	totalElapsed := time.Since(startTime)
	assistant := s.lastAssistantContent()
	if assistant == "" {
		assistant = "(sem resposta)"
	}
	s.mu.Lock()
	if currentSessionID != "" {
		s.sessionID = currentSessionID
	}
	s.mu.Unlock()

	// Log final do fluxo multi-round
	s.logChatEntry(ChatLogEntry{
		Type:         "multi_round_done",
		Method:       "multi_round",
		SessionID:    currentSessionID,
		HasToolCalls: totalToolCalls > 0,
		ToolCalls:    allCalledTools,
		ToolCount:    totalToolCalls,
		ResponseLen:  len(assistant),
		Assistant:    TruncateForLog(assistant, 4000),
		LatencyMs:    int(totalElapsed.Milliseconds()),
	})

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
	startTime := time.Now()
	baseURL, err := normalizeAgentChatBaseURL(cfg.Endpoint)
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:      "round_http_error",
			Method:    "multi_round",
			SessionID: req.SessionID,
			Error:     fmt.Sprintf("baseURL: %v", err),
			LatencyMs: int(time.Since(startTime).Milliseconds()),
		})
		return "", err
	}
	payload, _ := json.Marshal(req)
	reqCtx, cancel := context.WithTimeout(ctx, 130*time.Second)
	defer cancel()

	endpoint := baseURL + "/api/v1/agent-auth/me/ai-chat/stream"
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:      "round_http_error",
			Method:    "multi_round",
			Endpoint:  endpoint,
			SessionID: req.SessionID,
			Error:     fmt.Sprintf("new request: %v", err),
			LatencyMs: int(time.Since(startTime).Milliseconds()),
		})
		return "", fmt.Errorf("criar request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	netutil.SetAgentAuthHeadersWithAgentID(httpReq, cfg.APIKey, cfg.AgentID)

	resp, err := tlsutil.NewHTTPClient(130 * time.Second).Do(httpReq)
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:      "round_http_error",
			Method:    "multi_round",
			Endpoint:  endpoint,
			SessionID: req.SessionID,
			Error:     fmt.Sprintf("Do: %v", err),
			LatencyMs: int(time.Since(startTime).Milliseconds()),
		})
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		errMsg := strings.TrimSpace(string(body))
		s.logChatEntry(ChatLogEntry{
			Type:       "round_http_error",
			Method:     "multi_round",
			Endpoint:   endpoint,
			SessionID:  req.SessionID,
			StatusCode: resp.StatusCode,
			Error:      errMsg,
			LatencyMs:  int(time.Since(startTime).Milliseconds()),
		})
		return "", fmt.Errorf("stream status %d: %s", resp.StatusCode, errMsg)
	}

	s.logChatEntry(ChatLogEntry{
		Type:       "round_http_ok",
		Method:     "multi_round",
		Endpoint:   endpoint,
		SessionID:  req.SessionID,
		StatusCode: resp.StatusCode,
		LatencyMs:  int(time.Since(startTime).Milliseconds()),
	})

	sessionID, _, err := s.parseMultiRoundSSE(resp.Body, onToken, pendingCalls)
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:      "round_sse_error",
			Method:    "multi_round",
			Endpoint:  endpoint,
			SessionID: sessionID,
			Error:     err.Error(),
			LatencyMs: int(time.Since(startTime).Milliseconds()),
		})
	}
	return sessionID, err
}

func (s *Service) parseMultiRoundSSE(body io.Reader, onToken func(string), pendingCalls *[]pendingToolCall) (string, bool, error) {
	var contentBuf strings.Builder
	currentSessionID := ""
	done := false
	parsedEvents := 0

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
			s.logf("[chat] SSE parse error: %v (raw=%s)", err, TruncateForLog(data, 200))
			s.logChatEntry(ChatLogEntry{
				Type:    "sse_parse_error",
				Method:  "multi_round",
				Error:   fmt.Sprintf("json: %v", err),
				UserMsg: TruncateForLog(data, 500),
			})
			continue
		}
		parsedEvents++
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
				argsStr := evt.effectiveToolArgs()
				if argsStr == "" {
					s.logf("[chat] ALERTA: tool_call '%s' recebido SEM argumentos! toolArguments e toolArgumentsDelta estao vazios. Verifique a serializacao do servidor (esperado: toolArgumentsDelta em camelCase).", evt.ToolName)
				}
				s.logChatEntry(ChatLogEntry{
					Type:      "sse_tool_call",
					Method:    "multi_round",
					ToolCalls: []string{evt.ToolName},
					ToolArgs:  []string{TruncateForLog(argsStr, 300)},
				})
				*pendingCalls = append(*pendingCalls, pendingToolCall{CallID: evt.ToolCallID, Name: evt.ToolName, Args: argsStr})
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
		s.logChatEntry(ChatLogEntry{
			Type:      "sse_scanner_error",
			Method:    "multi_round",
			SessionID: currentSessionID,
			Error:     err.Error(),
		})
		return currentSessionID, false, fmt.Errorf("ler stream: %w", err)
	}

	s.logChatEntry(ChatLogEntry{
		Type:        "sse_stream_end",
		Method:      "multi_round",
		SessionID:   currentSessionID,
		ResponseLen: parsedEvents,
	})
	return currentSessionID, done, nil
}

// toolArgsForLog extrai os argumentos de pendingToolCalls para logging (truncados 300 chars cada).
func toolArgsForLog(calls []pendingToolCall) []string {
	if len(calls) == 0 {
		return nil
	}
	args := make([]string, len(calls))
	for i, tc := range calls {
		args[i] = TruncateForLog(tc.Args, 300)
	}
	return args
}

func (s *Service) fallbackToSync(ctx context.Context, cfg Config, message, sessionID string, onToken func(string)) (string, error) {
	s.logChatEntry(ChatLogEntry{
		Type:       "fallback_to_sync",
		Method:     "multi_round",
		SessionID:  sessionID,
		MessageLen: len(message),
		UserMsg:    TruncateForLog(message, 500),
	})

	resp, err := s.callAgentChatSync(ctx, cfg, message, sessionID)
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:      "fallback_sync_error",
			Method:    "multi_round",
			SessionID: sessionID,
			Error:     err.Error(),
		})
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

	s.logChatEntry(ChatLogEntry{
		Type:        "fallback_sync_ok",
		Method:      "multi_round",
		SessionID:   resp.SessionID,
		ResponseLen: len(assistant),
		Assistant:   TruncateForLog(assistant, 2000),
	})
	return assistant, nil
}

// diagnoseMissingToolCall verifica se a pergunta do usuario contem palavras-chave
// que sugerem que o LLM deveria ter usado uma ferramenta MCP, e emite um warning
// no log para facilitar o diagnostico de System Prompts ineficazes.
func diagnoseMissingToolCall(s *Service, userMessage string) {
	msg := strings.ToLower(userMessage)
	hints := map[string]string{
		"instalado":      "list_installed_packages",
		"instalada":      "list_installed_packages",
		"instalar":       "search_packages / install_package",
		"instale":        "search_packages / install_package",
		"instala":        "search_packages / install_package",
		"desinstalar":    "uninstall_package",
		"desinstale":     "uninstall_package",
		"atualizar":      "get_pending_updates / upgrade_package",
		"atualizacao":    "get_pending_updates",
		"update":         "get_pending_updates",
		"memoria":        "get_inventory",
		"ram":            "get_inventory",
		"cpu":            "get_inventory",
		"processador":    "get_inventory",
		"disco":          "get_inventory",
		"hd":             "get_inventory",
		"ssd":            "get_inventory",
		"gpu":            "get_inventory",
		"placa de video": "get_inventory",
		"versao":         "get_inventory",
		"windows":        "get_inventory",
		"impressora":     "list_printers",
		"imprimir":       "list_printers / list_print_jobs",
		"chamado":        "list_tickets",
		"ticket":         "list_tickets",
		"suporte":        "list_tickets",
		"ping":           "ping_host",
		"dns":            "flush_dns",
		"firewall":       "get_inventory",
		"antivirus":      "get_inventory",
		"bateria":        "get_inventory",
		"bitlocker":      "get_inventory",
		"usuarios":       "get_inventory",
		"logados":        "get_inventory",
		"exportar":       "export_inventory_markdown / export_inventory_pdf",
		"relatorio":      "export_inventory_markdown / export_inventory_pdf",
	}
	hits := make([]string, 0)
	for keyword, tool := range hints {
		if strings.Contains(msg, keyword) {
			hits = append(hits, fmt.Sprintf("%s→%s", keyword, tool))
		}
	}
	if len(hits) > 0 {
		s.logf("[chat] diagnostico: LLM nao usou tools, mas mensagem contem palavras-chave: %s. Verifique o System Prompt do servidor.", strings.Join(hits, ", "))
		s.logChatEntry(ChatLogEntry{
			Type:      "missing_tool_diagnostic",
			Method:    "multi_round",
			UserMsg:   TruncateForLog(userMessage, 500),
			ToolCalls: hits,
			Error:     "LLM nao usou tools nesta pergunta. Ferramentas sugeridas: " + strings.Join(hits, ", "),
		})
	}
}
