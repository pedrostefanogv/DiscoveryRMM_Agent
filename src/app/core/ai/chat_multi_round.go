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
	"unicode/utf8"

	"discovery/app/core/tlsutil"
	"discovery/app/netutil"
)

func (s *Service) SendStreamMultiRound(
	ctx context.Context,
	userMessage string,
	onToken func(string),
	onStatus func(string),
	mcpExecutor func(ctx context.Context, toolName, argsJSON string) (string, error),
	onA2ui ...func(string),
) (string, error) {
	streamCtx, streamCancel := context.WithCancel(ctx)
	// Registra o cancel para que StopStream() (botão "Parar" do frontend)
	// interrompa também o loop multi-round — antes, só o stream single-round
	// era cancelável e o botão não tinha efeito aqui.
	streamID := s.registerStreamCancel(streamCancel)
	defer func() {
		s.unregisterStreamCancel(streamID)
		streamCancel()
	}()
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
	// Verifica se há ação A2UI pendente ANTES de validar a mensagem. Quando o
	// usuário clica num botão A2UI, a mensagem pode ser vazia (a ação é enviada
	// como tool result). Nesse caso, pulamos a validação de mensagem.
	hasPendingA2ui := s.peekA2uiAction() != nil
	if !hasPendingA2ui {
		if err := validateChatMessage(userMessage); err != nil {
			s.logChatEntry(ChatLogEntry{
				Type:    "chat_request",
				Method:  "multi_round",
				Error:   err.Error(),
				UserMsg: TruncateForLog(userMessage, 2000),
			})
			return "", err
		}
	}

	// Quando há ação A2UI, a "mensagem" é apenas uma sentinela interna
	// (__a2ui_action__) que não deve poluir o histórico nem ser enviada ao LLM.
	// A ação em si é injetada como tool result. Só adiciona ao history quando
	// for uma mensagem real do usuário.
	if !hasPendingA2ui {
		s.mu.Lock()
		s.history = append(s.history, Message{Role: "user", Content: userMessage})
		s.mu.Unlock()
	}

	// Se houver uma ação A2UI pendente (userAction de uma surface), injeta-a
	// como um tool result no primeiro round para o LLM reagir ao clique/input.
	// O servidor C# espera toolResults no formato {callId, name, result}.
	//
	// IMPORTANTE: quando há ação A2UI, o request deve ter Message VAZIO (null)
	// e ToolResults preenchido, para que o servidor chame StreamMultiRoundAsync
	// (round 2+) em vez de StreamAsync (round 1). O AgentAuthController decide:
	//   (cmd.Message != null) ? StreamAsync : StreamMultiRoundAsync
	// Se enviarmos Message + ToolResults juntos, o servidor chama StreamAsync e
	// IGNORA os ToolResults — a ação A2UI nunca chegaria ao LLM.
	var initialToolResults []toolResultItem
	hasA2uiAction := false
	if action := s.takeA2uiAction(); action != nil {
		hasA2uiAction = true
		ctxJSON, _ := json.Marshal(action.Context)
		initialToolResults = append(initialToolResults, toolResultItem{
			CallID: "a2ui_" + action.SurfaceID,
			Name:   "a2ui_action",
			Result: fmt.Sprintf(`{"surfaceId":%q,"name":%q,"context":%s}`, action.SurfaceID, action.Name, string(ctxJSON)),
		})
		s.logf("[chat] ação A2UI '%s' injetada como tool result", action.Name)
	}

	pendingCalls := make([]pendingToolCall, 0)
	// Se há ação A2UI, Message fica vazio (null) para o servidor usar o fluxo
	// multi-round com ToolResults. Caso contrário, envia a mensagem do usuário.
	// Mode explícito elimina a dependência da convenção "Message == null".
	reqMessage := userMessage
	reqMode := "user_message"
	if hasA2uiAction {
		reqMessage = ""
		reqMode = "a2ui_action"
	}
	req := agentStreamRequest{Message: reqMessage, SessionID: sessionID, ToolResults: initialToolResults, Mode: reqMode}
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
	forcedRetries := 0

	// Salva o tamanho do historico ANTES de iniciar o loop multi-round.
	// Isso garante que lastAssistantContentSince() so retorne respostas
	// geradas NESTA chamada, evitando repetir conteudo de conversas anteriores
	// quando o LLM nao produz tokens novos (resposta vazia).
	s.mu.RLock()
	historyBeforeLen := len(s.history)
	s.mu.RUnlock()

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

		currentSessionID, err = s.executeRound(streamCtx, cfg, req, round, onToken, &pendingCalls, onA2ui...)
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
			// Em turnos iniciados por ação A2UI, a sentinela não deve ir ao
			// servidor como mensagem de usuário (o contexto real da ação já
			// está na sessão do servidor). Envia string vazia.
			fallbackMsg := userMessage
			if hasA2uiAction {
				fallbackMsg = ""
			}
			return s.fallbackToSync(streamCtx, cfg, fallbackMsg, sessionID, onToken)
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
			// Diagnóstico: detectar perguntas que provavelmente precisariam de tools.
			// Para ações explícitas de chamados (abrir/consultar), reenvia UMA vez com
			// instrução imperativa para forçar o uso da function call nativa.
			//
			// Também detecta quando o LLM "parou sem concluir": a resposta é apenas
			// uma promessa de ação (ex.: "vou abrir", "só um instante", "deixa eu
			// verificar") sem executar a tool call. Nesse caso reenvia com instrução
			// de conclusão, evitando que o usuário precise digitar "prossiga".
			// Limitado a 1 retry forçado por chamada (fora do round 0) para não
			// causar loop; em rounds > 0 o retry cobre também o caso em que o LLM
			// prometeu agir APÓS executar uma tool.
			if forcedRetries < 1 {
				assistantText := s.lastAssistantContentSince(historyBeforeLen)
				var retry string
				if r := diagnoseMissingToolCall(s, userMessage); r != "" {
					retry = r
				} else if detectIncompleteResponse(assistantText) {
					retry = "A sua resposta anterior terminou sem concluir a ação — você apenas prometeu fazer algo sem executar. Se existe uma ferramenta para a ação que o usuário pediu, EXECUTE-A agora via function call nativa. Caso contrário, dê uma resposta final completa e direta respondendo à solicitação, sem promessas como \"vou fazer\" ou \"só um instante\"."
				}
				if retry != "" {
					forcedRetries++
					var tools []map[string]any
					s.mu.RLock()
					if s.registry != nil {
						tools = s.registry.OpenAIFunctions()
					}
					s.mu.RUnlock()
					// Quando o turno foi iniciado por uma ação A2UI, a sentinela
					// "__a2ui_action__" NÃO pode ser reenviada como Message —
					// isso faria o servidor chamar StreamAsync e a ação A2UI
					// (já consumida via takeA2uiAction) seria perdida. Nesse
					// caso Message fica vazio (null) e apenas o SystemNote
					// orienta o LLM a concluir.
					retryMessage := userMessage
					if hasA2uiAction {
						retryMessage = ""
					}
					s.logChatEntry(ChatLogEntry{
						Type:      "tool_force_retry",
						Method:    "multi_round",
						SessionID: currentSessionID,
						Error:     retry,
					})
					req = agentStreamRequest{
						Message:    retryMessage,
						SessionID:  currentSessionID,
						Tools:      tools,
						SystemNote: retry,
						Mode:       "user_message",
					}
					if onStatus != nil {
						onStatus("Concluindo ação solicitada...")
					}
					continue
				}
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

			toolResults = append(toolResults, toolResultItem{CallID: tc.CallID, Name: tc.Name, Result: truncateToolResult(result)})
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
			Mode:        "tool_results",
		}
	}

	totalElapsed := time.Since(startTime)
	assistant := s.lastAssistantContentSince(historyBeforeLen)
	if assistant == "" {
		// Se o LLM nao produziu resposta textual (ex.: tool calls sem texto,
		// ou stream vazio), informa o usuario de forma util em vez de mostrar
		// "(sem resposta)".
		if totalToolCalls > 0 {
			assistant = "Ações executadas. Se precisar de mais alguma coisa, é só pedir!"
		} else {
			assistant = "Não consegui processar sua solicitação. Pode reformular a pergunta?"
		}
	}
	// Sanitiza vazamentos de tool calls / marcações internas do LLM (DSML,
	// blocos ```json com invokes, ações A2UI cruas) antes de exibir ao usuário.
	if clean, removed := sanitizeAssistantText(assistant); removed {
		s.logf("[chat] sanitização: removidos vazamentos de tool call/marcação interna da resposta (%d -> %d chars)", len(assistant), len(clean))
		s.logChatEntry(ChatLogEntry{
			Type:        "assistant_sanitized",
			Method:      "multi_round",
			SessionID:   currentSessionID,
			ResponseLen: len(clean),
		})
		assistant = clean
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

// lastAssistantContentSince retorna o conteudo da ultima resposta do assistant
// adicionada ao historico a partir do indice historySince (exclusivo).
// Isso evita retornar conteudo stale de conversas anteriores quando o LLM
// nao produz tokens novos no round atual.
func (s *Service) lastAssistantContentSince(historySince int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.history) - 1; i >= historySince; i-- {
		if s.history[i].Role == "assistant" && s.history[i].Content != "" {
			return s.history[i].Content
		}
	}
	return ""
}

