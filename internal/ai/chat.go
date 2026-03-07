// Package ai provides an AI chat service that uses an OpenAI-compatible API
// with function calling backed by the MCP tool registry.
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
	"sync"
	"time"

	"winget-store/internal/mcp"
)

// Config holds the LLM API settings.
type Config struct {
	Endpoint     string `json:"endpoint"` // e.g. "https://api.openai.com/v1/chat/completions"
	APIKey       string `json:"apiKey"`
	Model        string `json:"model"`        // e.g. "gpt-4o-mini"
	SystemPrompt string `json:"systemPrompt"` // optional custom assistant instructions
}

// Message represents a single chat message.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall is an AI-requested function call.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Service manages conversations with an LLM that can call tools.
type Service struct {
	mu       sync.RWMutex
	cfg      Config
	registry *mcp.Registry
	history  []Message
	logger   func(string)

	streamMu           sync.Mutex
	activeStreamID     uint64
	activeStreamCancel context.CancelFunc
}

// NewService creates a chat service.
func NewService(registry *mcp.Registry) *Service {
	return &Service{
		registry: registry,
		history:  []Message{},
	}
}

// SetLogger configures an optional callback for chat diagnostics.
func (s *Service) SetLogger(logger func(string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = logger
}

// SetConfig updates the LLM API configuration.
func (s *Service) SetConfig(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

// GetConfig returns the current configuration (API key masked).
func (s *Service) GetConfig() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cfg
	if len(c.APIKey) > 8 {
		c.APIKey = c.APIKey[:4] + "..." + c.APIKey[len(c.APIKey)-4:]
	} else if c.APIKey != "" {
		c.APIKey = "***"
	}
	return c
}

// ClearHistory resets the conversation.
func (s *Service) ClearHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = []Message{}
}

func (s *Service) registerStreamCancel(cancel context.CancelFunc) uint64 {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	s.activeStreamID++
	id := s.activeStreamID
	s.activeStreamCancel = cancel
	return id
}

func (s *Service) unregisterStreamCancel(id uint64) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.activeStreamID == id {
		s.activeStreamCancel = nil
	}
}

// StopStream cancels the currently running streamed response, if any.
func (s *Service) StopStream() bool {
	s.streamMu.Lock()
	cancel := s.activeStreamCancel
	s.activeStreamCancel = nil
	s.streamMu.Unlock()

	if cancel != nil {
		cancel()
		return true
	}
	return false
}

// GetHistory returns a copy of the conversation history (for display).
func (s *Service) GetHistory() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Message, len(s.history))
	copy(out, s.history)
	return out
}

