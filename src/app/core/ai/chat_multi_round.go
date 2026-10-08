// Package ai: multi-round agent loop — Server-Managed Agent Loop client side.
package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"discovery/app/core/tlsutil"
	"discovery/app/netutil"
)

// ErrTurnBusy é devolvido quando já existe um turno de chat em andamento.
// Sentinel de propósito: a camada de UI/serviço precisa distinguir essa recusa
// (não é falha de resposta — não deve gerar notificação) de um erro real.
var ErrTurnBusy = errors.New("já existe uma resposta em andamento — aguarde ou clique em Parar")

// maxMultiRounds limita o número de rounds do loop multi-round no agent.
// Alinhado com o orçamento do servidor (MaxToolCallIterations, default 10,
// clamp 1-20): o agent não deve desistir antes do servidor.
const maxMultiRounds = 20

// maxMultiRoundTotal limita o tempo TOTAL do loop multi-round (todos os
// rounds + execução de tools). Com 20 rounds × timeout progressivo por round
// (até 130s) + 60s por tool, o pior caso sem deadline passaria de 40min.
// 10min é folga suficiente para tool chains longas sem deixar o usuário
// esperando indefinidamente.
const maxMultiRoundTotal = 10 * time.Minute

func (s *Service) SendStreamMultiRound(
	ctx context.Context,
	userMessage string,
	onToken func(string),
	onStatus func(string),
	mcpExecutor func(ctx context.Context, toolName, argsJSON string) (string, error),
	onA2ui ...func(string),
) (string, error) {
	return s.SendStreamMultiRoundWithProgress(ctx, userMessage, onToken, onStatus, nil, mcpExecutor, onA2ui...)
}