// lastAssistantContent mantido para compatibilidade com outros chamadores.
func (s *Service) lastAssistantContent() string {
	return s.lastAssistantContentSince(0)
}

func (s *Service) executeRound(ctx context.Context, cfg Config, req agentStreamRequest, round int, onToken func(string), pendingCalls *[]pendingToolCall, onA2ui ...func(string)) (string, error) {
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
	timeout := roundTimeout(round)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
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

	resp, err := tlsutil.NewHTTPClient(timeout).Do(httpReq)
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

	sessionID, _, err := s.parseMultiRoundSSE(resp.Body, onToken, pendingCalls, onA2ui...)
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

func (s *Service) parseMultiRoundSSE(body io.Reader, onToken func(string), pendingCalls *[]pendingToolCall, onA2ui ...func(string)) (string, bool, error) {
	var contentBuf strings.Builder
	currentSessionID := ""
	done := false
	parsedEvents := 0

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		// IMPORTANTE: NÃO usar TrimSpace na linha nem no conteúdo do token.
		// O TrimSpace apagava espaços/quebras de linha legítimos nas fronteiras
		// dos tokens (ex.: "apenas23 MB", "de1 GB", linhas de tabela markdown
		// coladas), quebrando a renderização. Apenas o prefixo "data: " é
		// removido, preservando o restante.
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimPrefix(line, "data:")
		if strings.HasPrefix(data, " ") {
			data = data[1:]
		}
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
		case "a2ui":
			// Mensagem A2UI (interface rica) emitida pelo servidor. Repassa ao
			// frontend via callback onA2ui (que emite o evento Wails "chat:a2ui").
			// O servidor serializa A2uiJson como string; aqui já chega como o
			// JSON cru (sem aspas duplas), pronto para o frontend parsear.
			msg := strings.TrimSpace(evt.A2UI)
			if msg != "" && msg != "null" {
				if len(onA2ui) > 0 && onA2ui[0] != nil {
					onA2ui[0](msg)
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

// roundTimeout retorna um timeout progressivo por round: round 0 = 60s,
// round 1 = 90s, rounds 2+ = 130s. Reduz a espera do usuário quando o
// servidor está lento e dá folga extra para tool chains longas em rounds
// intermediários.
func roundTimeout(round int) time.Duration {
	switch round {
	case 0:
		return 60 * time.Second
	case 1:
		return 90 * time.Second
	default:
		return 130 * time.Second
	}
}

// maxToolResultBytes limita o tamanho de cada tool result enviado ao servidor.
// Resultados gigantes (get_inventory, list_installed_packages) podem estourar
// limites do servidor e degradar o contexto do LLM.
const maxToolResultBytes = 16 * 1024

// truncateToolResult trunca o resultado de uma tool para maxToolResultBytes.
// Tenta fechar estruturas JSON abertas de forma estruturalmente válida; se o
// resultado truncado não for JSON válido (ex.: corte no meio de uma chave ou
// de um rune multibyte), cai para texto cru com marcador — nunca devolve JSON
// quebrado que faria o parse falhar no servidor.
func truncateToolResult(result string) string {
	if len(result) <= maxToolResultBytes {
		return result
	}
	cut := result[:maxToolResultBytes]
	// Não partir rune UTF-8 no meio: recua bytes até o corte ser string válida.
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	trimmed := strings.TrimRight(cut, " \t\r\n,")
	// Fecha estruturas JSON abertas (contagem de delimitadores fora de strings).
	var stack []byte
	inStr := false
	esc := false
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	closed := trimmed
	for i := len(stack) - 1; i >= 0; i-- {
		closed += string(stack[i])
	}
	if inStr {
		closed += `"`
	}
	// Só usa a versão fechada se for JSON válido; caso contrário, texto cru
	// com marcador (o LLM entende ambos, mas JSON quebrado quebraria o parse).
	if json.Valid([]byte(closed)) {
		return closed
	}
	return cut + "\n...[resultado truncado pela limitação de tamanho]"
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
		assistant = "Não foi possível obter uma resposta do servidor. Tente novamente."
	}
	// O endpoint sync NÃO suporta function calling: o LLM tende a emitir as
	// tool calls como texto (blocos ```json com invokes, marcação DSML).
	// Sanitiza antes de exibir para o usuário não ver JSON/marcações cruas.
	if clean, removed := sanitizeAssistantText(assistant); removed {
		s.logf("[chat] fallback sync: sanitização removeu vazamentos de tool call da resposta (%d -> %d chars)", len(assistant), len(clean))
		s.logChatEntry(ChatLogEntry{
			Type:        "assistant_sanitized",
			Method:      "fallback_sync",
			SessionID:   resp.SessionID,
			ResponseLen: len(clean),
		})
		assistant = clean
	}
	if assistant == "" {
		// A resposta era SÓ tool calls vazadas — nada de texto útil restou.
		assistant = "Não consegui concluir a ação solicitada. Tente reformular o pedido."
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
//
// Retorna uma string de instrucao (retry forcado) quando a acao solicitada e
// explicita o suficiente para o agente reenviar com uma ordem imperativa —
// atualmente cobre abrir chamado e consultar chamados. Retorna "" quando apenas
// registra o diagnostico sem reenvio.
func diagnoseMissingToolCall(s *Service, userMessage string) string {
	msg := strings.ToLower(userMessage)
	for _, pattern := range ticketIntentPatterns {
		if !patternsMatch(msg, pattern.keywords) {
			continue
		}
		if pattern.kind == ticketOpen {
			if hasConfirmedAction(msg) {
				return "O usuario ja confirmou a abertura do chamado. Emita AGORA uma unica function call nativa `create_ticket` com os dados ja coletados (title, description, priority, category). NAO responda com texto, NAO prometa abrir, NAO chame outras ferramentas. Envie apenas a function call create_ticket."
			}
			continue
		}
		// ticketList: perguntou se ha chamados abertos
		return "O usuario perguntou sobre os chamados da maquina. Emita AGORA a function call nativa `list_tickets` e responda com base no resultado. NAO responda com texto, NAO prometa verificar. Envie apenas a function call list_tickets."
	}
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
		"chamado":        "list_tickets / create_ticket",
		"ticket":         "list_tickets / create_ticket",
		"suporte":        "list_tickets / create_ticket",
		"abrir":          "create_ticket",
		"criar":          "create_ticket",
		"ping":           "ping_host",
		"dns":            "flush_dns",
		"firewall":       "get_inventory",
		"antivirus":      "get_inventory",
		"virus":          "get_top_processes / get_recent_errors / get_inventory",
		"malware":        "get_top_processes / get_recent_errors / get_inventory",
		"bateria":        "get_inventory",
		"bitlocker":      "get_inventory",
		"usuarios":       "get_inventory",
		"logados":        "get_inventory",
		"exportar":       "export_inventory_markdown",
		"relatorio":      "export_inventory_markdown",
		"lento":          "get_top_processes / get_inventory",
		"lentid":         "get_top_processes / get_inventory",
		"travando":       "get_top_processes",
		"travado":        "get_top_processes",
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
	return ""
}

// ticketIntentKind classifica a intenção de chamado detectada.
type ticketIntentKind int

const (
	ticketOpen ticketIntentKind = iota // "abra/abre um chamado"
	ticketList                         // "tem algum chamado aberto?"
)

type ticketIntentPattern struct {
	kind     ticketIntentKind
	keywords []string
}

// ticketIntentPatterns são padrões de intenção de chamados que disparam o
// retry forçado (evita o loop de "vou abrir" sem tool call).
var ticketIntentPatterns = []ticketIntentPattern{
	{kind: ticketList, keywords: []string{"chamado", "aberto", "minha", "maquina", "tem"}},
	{kind: ticketList, keywords: []string{"ticket", "aberto"}},
	{kind: ticketList, keywords: []string{"chamados", "abertos", "para"}},
	{kind: ticketList, keywords: []string{"meus", "chamados"}},
	{kind: ticketOpen, keywords: []string{"abra", "chamado"}},
	{kind: ticketOpen, keywords: []string{"abre", "chamado"}},
	{kind: ticketOpen, keywords: []string{"abrir", "chamado"}},
	{kind: ticketOpen, keywords: []string{"abrir", "ticket"}},
	{kind: ticketOpen, keywords: []string{"abra", "ticket"}},
	{kind: ticketOpen, keywords: []string{"criar", "chamado"}},
	{kind: ticketOpen, keywords: []string{"crie", "chamado"}},
}

// patternsMatch verifica se TODAS as palavras-chave do padrão aparecem na mensagem.
// A comparação é case-insensitive (normaliza internamente para minúsculas).
func patternsMatch(msg string, keywords []string) bool {
	msg = strings.ToLower(msg)
	for _, kw := range keywords {
		if !strings.Contains(msg, kw) {
			return false
		}
	}
	return true
}

// hasConfirmedAction detecta confirmações curtas do usuário ("abra", "pode abrir",
// "sim", "prossiga", etc.) que seguem uma proposta de chamado.
// A comparação é case-insensitive (normaliza internamente para minúsculas).
func hasConfirmedAction(msg string) bool {
	msg = strings.ToLower(strings.TrimSpace(msg))
	if len(msg) > 60 {
		return false
	}
	for _, kw := range []string{"abra", "abre", "pode abrir", "sim", "prossiga", "pode", "ok", "confirma", "confirmado", "já", "ja"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// incompleteResponseMarkers são frases que indicam que o LLM prometeu executar
// uma ação mas encerrou o turno sem concluir (sem tool call e sem resposta final).
// Usado para detectar "LLM parou sem concluir" e reenviar automaticamente.
var incompleteResponseMarkers = []string{
	"vou fazer", "vou abrir", "vou verificar", "vou pegar", "vou coletar",
	"vou consultar", "vou tentar", "vou executar", "vou analisar", "vou buscar",
	"estou verificando", "estou consultando", "estou analisando", "estou buscando",
	"só um instante", "so um instante", "um instante", "um momento", "aguarde",
	"deixa eu", "deixe eu", "deixa-me", "permita-me", "vou montar", "vou registrar",
	"me deixe", "espere",
}

// detectIncompleteResponse retorna true quando a resposta do assistant terminou
// sem concluir de fato: é uma promessa de ação ("vou fazer...", "só um instante")
// sem uma resposta final substantiva. O texto é curto e termina sugerindo que
// algo ainda será feito. Limitado a respostas curtas (<= 300 chars) para evitar
// falso positivo em respostas longas e legítimas que contenham "vou verificar..."
// como parte de um diagnóstico completo.
func detectIncompleteResponse(assistant string) bool {
	if assistant == "" {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(assistant))

	// Respostas longas são tratadas como completas: uma análise detalhada não é
	// uma "promessa pendente" só por conter "vou verificar" em algum ponto.
	if len([]rune(text)) > 300 {
		return false
	}

	// Se a resposta contém um job concluído ou evidência de ação já realizada,
	// não é considerada incompleta. Estas respostas costumam ser mais longas e
	// afirmativas; exigimos uma pista de promessa pendente.
	hasPromise := false
	for _, m := range incompleteResponseMarkers {
		if strings.Contains(text, m) {
			hasPromise = true
			break
		}
	}
	if !hasPromise {
		return false
	}

	// Evita falso positivo quando a resposta já contém uma conclusão clara
	// (ex.: "Feito! desinstalei o programa"). Palavras de conclusão anulam a
	// detecção de resposta incompleta.
	for _, done := range completionMarkers {
		if strings.Contains(text, done) {
			return false
		}
	}

	return true
}

// completionMarkers são palavras/frases que indicam que o assistant já concluiu
// a ação ou deu uma resposta final — anulam a detecção de "resposta incompleta".
var completionMarkers = []string{
	"pronto", "concluí", "conclui", "finalizado", "finalizei", "feito",
	"instalado", "desinstalado", "atualizado", "criado", "aberto com sucesso",
	"chamado criado", "ticket criado", "reiniciei", "reiniciado",
	"resolvido", "solucionado", "tudo certo", "tudo pronto", "é isso",
	"isso é tudo", "mais alguma", "posso ajudar", "em que mais",
}