const defaultSystemPrompt = `Voce e o assistente Discovery, integrado a um aplicativo de gerenciamento de inventario e pacotes Windows.
Responda sempre em portugues brasileiro, com linguagem amigavel e acessivel para qualquer pessoa, evitando jargao tecnico desnecessario.

O que voce pode fazer (use as ferramentas disponiveis automaticamente quando fizer sentido):
- Consultar informacoes do computador: hardware, sistema operacional, discos, rede, memoria, GPU, bateria, BitLocker, softwares instalados, usuarios logados e mais (get_inventory).
- Pesquisar programas disponiveis no catalogo winget (search_packages).
- Instalar, desinstalar ou atualizar programas via winget (install_package, uninstall_package, upgrade_package, upgrade_all_packages).
- Verificar quais programas tem atualizacao pendente (get_pending_updates).
- Exportar um relatorio completo do computador em Markdown ou PDF (export_inventory_markdown, export_inventory_pdf).
- Verificar se o osquery esta presente no computador (get_osquery_status).

Regras de comportamento:
1. Faca somente o que o usuario pedir ou perguntar; nao execute nada extra por conta propria.
2. Antes de instalar, desinstalar, atualizar ou exportar qualquer coisa, sempre pesquise primeiro (search_packages / get_pending_updates) para confirmar o ID correto e informe o usuario.
3. Peca aprovacao explicita antes de qualquer acao que altere o computador. Explique em uma frase simples o que sera feito e aguarde confirmacao.
4. Ao mostrar dados do inventario, resuma as informacoes mais relevantes em linguagem clara; nao despeje dados brutos.
5. Quando uma acao for concluida, confirme de forma acolhedora incluindo o que foi feito e detalhes uteis (ex.: nome e versao do programa instalado).

Recursos de formatacao:
Voce pode usar Markdown para enriquecer suas respostas e melhorar a clareza:
- **negrito** para destaques importantes ou nomes de programas/recursos
- *italico* para enfase ou observacoes adicionais
- backticks para nomes de comandos, caminhos ou valores tecnicos
- blocos de codigo para output de comandos
- > citacao para avisos, dicas ou advertencias importantes
- # Titulo, ## Subtitulo para organizar respostas longas
- [link](url) para referencias externas
- Listas numeradas (1. 2. 3.) para passos sequenciais
- Tabelas Markdown para comparar dados ou listar informacoes tabulares. Use o formato padrao com | e --- para separar cabecalho e dados, incluindo alinhamento com :---:, ---: se necessario. Exemplo:
  | Nome | Versao | Status |
  |------|--------|--------|
  | App  | 1.0    | OK     |
Use a formatacao com moderacao; mantenha a resposta legivel e natural.

Botoes interativos:
O chat possui botoes dinamicos. Qualquer linha da sua resposta que comece com "- " ou "* " sera exibida como um botao clicavel para o usuario. Use esse recurso sempre que fizer sentido para facilitar a interacao:
- Ao oferecer opcoes ou escolhas, liste cada alternativa em sua propria linha com "- " no inicio (maximo 6 opcoes). Escreva cada opcao de forma curta e direta, pois o texto vira o rotulo do botao.
- Ao pedir confirmacao, inclua opcoes como "- Sim, pode prosseguir" e "- Nao, cancelar" para que o usuario responda com um clique.
- Ao sugerir proximos passos apos uma acao concluida, liste as sugestoes com "- " para que tambem virem botoes.
Nunca use "- " para informacoes descritivas que nao sejam opcoes clicaveis; use frases corridas ou paragrafos para explicacoes.

Navegacao interna do app:
- Existem ferramentas MCP para navegacao interna: get_internal_navigation_routes e build_internal_navigation_link.
- Sempre que fizer sentido, use essas ferramentas para montar links internos discovery://.
- Para gerar card clicavel pequeno no chat, produza markdown no formato [Titulo | Subtitulo | Meta](discovery://rota).
- Para botao interno simples, use [Abrir](discovery://rota).`

func resolveSystemPrompt(cfg Config) string {
	prompt := strings.TrimSpace(cfg.SystemPrompt)
	if prompt == "" {
		return defaultSystemPrompt
	}
	return prompt
}