// SendStreamMultiRoundWithProgress é a versão estendida do multi-round que
// recebe callback de progresso do agent loop (chunk "loop_progress" do
// servidor: round atual / máximo). onLoopProgress pode ser nil.
func (s *Service) SendStreamMultiRoundWithProgress(
	ctx context.Context,
	userMessage string,
	onToken func(string),
	onStatus func(string),
	onLoopProgress func(round, maxRounds int),
	mcpExecutor func(ctx context.Context, toolName, argsJSON string) (string, error),
	onA2ui ...func(string),
) (string, error) {
	// M4: um turno por vez. Dois fluxos concorrentes (webview + debug HTTP,
	// duplo clique em enviar) não devem compartilhar history/sessionID.
	if !s.turnMu.TryLock() {
		// A recusa era invisível no chat_logs.jsonl — impossível diagnosticar o
		// "já existe uma resposta em andamento" que o usuário vê quando a UI e
		// o core divergem sobre o turno ativo (ex.: UI liberada antes do
		// terminal real do turno). Loga para permitir correlação com o log da UI.
		s.logf("[chat] send recusado: turno anterior ainda em andamento (%q)", TruncateForLog(userMessage, 200))
		s.logChatEntry(ChatLogEntry{
			Type:    "turn_busy_rejected",
			Method:  "multi_round",
			UserMsg: TruncateForLog(userMessage, 500),
		})
		return "", ErrTurnBusy
	}
	defer s.turnMu.Unlock()

	streamCtx, streamCancel := context.WithCancel(ctx) // Registra o cancel para que StopStream() (botão "Parar" do frontend)
	// interrompa também o loop multi-round — antes, só o stream single-round
	// era cancelável e o botão não tinha efeito aqui.
	streamID := s.registerStreamCancel(streamCancel)
	defer func() {
		s.unregisterStreamCancel(streamID)
		streamCancel()
		// As ações A2UI NÃO são descartadas aqui de propósito: um clique feito
		// DURANTE o turno precisa sobreviver até o turno-sentinela que o frontend
		// inicia logo após "chat:done". O descarte neste ponto matava justamente o
		// clique legítimo (a ação morria antes do turno-sentinela consumir).
		// A limpeza de ações órfãs ficou em resolveA2uiTurn, no início de um
		// turno de mensagem digitada.
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
	// Resolve a relação da mensagem entrante com a fila de ações A2UI ANTES de
	// validar: a sentinela __a2ui_action__ é um turno disparado por clique em
	// surface, não uma mensagem de usuário (ver resolveA2uiTurn).
	action, isA2uiTurn, a2uiErr := s.resolveA2uiTurn(userMessage)
	if a2uiErr != nil {
		s.logChatEntry(ChatLogEntry{
			Type:    "chat_request",
			Method:  "multi_round",
			Error:   a2uiErr.Error(),
			UserMsg: TruncateForLog(userMessage, 500),
		})
		return "", a2uiErr
	}

	// Só mensagem real do usuário passa por validação e entra no histórico; a
	// sentinela interna jamais deve ir ao LLM nem poluir a conversa.
	if !isA2uiTurn {
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
		appendHistoryLocked(s, Message{Role: "user", Content: userMessage})
		s.mu.Unlock()
	}

	// Se houver uma ação A2UI (userAction de uma surface), injeta-a como tool
	// result no primeiro round para o LLM reagir ao clique/input. O servidor C#
	// espera toolResults no formato {callId, name, result}.
	//
	// IMPORTANTE: quando há ação A2UI, o request deve ter Message VAZIO (null)
	// e ToolResults preenchido, para que o servidor chame StreamMultiRoundAsync
	// (round 2+) em vez de StreamAsync (round 1). O AgentAuthController decide:
	//   (cmd.Message != null) ? StreamAsync : StreamMultiRoundAsync
	// Se enviarmos Message + ToolResults juntos, o servidor chama StreamAsync e
	// IGNORA os ToolResults — a ação A2UI nunca chegaria ao LLM.
	var initialToolResults []toolResultItem
	if action != nil {
		initialToolResults = append(initialToolResults, toolResultItem{
			// CallID ÚNICO por clique (surface + seq): cliques repetidos na mesma
			// surface geravam o MESMO tool_call_id, e ids repetidos na sessão
			// quebram o pareamento tool_call/tool_message do provedor.
			CallID: a2uiActionCallID(action),
			Name:   "a2ui_action",
			// O snapshot da última definição da surface vai anexado: o servidor
			// persiste a resposta assistente SEM o bloco a2ui, então o LLM não tem
			// a árvore anterior no histórico para reemitir em updateComponents —
			// era por isso que "Avançar" no stepper só devolvia prosa e a tela não
			// mudava de passo (2026-10-08).
			Result: buildA2uiActionToolResult(action, s.a2uiSurfaceSnapshot(action.SurfaceID)),
		})
		s.logf("[chat] ação A2UI '%s' (seq=%d) injetada como tool result", action.Name, action.Seq)
	}

	pendingCalls := make([]pendingToolCall, 0)
	// Se há ação A2UI, Message fica vazio (null) para o servidor usar o fluxo
	// multi-round com ToolResults. Caso contrário, envia a mensagem do usuário.
	// Mode explícito elimina a dependência da convenção "Message == null".
	reqMessage := userMessage
	reqMode := "user_message"
	if isA2uiTurn {
		reqMessage = ""
		reqMode = "a2ui_action"
	}
	req := agentStreamRequest{Message: reqMessage, SessionID: sessionID, ToolResults: initialToolResults, Mode: reqMode}
	// Prints/atachamentos do usuário (data URLs) entram no primeiro round.
	if imgs := s.takePendingImages(); len(imgs) > 0 {
		req.Images = imgs
		s.logf("[chat] %d imagem(ns) anexada(s) enviadas ao servidor", len(imgs))
	}
	// Model pass-through (Fase 3): campo opcional, servidores antigos ignoram.
	reqModel := strings.TrimSpace(cfg.Model)
	req.Model = reqModel
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
	// terminalMessage: preenchido quando uma tool do round e TERMINAL (ex.:
	// power_action restart/shutdown). Nesse caso o turno e encerrado na hora,
	// sem novo round no LLM — ver terminalToolResult.
	terminalMessage := ""
	forcedRetries := 0
	// B3: tool results já executadas mas ainda não processadas pelo LLM em um
	// round concluído. Se o round seguinte falhar por erro de rede/HTTP,
	// tentamos reenviá-las uma vez (mode tool_results) antes de degradar.
	undeliveredResults := make([]toolResultItem, 0)
	rescueAttempted := false

	// Guardrails A2UI client-side: normalização, teto por turno e dedup. Agora
	// centralizados em a2uiClientGuard para valerem também nos caminhos
	// síncronos (SendWithA2ui/fallbackToSync).
	guard := newA2uiClientGuard(maxA2uiMessagesPerTurn)
	var a2uiCb func(string)
	if len(onA2ui) > 0 && onA2ui[0] != nil {
		base := onA2ui[0]
		a2uiCb = func(msg string) {
			m, reason := guard.Offer(msg)
			switch reason {
			case a2uiDropEmpty:
				return
			case a2uiDropDuplicate:
				s.logf("[chat] a2ui: payload duplicado descartado (%d chars)", len(m))
				return
			case a2uiDropLimit:
				s.logf("[chat] a2ui: limite de %d mensagens por turno atingido — mensagem descartada", maxA2uiMessagesPerTurn)
				return
			}
			if trimmed := strings.TrimSpace(msg); m != trimmed {
				s.logf("[chat] a2ui: payload normalizado (rótulo duplicado/markdown/botões) %d -> %d chars", len(trimmed), len(m))
			}
			// Guarda a última definição da surface: será reenviada ao LLM no
			// clique de navegação (o histórico persistido não tem o bloco a2ui).
			s.rememberA2uiSurface(m)
			base(m)
		}
	}

	// Salva o tamanho do historico ANTES de iniciar o loop multi-round.
	// Isso garante que lastAssistantContentSince() so retorne respostas
	// geradas NESTA chamada, evitando repetir conteudo de conversas anteriores
	// quando o LLM nao produz tokens novos (resposta vazia).
	s.mu.RLock()
	historyBeforeLen := len(s.history)
	s.mu.RUnlock()

	for round := 0; round < maxMultiRounds; round++ {
		// Deadline total do loop: encerra com resposta parcial em vez de
		// continuar indefinidamente (o servidor também tem seu orçamento).
		if time.Since(startTime) >= maxMultiRoundTotal {
			s.logChatEntry(ChatLogEntry{
				Type:      "multi_round_total_timeout",
				Method:    "multi_round",
				SessionID: currentSessionID,
				Round:     round,
				Error:     "deadline total do loop atingido",
			})
			break
		}
		roundStart := time.Now()
		if onStatus != nil {
			if round == 0 {
				onStatus("Um instante...")
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

		// O heartbeat de progresso do servidor é contado POR REQUEST (toolIterations
		// é local ao StreamAsync dele). Em tool chains MCP cada round do agente é um
		// request novo, então o servidor sempre reporta "round 1" — a pill do
		// frontend travava em "Etapa 1 de N" durante toda a cadeia. O único contador
		// com escopo de TURNO é este loop, então reescrevemos o progresso com o round
		// local. maxRounds vai 0 de propósito: o teto de iterações do servidor não
		// limita esta cadeia (ele só vale para tools executadas lá dentro), e um
		// "de N" fixo aqui seria uma promessa falsa. O frontend cai em
		// "Etapa {round}" (chat.activity.roundOnly) quando maxRounds é 0.
		roundProgress := turnRoundProgress(onLoopProgress, round)

		roundSessionID, roundErr := s.executeRound(streamCtx, cfg, req, round, onStatus, onToken, &pendingCalls, roundProgress, a2uiCb)
		// BUG (turno real de 2026-10-01 12:01Z): em erro o executeRound devolve
		// sessionID VAZIO; sobrescrever currentSessionID zerava a sessão e o
		// resgate (B3) era rejeitado com "SessionId requerido em multi-round",
		// derrubando o turno para o sync e PERDENDO as capturas já feitas.
		currentSessionID = resolveRoundSessionID(currentSessionID, roundSessionID, sessionID)
		err = roundErr
		roundElapsed := time.Since(roundStart)

		if err == nil {
			// B3: o round anterior entregou seus tool results ao servidor
			// (o request atual os carregou) — limpa o buffer de resgate.
			undeliveredResults = nil
		}

		if err != nil {
			s.logChatEntry(ChatLogEntry{
				Type:      "round_error",
				Method:    "multi_round",
				SessionID: currentSessionID,
				Round:     round,
				Error:     err.Error(),
				LatencyMs: int(roundElapsed.Milliseconds()),
			})
			// B3: em rounds > 0 com tool results já executadas mas ainda não
			// processadas pelo LLM, tenta reenviá-las UMA vez (mode
			// tool_results) em vez de degradar direto para o sync — que não
			// suporta function calling e perderia todo o contexto das tools.
			// O fallback sync fica apenas para falha no round 0.
			if round > 0 && !rescueAttempted && len(undeliveredResults) > 0 && strings.TrimSpace(currentSessionID) != "" {
				rescueAttempted = true
				s.logChatEntry(ChatLogEntry{
					Type:      "round_error_rescue",
					Method:    "multi_round",
					SessionID: currentSessionID,
					Round:     round,
					ToolCalls: []string{"rescue:" + fmt.Sprint(len(undeliveredResults)) + " results"},
					Error:     err.Error(),
				})
				if onStatus != nil {
					onStatus("Reconectando — reenviando resultados das ferramentas...")
				}
				s.mu.RLock()
				tools := make([]map[string]any, 0)
				if s.registry != nil {
					tools = s.registry.OpenAIFunctions()
				}
				s.mu.RUnlock()
				req = agentStreamRequest{
					SessionID:   currentSessionID,
					ToolResults: undeliveredResults,
					Tools:       tools,
					Model:       reqModel,
					Mode:        "tool_results",
				}
				undeliveredResults = nil
				continue
			}
			// Em turnos iniciados por ação A2UI, a sentinela não deve ir ao
			// servidor como mensagem de usuário (o contexto real da ação já
			// está na sessão do servidor). Envia string vazia.
			// O fallback sync só roda para falha no round 0 (ou resgate
			// indisponível) — partial tokens já persistidos pelo parser SSE.
			// Rounds > 0 NÃO degradam para o sync: o sync não suporta function
			// calling e descartaria tools/capturas já executadas — o LLM respondia
			// como se não tivesse visto nada (exatamente o turno de
			// 2026-10-01 12:01Z, em que 2 capturas de janela foram perdidas).
			// Devolve erro claro para o usuário tentar de novo; o texto parcial
			// já transmitido permanece na bolha.
			if round > 0 {
				s.logChatEntry(ChatLogEntry{
					Type:      "round_failed_no_sync",
					Method:    "multi_round",
					SessionID: currentSessionID,
					Round:     round,
					Error:     err.Error(),
				})
				return "", fmt.Errorf("não consegui concluir a análise das ferramentas no round %d (%v) — as capturas/resultados não foram descartados no servidor; tente novamente", round+1, err)
			}
			fallbackMsg := userMessage
			if isA2uiTurn {
				fallbackMsg = ""
			}
			return s.fallbackToSync(streamCtx, cfg, fallbackMsg, sessionID, onToken, onA2ui...)
		}

		hasToolCalls := len(pendingCalls) > 0
		calledTools := make([]string, 0, len(pendingCalls))
		for _, tc := range pendingCalls {
			calledTools = append(calledTools, tc.Name)
		}

		if !hasToolCalls {
			if strings.TrimSpace(s.lastAssistantContentSince(historyBeforeLen)) == "" {
				s.logf("[chat] round %d: stream sem conteúdo e sem tool_call (resposta vazia do servidor)", round)
			} else {
				s.logf("[chat] round %d: LLM respondeu sem tool_call (resposta direta)", round)
			}
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
				// O retry por intenção de chamado (e o diagnóstico "não usou
				// tools") só faz sentido quando NENHUMA tool rodou no turno:
				// se list_tickets/get_ticket_details já executaram nos rounds
				// anteriores, o round final sem tool call É a resposta legítima.
				// Sem este gate, o turno real de 2026-09-19 22:26 (pergunta de
				// chamados com list_tickets no round 0) acusava "LLM nao usou
				// tools" no log, e uma pergunta com "tem chamado aberto?" no
				// round final dispararia um retry que reexecutaria list_tickets
				// à toa (+10-30s e resposta duplicada).
				if totalToolCalls == 0 {
					if r := diagnoseMissingToolCall(s, userMessage); r != "" {
						retry = r
					}
				}
				if retry == "" && detectIncompleteResponse(assistantText) {
					retry = "A sua resposta anterior terminou sem concluir a ação — você apenas prometeu fazer algo sem executar. Se existe uma ferramenta para a ação que o usuário pediu, EXECUTE-A agora via function call nativa. Caso contrário, dê uma resposta final completa e direta respondendo à solicitação, sem promessas como \"vou fazer\" ou \"só um instante\"."
				}
				if retry == "" && detectDegenerateResponse(assistantText) {
					retry = "A sua resposta anterior saiu corrompida/degenerada (fragmentos, reticências soltas ou raciocínio interno vazado no lugar da resposta). Reenvie AGORA a resposta completa, coesa e em português, respondendo diretamente à solicitação do usuário. NAO repita o texto corrompido, NAO escreva raciocínio interno ou planejamento — apenas a resposta final limpa."
				}
				if retry != "" {
					forcedRetries++
					var tools []map[string]any
					s.mu.RLock()
					if s.registry != nil {
						tools = s.registry.OpenAIFunctions()
					}
					s.mu.RUnlock()
					// M3: o retry NÃO reenvia a userMessage como Message — isso
					// duplicava a mensagem do usuário no histórico do servidor.
					// Message fica vazio (null) e apenas o SystemNote orienta o
					// LLM a concluir (mesmo contrato do fluxo A2UI, onde a
					// sentinela "__a2ui_action__" jamais pode ser reenviada).
					s.logChatEntry(ChatLogEntry{
						Type:      "tool_force_retry",
						Method:    "multi_round",
						SessionID: currentSessionID,
						Error:     retry,
					})
					req = agentStreamRequest{
						Message:    "",
						SessionID:  currentSessionID,
						Tools:      tools,
						Model:      reqModel,
						SystemNote: retry,
						Mode:       "tool_results",
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
		for toolIdx, tc := range pendingCalls {
			// Progresso granular: num lote com várias tools a execução é
			// sequencial e cada item pode levar minutos (winget/choco). Sem um
			// status por item, a UI fica com o rótulo do lote congelado — foi o
			// que aconteceu no turno de 2026-09-30 17:08Z (log chat_logs.jsonl):
			// 4 upgrade_package em ~5min com o indicador parado. O formato é
			// consumido por describeChatActivity em frontend/js/app-chat.js.
			if onStatus != nil && len(pendingCalls) > 1 {
				onStatus(formatToolProgressStatus(tc.Name, toolIdx+1, len(pendingCalls), tc.Args))
			}
			toolExecStart := time.Now()
			var result string
			var execErr error
			if mcpExecutor == nil {
				result = `{"error":"MCP indisponivel"}`
				execErr = fmt.Errorf("MCP indisponivel")
			} else {
				// Timer por tool: se a execução travar (ex.: comando remoto
				// pendurado), devolve erro estruturado em vez de bloquear o
				// loop para sempre — o LLM pode informar o usuário e sugerir
				// alternativas. O streamCtx (cancelável via botão Parar)
				// prevalece sobre o timer.
				//
				// B4/B5: ask_user NÃO tem timeout — a pergunta fica aberta até
				// o usuário responder e o chat prossegue na resposta (o timer
				// antigo de 60/120/150s matava a pergunta e a resposta do
				// usuário ia para uma pergunta morta). O cancelamento do
				// stream (botão Parar) interrompe a espera via streamCtx.
				//
				// capture_screenshot também exige interação do usuário
				// (autorização + seleção da área/janela no overlay) e read_file
				// exige autorização por leitura: o timer de 60s mataria a
				// interação/espera pela resposta do usuário.
				//
				// Toda tool que pede AUTORIZAÇÃO ao usuário (gravação, instalar,
				// desinstalar, parar serviço, reiniciar...) fica na mesma regra:
				// o timeout de 60s mataria a pergunta antes do clique. A lista é
				// derivada de mcp.ToolConsentFor, então acompanha a política.
				// Timeout padrão de 60s; a política de escopo do servidor pode definir
				// um valor por tool (pendingToolCall.TimeoutSeconds) — ex.: ações
				// pesadas precisam de mais tempo.
				execTimeout := 60 * time.Second
				if tc.TimeoutSeconds > 0 {
					execTimeout = time.Duration(tc.TimeoutSeconds) * time.Second
				}
				var execCtx context.Context
				var execCancel context.CancelFunc
				if interactiveToolRequiresUser(tc.Name, tc.Args) {
					execCtx, execCancel = context.WithCancel(streamCtx)
				} else {
					execCtx, execCancel = context.WithTimeout(streamCtx, execTimeout)
				}
				result, execErr = mcpExecutor(execCtx, tc.Name, tc.Args)
				execCancel()
			}
			toolElapsed := time.Since(toolExecStart)

			if execErr != nil {
				// M2: serializa o erro com json.Marshal — interpolação manual
				// com ReplaceAll não escapava barras invertidas e produzia
				// JSON inválido quando a mensagem de erro continha aspas/barra.
				if b, mErr := json.Marshal(map[string]string{"error": execErr.Error()}); mErr == nil {
					result = string(b)
				} else {
					result = `{"error":"erro interno na execucao da tool"}`
				}
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

			// Tool TERMINAL (ex.: power_action restart/shutdown): a maquina sera
			// reiniciada/desligada, logo NAO ha tempo para outro round do LLM nem
			// para o tool result chegar ao servidor. Encerra o turno agora.
			if finalMsg, terminal := terminalToolResult(result); terminal {
				terminalMessage = finalMsg
				break
			}
		}

		if terminalMessage != "" {
			s.logChatEntry(ChatLogEntry{
				Type:         "turn_terminal_tool",
				Method:       "multi_round",
				SessionID:    currentSessionID,
				Round:        round,
				HasToolCalls: true,
				ToolCalls:    calledTools,
				ResponseLen:  len(terminalMessage),
				Assistant:    TruncateForLog(terminalMessage, 1000),
				LatencyMs:    int(time.Since(startTime).Milliseconds()),
			})
			// Emite a mensagem final ao usuario e fecha o turno (o chamador emite
			// chat:done). Sem novo round no LLM: ele nao teria tempo de responder
			// antes do reboot e um erro deixaria o usuario preso em "Pensando...".
			if onToken != nil {
				onToken(terminalMessage)
			}
			s.mu.Lock()
			appendHistoryLocked(s, Message{Role: "assistant", Content: terminalMessage})
			// Sessao NOVA no proximo turno: o tool result desta tool terminal NUNCA
			// e enviado ao servidor (o turno fecha aqui), entao a sessao atual fica
			// com um assistant.tool_calls sem o role=tool correspondente — e os
			// providers (OpenAI/OpenRouter) rejeitam a cadeia com HTTP 400. Isso
			// importa quando o usuario CANCELA o aviso e volta a conversar (no
			// reboot a sessao se perde de qualquer forma).
			s.sessionID = ""
			s.mu.Unlock()
			return terminalMessage, nil
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
		// B3: guarda os results como "não entregues" até o próximo round
		// confirmar sucesso; em erro, o resgate os reenvia.
		undeliveredResults = toolResults
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
			Model:       reqModel,
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

// terminalToolResult detecta um resultado de tool marcado com "terminal": true
// e devolve a mensagem final para o usuario.
//
// Contrato usado por tools cuja acao encerra o contexto do computador
// (power_action restart/shutdown): o tool result precisa voltar IMEDIATAMENTE,
// porque (a) a maquina sera reiniciada/desligada e o processo morre antes de
// qualquer novo round, e (b) o servidor/LLM nunca receberiam a resposta — o
// usuario ficaria com "Pensando..." preso. O tool devolve
// {ok:true, terminal:true, outcome:"notification_shown", message:"..."} e o
// loop encerra o turno com essa mensagem (sem novo round).
func terminalToolResult(result string) (string, bool) {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return "", false
	}
	var payload struct {
		Terminal bool   `json:"terminal"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil || !payload.Terminal {
		return "", false
	}
	msg := strings.TrimSpace(payload.Message)
	if msg == "" {
		msg = "Acao agendada — a conversa foi encerrada. A maquina sera reiniciada/desligada."
	}
	return msg, true
}

// formatToolProgressStatus monta o status de progresso de UMA tool dentro de um
// lote: "Executando: <tool> (i/n) [<hint>]...".
//
// O sufixo "(i/n)" diz em qual item do lote estamos e o bloco "[hint]" diz QUAL
// item (ex.: id do pacote winget) — sem isso o usuário só vê o rótulo genérico
// do lote ("Atualizando programa") congelado por minutos. O hint é opcional e
// fica sempre no fim, antes das reticências; o parser do frontend
// (describeChatActivity) depende dessa ordem.
func formatToolProgressStatus(name string, index, total int, args string) string {
	base := "Executando: " + strings.TrimSpace(name)
	if total > 1 && index > 0 {
		base += fmt.Sprintf(" (%d/%d)", index, total)
	}
	if hint := toolProgressHint(args); hint != "" {
		base += " [" + hint + "]"
	}
	return base + "..."
}

// toolProgressHint extrai dos argumentos JSON da tool um rótulo curto e legível
// para o status de progresso (id/nome do pacote, host, arquivo). Devolve "" se
// nenhum campo aproveitável existir — o status sai sem o bloco "[...]".
//
// Colchetes, vírgulas e quebras de linha são neutralizados porque delimitam
// partes do status no parser do frontend.
func toolProgressHint(args string) string {
	args = strings.TrimSpace(args)
	if args == "" || args == "{}" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return ""
	}
	for _, key := range []string{"id", "packageId", "name", "app", "host", "file", "path"} {
		v, ok := m[key].(string)
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		// Data URL (ex.: argumento da captura de tela) não é rótulo útil: viraria
		// "data:image/png;base64,…" no chip em vez do alvo da ação.
		if strings.HasPrefix(v, "data:") {
			continue
		}
		// Neutraliza os caracteres que o parser do frontend usa como delimitador:
		// "," separa as tools da lista, "[" e "]" delimitam o bloco do hint e
		// quebras de linha bagunçam o chip. Sem isso, um pacote com vírgula no
		// nome (ex.: {"name":"Foo, Bar"}) viraria dois chips lixo.
		v = strings.NewReplacer(
			"[", "(", "]", ")",
			",", ";",
			"\n", " ", "\r", " ",
		).Replace(v)
		if utf8.RuneCountInString(v) > 60 {
			v = string([]rune(v)[:60]) + "…"
		}
		return v
	}
	return ""
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

func (s *Service) executeRound(ctx context.Context, cfg Config, req agentStreamRequest, round int, onStatus func(string), onToken func(string), pendingCalls *[]pendingToolCall, onLoopProgress func(round, maxRounds int), onA2ui ...func(string)) (string, error) {
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
	// B7: o erro era engolido e o request seguia sem auth — falhava com 401
	// opaco e sem diagnóstico. Agora falha cedo com o erro logado.
	if err := netutil.SetAgentAuthHeadersWithAgentID(httpReq, cfg.APIKey, cfg.AgentID); err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:      "round_http_error",
			Method:    "multi_round",
			Endpoint:  endpoint,
			SessionID: req.SessionID,
			Error:     fmt.Sprintf("auth headers: %v", err),
			LatencyMs: int(time.Since(startTime).Milliseconds()),
		})
		return "", fmt.Errorf("headers de autenticacao: %w", err)
	}

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

	// Status pós-conexão: do HTTP 200 até a primeira tool/token é o LLM
	// planejando/executando — sem este sinal, o rótulo inicial ("Um
	// instante...") persistia 10-20s cobrindo a fase errada.
	if onStatus != nil {
		onStatus("Analisando sua solicitacao...")
	}

	sessionID, _, err := s.parseMultiRoundSSEWithProgress(resp.Body, onToken, pendingCalls, onLoopProgress, onA2ui...)
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

// turnRoundProgress adapta o callback de progresso do servidor para o contador
// de TURNO do agente. O servidor conta as iterações do loop DELE por request
// (toolIterations é local ao StreamAsync): nas tool chains MCP cada round do
// agente é um request novo e o heartbeat sempre volta "round 1". O round
// relevante para o usuário é o deste loop (0-based), então descartamos os
// valores do servidor. maxRounds vai 0 de propósito: o teto de iterações do
// servidor não limita a cadeia delegada, e um "de N" fixo seria falso — o
// frontend renderiza "Etapa {round}" (chat.activity.roundOnly) nesse caso.
// Devolve nil quando não há callback, preservando os chamadores antigos.
func turnRoundProgress(onLoopProgress func(round, maxRounds int), round int) func(round, maxRounds int) {
	if onLoopProgress == nil {
		return nil
	}
	return func(_, _ int) { onLoopProgress(round+1, 0) }
}

func (s *Service) parseMultiRoundSSE(body io.Reader, onToken func(string), pendingCalls *[]pendingToolCall, onA2ui ...func(string)) (string, bool, error) {
	return s.parseMultiRoundSSEWithProgress(body, onToken, pendingCalls, nil, onA2ui...)
}

// parseMultiRoundSSEWithProgress é a versão estendida do parser SSE que
// também recebe callback de progresso do agent loop (chunk "loop_progress"
// do servidor: round atual / máximo de rounds). Permite ao frontend exibir
// "Processando… round X/Y" em vez de parecer travado durante tool chains.
func (s *Service) parseMultiRoundSSEWithProgress(body io.Reader, onToken func(string), pendingCalls *[]pendingToolCall, onLoopProgress func(round, maxRounds int), onA2ui ...func(string)) (string, bool, error) {
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
		// coladas), quebrando a renderização. Apenas o prefixo "data: " e o
		// \r residual de streams CRLF são removidos, preservando o restante.
		line = strings.TrimSuffix(line, "\r")
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
		case "loop_progress":
			// Heartbeat de progresso do agent loop emitido pelo servidor.
			// Não altera o estado do loop — apenas informa o frontend.
			if onLoopProgress != nil && evt.LoopMaxRounds > 0 {
				onLoopProgress(evt.LoopRound, evt.LoopMaxRounds)
			}
		case "budget_exhausted", "budget_renewed":
			// Orçamento de rounds do turno: o servidor vai PEDIR autorização ao
			// usuário (esgotado) ou retomar as ações pendentes (renovado). Sem
			// este sinal a UI não tinha como explicar por que a ação clicada
			// ainda não executou.
			state := "exhausted"
			if strings.EqualFold(evt.Type, "budget_renewed") {
				state = "renewed"
			}
			s.logf("[chat] orçamento de rounds: %s (%d/%d) — %s", state, evt.LoopRound, evt.LoopMaxRounds, TruncateForLog(evt.Content, 160))
			s.notifyBudget(state, evt.Content, evt.LoopRound, evt.LoopMaxRounds)
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
			// Timeouts por tool definidos na política de escopo do servidor
			// (governança das MCP tools): aplicados às chamadas pendentes deste
			// round. Vazio/ausente = padrão do agente.
			if pendingCalls != nil && len(evt.ToolTimeouts) > 0 {
				for i := range *pendingCalls {
					if secs, ok := evt.ToolTimeouts[(*pendingCalls)[i].Name]; ok && secs > 0 {
						(*pendingCalls)[i].TimeoutSeconds = secs
					}
				}
			}
			// B16: o servidor pode entregar o texto final no próprio evento de
			// encerramento (done/round_end) em vez de em tokens incrementais.
			// Antes esse conteúdo era descartado e o chat exibia "sem resposta".
			if evt.Content != "" {
				contentBuf.WriteString(evt.Content)
				if onToken != nil {
					onToken(evt.Content)
				}
			}
			if contentBuf.Len() > 0 {
				s.mu.Lock()
				appendHistoryLocked(s, Message{Role: "assistant", Content: contentBuf.String()})
				s.mu.Unlock()
			}
			return currentSessionID, false, nil
		case "done":
			if evt.SessionID != "" {
				currentSessionID = evt.SessionID
			}
			done = true
			if evt.Content != "" {
				contentBuf.WriteString(evt.Content)
				if onToken != nil {
					onToken(evt.Content)
				}
			}
			if contentBuf.Len() > 0 {
				s.mu.Lock()
				appendHistoryLocked(s, Message{Role: "assistant", Content: contentBuf.String()})
				s.mu.Unlock()
			}
			return currentSessionID, true, nil
		case "error":
			msg := strings.TrimSpace(evt.Error)
			if msg == "" {
				msg = "stream erro"
			}
			// B3: persiste tokens parciais já recebidos antes do erro — sem
			// isso o texto já exibido ao usuário se perdia do histórico.
			if contentBuf.Len() > 0 {
				s.mu.Lock()
				appendHistoryLocked(s, Message{Role: "assistant", Content: contentBuf.String()})
				s.mu.Unlock()
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
		// B3: persiste tokens parciais recebidos antes do erro de leitura.
		if contentBuf.Len() > 0 {
			s.mu.Lock()
			appendHistoryLocked(s, Message{Role: "assistant", Content: contentBuf.String()})
			s.mu.Unlock()
		}
		return currentSessionID, false, fmt.Errorf("ler stream: %w", err)
	}

	// B16: stream fechado sem NENHUM evento SSE — sintoma clássico de dead-end
	// no servidor (200 sem tokens/tool_call). Registra diagnóstico explícito em
	// vez de deixar o turno cair silenciosamente no fallback.
	if parsedEvents == 0 {
		s.logf("[chat] stream encerrado sem nenhum evento SSE do servidor")
		s.logChatEntry(ChatLogEntry{
			Type:      "empty_stream_response",
			Method:    "multi_round",
			SessionID: currentSessionID,
		})
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
// resolveRoundSessionID decide o sessionId a usar depois de um round.
//
// Regra: um id devolvido válido vence; se o round falhou e devolveu vazio,
// MANTÉM o id anterior (o servidor já conhece a sessão) e só usa o fallback do
// turno quando não havia nenhum. Sem isso, o resgate de tool results (B3)
// enviava sessionId vazio e o servidor respondia
// "SessionId requerido em multi-round" — o turno caía para o sync e as
// capturas eram descartadas (regressão real de 2026-10-01 12:01Z).
func resolveRoundSessionID(previous, returned, fallback string) string {
	if strings.TrimSpace(returned) != "" {
		return returned
	}
	if strings.TrimSpace(previous) != "" {
		return previous
	}
	return fallback
}

func roundTimeout(round int) time.Duration {
	switch round {
	case 0:
		// B16: 60s cortava modelos de raciocínio (openrouter/auto) que levam
		// mais tempo até o primeiro token. Alinhado ao piso de 120s do servidor.
		return 180 * time.Second
	case 1:
		return 210 * time.Second
	default:
		return 240 * time.Second
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
	// B8: fecha delimitadores na ordem LIFO correta — a string aberta é o
	// delimitador mais interno e deve ser fechada ANTES dos brackets. Fechar
	// colchetes primeiro produzia JSON inválido (caía sempre no fallback de
	// texto cru mesmo quando a remontagem seria válida).
	if inStr {
		closed += `"`
	}
	for i := len(stack) - 1; i >= 0; i-- {
		closed += string(stack[i])
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

func (s *Service) fallbackToSync(ctx context.Context, cfg Config, message, sessionID string, onToken func(string), onA2ui ...func(string)) (string, error) {
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

	// A2UI no fallback síncrono: o servidor devolve as interfaces no JSON (sem
	// SSE). Sem este repasse o card sumia em silêncio quando o stream falhava.
	if len(onA2ui) > 0 && onA2ui[0] != nil {
		// Mesmos guardrails do streaming (normalizar/dedup/teto) e mesma
		// retenção da surface para os cliques de navegação continuarem funcionando.
		guard := newA2uiClientGuard(maxA2uiMessagesPerTurn)
		for _, msg := range resp.A2uiMessages {
			m, reason := guard.Offer(msg)
			if reason != a2uiAccept {
				continue
			}
			s.rememberA2uiSurface(m)
			onA2ui[0](m)
		}
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
	appendHistoryLocked(s, Message{Role: "assistant", Content: assistant})
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
// Deve ser chamada APENAS quando nenhuma tool foi executada no turno
// (totalToolCalls == 0): com tools já executadas, um round final sem tool call
// é a resposta legítima, e o diagnóstico/retry seria falso positivo.
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
				return "O usuario ja confirmou a abertura do chamado. ANTES de abrir, verifique duplicidade: emita AGORA uma unica function call nativa `list_tickets` e AGUARDE o resultado (NAO envie create_ticket no mesmo turno). Se nenhum item tiver isOpen=true sobre o mesmo assunto, emita na proxima rodada a function call nativa `create_ticket` com os dados ja coletados (title, description, departmentId, priority, category). Se existir chamado aberto do mesmo assunto, NAO crie duplicata: informe o usuario e ofereca add_ticket_comment no chamado existente. NAO responda apenas com texto prometendo verificar."
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
		"disco":          "disk / get_inventory",
		"hd":             "disk / get_inventory",
		"ssd":            "disk / get_inventory",
		"gpu":            "get_inventory",
		"placa de video": "get_inventory",
		"versao":         "get_inventory",
		"windows":        "get_inventory / windows_update",
		"impressora":     "printer",
		"imprimir":       "printer",
		"chamado":        "list_ticket_templates / list_tickets / create_ticket",
		"ticket":         "list_ticket_templates / list_tickets / create_ticket",
		"suporte":        "list_ticket_templates / list_tickets / create_ticket",
		"modelo":         "list_ticket_templates",
		"template":       "list_ticket_templates",
		"abrir":          "list_ticket_templates / create_ticket",
		"criar":          "list_ticket_templates / create_ticket",
		"ping":           "network_diagnostics",
		"dns":            "network_diagnostics",
		"firewall":       "security_status / get_inventory",
		"antivirus":      "security_status / get_inventory",
		"virus":          "process_control / get_recent_errors / get_inventory",
		"malware":        "process_control / get_recent_errors / get_inventory",
		"bateria":        "get_inventory",
		"bitlocker":      "get_inventory",
		"usuarios":       "get_inventory",
		"logados":        "get_inventory",
		"exportar":       "export_inventory_markdown",
		"relatorio":      "export_inventory_markdown",
		"lento":          "process_control / get_inventory",
		"lentid":         "process_control / get_inventory",
		"travando":       "process_control",
		"travado":        "process_control",
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

// isDegeneratePunctLine reporta se a linha inteira é só pontuação/fragmento
// ("...", "…", "---", "??", "!!", "--"). Uma ou duas ocorrências são
// separadores legítimos de markdown; em massa, são sinais de saída
// degenerada do modelo.
func isDegeneratePunctLine(t string) bool {
	if t == "" {
		return false
	}
	for _, r := range t {
		switch r {
		case '.', '…', '?', '!', '-', '*', '_', '~':
			continue
		default:
			return false
		}
	}
	return true
}

// degeneratePlanPhrases são frases de planejamento em inglês típicas de
// raciocínio vazado como conteúdo (modelos reasoning mal isolados no stream).
// A resposta do agente é em pt-BR; planejamento em inglês no meio dela é
// vazamento. (Visto em produção em 20/09: "We need to respond properly...",
// "Since they want verification, we can run listinstalledpackages...")
var degeneratePlanPhrases = []string{
	"we need to", "the user wants", "the user asked", "let's do that",
	"we should", "i'll need to", "we can run", "we have already",
	"we need to call", "properly respond",
}

// detectDegenerateResponse retorna true quando a resposta final apresenta
// sinais de saída degenerada do modelo: muitas linhas que são só fragmentos
// ("...", "…", "---", "??", palavras truncadas de 1-3 chars) ou planejamento
// interno vazado como conteúdo. Nesse caso o turno é reenviado UMA vez com
// instrução de regeneração limpa (mesmo mecanismo do retry forçado), em vez
// de encerrar a conversa exibindo lixo. Conservador: exige múltiplos sinais
// para não reagir a respostas legítimas com poucos separadores "---".
func detectDegenerateResponse(assistant string) bool {
	if len([]rune(assistant)) < 60 {
		return false
	}
	junk := 0
	total := 0
	for _, line := range strings.Split(assistant, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		total++
		if isDegeneratePunctLine(t) {
			junk++
			continue
		}
		// Fragmento: linha alfabética muito curta ("O", "Vou", "Sim") sozinha,
		// sem ser título/lista — no texto cru do modelo os itens de lista
		// chegam colados, então linha curtíssima isolada é resíduo.
		r := []rune(t)
		if len(r) <= 3 && !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "*") {
			junk++
		}
	}
	if junk >= 5 && total > 0 && junk*3 >= total {
		return true
	}
	plan := 0
	lower := strings.ToLower(assistant)
	for _, p := range degeneratePlanPhrases {
		if strings.Contains(lower, p) {
			plan++
		}
	}
	return plan >= 3
}