// Send processes a user message: appends it to history, calls the LLM
// (possibly multiple rounds for tool calls), and returns the assistant reply.
func (s *Service) Send(ctx context.Context, userMessage string) (string, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	s.logf("mensagem recebida (%d chars)", len(strings.TrimSpace(userMessage)))

	if cfg.Endpoint == "" || cfg.APIKey == "" || cfg.Model == "" {
		return "", fmt.Errorf("configuracao de IA incompleta: defina endpoint, apiKey e model")
	}

	s.mu.Lock()
	s.history = append(s.history, Message{Role: "user", Content: userMessage})
	s.mu.Unlock()

	// Build tool definitions.
	tools := s.registry.OpenAIFunctions()
	s.logf("ferramentas disponiveis: %d", len(tools))

	// Allow up to 8 rounds of tool calling before forcing a final answer.
	const maxToolRounds = 8
	for round := 1; round <= maxToolRounds; round++ {
		s.logf("rodada de ferramentas %d/%d", round, maxToolRounds)
		s.mu.RLock()
		messages := s.buildMessages(resolveSystemPrompt(cfg))
		s.mu.RUnlock()

		resp, err := s.callLLM(ctx, cfg, messages, tools)
		if err != nil {
			return "", err
		}

		msg := resp.Choices[0].Message

		// If the LLM didn't request any tool calls, treat as final answer.
		if len(msg.ToolCalls) == 0 {
			s.logf("resposta final sem ferramentas")
			assistant := Message{Role: "assistant", Content: msg.Content}
			s.mu.Lock()
			s.history = append(s.history, assistant)
			s.mu.Unlock()
			return msg.Content, nil
		}

		s.logf("modelo solicitou %d chamada(s) de ferramenta", len(msg.ToolCalls))

		// Record the assistant message with tool calls.
		assistantMsg := Message{
			Role:      "assistant",
			Content:   msg.Content,
			ToolCalls: msg.ToolCalls,
		}
		s.mu.Lock()
		s.history = append(s.history, assistantMsg)
		s.mu.Unlock()

		// Execute each tool call and record results.
		for _, tc := range msg.ToolCalls {
			s.logf("chamando ferramenta: %s", tc.Function.Name)
			result, callErr := s.registry.Call(tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			var content string
			if callErr != nil {
				s.logf("ferramenta %s retornou erro: %v", tc.Function.Name, callErr)
				content = fmt.Sprintf("Erro: %v", callErr)
			} else {
				b, _ := json.Marshal(result)
				content = string(b)
				// Truncate very large results.
				if len(content) > 20000 {
					content = content[:20000] + "... (truncado)"
				}
				s.logf("ferramenta %s executada com sucesso", tc.Function.Name)
			}
			toolMsg := Message{
				Role:       "tool",
				Content:    content,
				ToolCallID: tc.ID,
			}
			s.mu.Lock()
			s.history = append(s.history, toolMsg)
			s.mu.Unlock()
		}
	}

	// Last attempt: ask for a direct answer without tools to avoid dead loops.
	s.logf("limite de rodadas atingido; tentando resposta final sem ferramentas")
	s.mu.RLock()
	messages := s.buildMessages(resolveSystemPrompt(cfg))
	s.mu.RUnlock()
	messages = append(messages, map[string]any{
		"role":    "user",
		"content": "Pare de chamar ferramentas e responda diretamente ao usuario com base no contexto atual.",
	})

	resp, err := s.callLLM(ctx, cfg, messages, nil)
	if err == nil && len(resp.Choices) > 0 {
		final := strings.TrimSpace(resp.Choices[0].Message.Content)
		if final != "" {
			s.logf("resposta final sem ferramentas obtida apos fallback")
			s.mu.Lock()
			s.history = append(s.history, Message{Role: "assistant", Content: final})
			s.mu.Unlock()
			return final, nil
		}
	}

	s.logf("falha: limite de chamadas de ferramentas excedido")

	return "", fmt.Errorf("limite de chamadas de ferramentas excedido")
}

func (s *Service) logf(format string, args ...any) {
	s.mu.RLock()
	logger := s.logger
	s.mu.RUnlock()
	if logger != nil {
		logger(fmt.Sprintf(format, args...))
	}
}

func (s *Service) buildMessages(systemPrompt string) []map[string]any {
	msgs := make([]map[string]any, 0, len(s.history)+1)
	msgs = append(msgs, map[string]any{"role": "system", "content": systemPrompt})

	for _, m := range s.history {
		entry := map[string]any{"role": m.Role}
		if m.Content != "" {
			entry["content"] = m.Content
		}
		if m.ToolCallID != "" {
			entry["tool_call_id"] = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			entry["tool_calls"] = m.ToolCalls
		}
		msgs = append(msgs, entry)
	}
	return msgs
}

// llmResponse is just enough of the OpenAI chat completion response.
type llmResponse struct {
	Choices []struct {
		Message struct {
			Role      string     `json:"role"`
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (s *Service) callLLM(ctx context.Context, cfg Config, messages []map[string]any, tools []map[string]any) (*llmResponse, error) {
	body := map[string]any{
		"model":    cfg.Model,
		"messages": messages,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("falha ao serializar request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cfg.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("falha ao criar request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha na chamada ao LLM: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("falha ao ler resposta do LLM: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM retornou status %d: %s", resp.StatusCode, string(data))
	}

	var result llmResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("falha ao decodificar resposta do LLM: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("erro do LLM: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("LLM retornou resposta vazia")
	}
	return &result, nil
}

// streamDeltaChunk is one SSE chunk from an OpenAI-compatible streaming response.
type streamDeltaChunk struct {
	Choices []struct {
		Delta struct {
			Role      string            `json:"role"`
			Content   string            `json:"content"`
			ToolCalls []streamToolDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type streamToolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// callLLMStream sends a request with stream:true and calls onToken for every text delta.
// Returns the full assembled content and any tool calls requested by the model.
// If the server returns regular JSON instead of SSE (provider fallback), it is parsed normally.
func (s *Service) callLLMStream(ctx context.Context, cfg Config, messages []map[string]any, tools []map[string]any, onToken func(string)) (string, []ToolCall, error) {
	body := map[string]any{
		"model":    cfg.Model,
		"messages": messages,
		"stream":   true,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("falha ao serializar request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cfg.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", nil, fmt.Errorf("falha ao criar request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("falha na chamada ao LLM: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("LLM retornou status %d: %s", resp.StatusCode, string(data))
	}

	// Fallback: some providers return application/json even when stream:true is requested.
	if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		data, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return "", nil, fmt.Errorf("falha ao ler resposta do LLM: %w", readErr)
		}
		var result llmResponse
		if jsonErr := json.Unmarshal(data, &result); jsonErr != nil {
			return "", nil, fmt.Errorf("falha ao decodificar resposta do LLM: %w", jsonErr)
		}
		if result.Error != nil {
			return "", nil, fmt.Errorf("erro do LLM: %s", result.Error.Message)
		}
		if len(result.Choices) == 0 {
			return "", nil, fmt.Errorf("LLM retornou resposta vazia")
		}
		msg := result.Choices[0].Message
		if onToken != nil && msg.Content != "" {
			onToken(msg.Content)
		}
		return msg.Content, msg.ToolCalls, nil
	}

	// Parse SSE stream line by line.
	var contentBuf strings.Builder
	toolCallsMap := make(map[int]*ToolCall)
	toolArgsMap := make(map[int]*strings.Builder)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 512*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk streamDeltaChunk
		if jsonErr := json.Unmarshal([]byte(data), &chunk); jsonErr != nil {
			continue
		}
		if chunk.Error != nil {
			return "", nil, fmt.Errorf("erro do LLM: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			contentBuf.WriteString(delta.Content)
			if onToken != nil {
				onToken(delta.Content)
			}
		}

		for _, tc := range delta.ToolCalls {
			idx := tc.Index
			if _, exists := toolCallsMap[idx]; !exists {
				toolCallsMap[idx] = &ToolCall{Type: "function"}
				toolArgsMap[idx] = &strings.Builder{}
			}
			if tc.ID != "" {
				toolCallsMap[idx].ID = tc.ID
			}
			if tc.Type != "" {
				toolCallsMap[idx].Type = tc.Type
			}
			if tc.Function.Name != "" {
				toolCallsMap[idx].Function.Name = tc.Function.Name
			}
			toolArgsMap[idx].WriteString(tc.Function.Arguments)
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return "", nil, fmt.Errorf("erro ao ler stream: %w", scanErr)
	}

	var toolCalls []ToolCall
	for i := 0; i < len(toolCallsMap); i++ {
		tc, ok := toolCallsMap[i]
		if !ok {
			continue
		}
		if args, ok2 := toolArgsMap[i]; ok2 {
			tc.Function.Arguments = args.String()
		}
		toolCalls = append(toolCalls, *tc)
	}

	return contentBuf.String(), toolCalls, nil
}

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
	s.mu.Unlock()
	s.logf("stream: mensagem recebida (%d chars)", len(strings.TrimSpace(userMessage)))

	if cfg.Endpoint == "" || cfg.APIKey == "" || cfg.Model == "" {
		return "", fmt.Errorf("configuracao de IA incompleta: defina endpoint, apiKey e model")
	}

	s.mu.Lock()
	s.history = append(s.history, Message{Role: "user", Content: userMessage})
	s.mu.Unlock()

	tools := s.registry.OpenAIFunctions()
	s.logf("stream: ferramentas disponiveis: %d", len(tools))

	const maxToolRounds = 8
	for round := 1; round <= maxToolRounds; round++ {
		if err := streamCtx.Err(); err != nil {
			return "", err
		}

		s.logf("stream: rodada %d/%d", round, maxToolRounds)

		s.mu.RLock()
		messages := s.buildMessages(resolveSystemPrompt(cfg))
		s.mu.RUnlock()

		content, toolCalls, err := s.callLLMStream(streamCtx, cfg, messages, tools, onToken)
		if err != nil {
			return "", err
		}

		if len(toolCalls) == 0 {
			s.logf("stream: resposta final sem ferramentas")
			s.mu.Lock()
			s.history = append(s.history, Message{Role: "assistant", Content: content})
			s.mu.Unlock()
			return content, nil
		}

		s.logf("stream: modelo solicitou %d chamada(s) de ferramenta", len(toolCalls))
		assistantMsg := Message{
			Role:      "assistant",
			Content:   content,
			ToolCalls: toolCalls,
		}
		s.mu.Lock()
		s.history = append(s.history, assistantMsg)
		s.mu.Unlock()

		for _, tc := range toolCalls {
			if err := streamCtx.Err(); err != nil {
				return "", err
			}

			s.logf("stream: chamando ferramenta: %s", tc.Function.Name)
			if onStatus != nil {
				onStatus("Executando: " + tc.Function.Name + "...")
			}
			result, callErr := s.registry.Call(tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			var toolContent string
			if callErr != nil {
				s.logf("stream: ferramenta %s retornou erro: %v", tc.Function.Name, callErr)
				toolContent = fmt.Sprintf("Erro: %v", callErr)
			} else {
				b, _ := json.Marshal(result)
				toolContent = string(b)
				if len(toolContent) > 20000 {
					toolContent = toolContent[:20000] + "... (truncado)"
				}
				s.logf("stream: ferramenta %s executada com sucesso", tc.Function.Name)
			}
			toolMsg := Message{
				Role:       "tool",
				Content:    toolContent,
				ToolCallID: tc.ID,
			}
			s.mu.Lock()
			s.history = append(s.history, toolMsg)
			s.mu.Unlock()
		}

		if onStatus != nil {
			onStatus("Preparando resposta...")
		}
	}

	s.logf("stream: limite de rodadas atingido")
	return "", fmt.Errorf("limite de chamadas de ferramentas excedido")
}

// TestConfig validates whether the provided configuration can reach the LLM.
func (s *Service) TestConfig(ctx context.Context, cfg Config) (string, error) {
	if cfg.Endpoint == "" || cfg.APIKey == "" || cfg.Model == "" {
		return "", fmt.Errorf("configuracao de IA incompleta: defina endpoint, apiKey e model")
	}

	messages := []map[string]any{
		{"role": "system", "content": "Responda apenas com OK."},
		{"role": "user", "content": "Teste de conectividade."},
	}

	resp, err := s.callLLM(ctx, cfg, messages, nil)
	if err != nil {
		return "", err
	}

	content := ""
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.Content
	}
	return content, nil
}

// Formatting helper functions for rich chat responses.
// These can be used by the service or called externally to build formatted messages.

// Bold wraps text in **bold** markdown.
func Bold(text string) string {
	return "**" + text + "**"
}

// Italic wraps text in *italic* markdown.
func Italic(text string) string {
	return "*" + text + "*"
}

// Code wraps text in inline `code` markdown.
func Code(text string) string {
	return "`" + text + "`"
}

// CodeBlock wraps text in a markdown code block with optional language.
func CodeBlock(code, language string) string {
	if language == "" {
		return "```\n" + code + "\n```"
	}
	return "```" + language + "\n" + code + "\n```"
}

// Warn creates a warning/important message block.
func Warn(message string) string {
	return "> ⚠️ " + message
}

// Tip creates a helpful tip block.
func Tip(message string) string {
	return "> 💡 " + message
}

// Note creates an informational note block.
func Note(message string) string {
	return "> ℹ️ " + message
}

// Success creates a success confirmation message.
func Success(message string) string {
	return "> ✅ " + message
}

// Heading creates a markdown heading (level 1-6).
func Heading(level int, text string) string {
	if level < 1 {
		level = 1
	}
	if level > 6 {
		level = 6
	}
	return strings.Repeat("#", level) + " " + text
}

// List creates a markdown bullet point list from strings.
func List(items ...string) string {
	var buf strings.Builder
	for _, item := range items {
		buf.WriteString("- " + item + "\n")
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// OrderedList creates a numbered list from strings.
func OrderedList(items ...string) string {
	var buf strings.Builder
	for i, item := range items {
		buf.WriteString(fmt.Sprintf("%d. %s\n", i+1, item))
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
