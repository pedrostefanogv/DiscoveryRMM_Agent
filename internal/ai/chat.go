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
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"winget-store/internal/mcp"
)

// Config holds the LLM API settings.
type Config struct {
	Endpoint     string `json:"endpoint"` // Agent base URL (e.g. "https://server") or explicit chat endpoint
	APIKey       string `json:"apiKey"`   // agent bearer token (mdz_...)
	Model        string `json:"model"`    // kept for compatibility; not used by AgentAuth backend
	SystemPrompt string `json:"systemPrompt"`
	MaxTokens    int    `json:"maxTokens"`
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
	mu        sync.RWMutex
	cfg       Config
	registry  *mcp.Registry
	history   []Message
	sessionID string
	logger    func(string)

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
	s.sessionID = ""
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
	sessionID := s.sessionID
	s.mu.Unlock()
	s.logf("mensagem recebida (%d chars)", len(strings.TrimSpace(userMessage)))

	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return "", fmt.Errorf("configuracao de IA incompleta: defina endpoint e token de agente")
	}
	if err := validateChatMessage(userMessage); err != nil {
		return "", err
	}

	s.mu.Lock()
	s.history = append(s.history, Message{Role: "user", Content: userMessage})
	s.mu.Unlock()

	resp, err := s.callAgentChatSync(ctx, cfg, userMessage, sessionID)
	if err != nil {
		return "", err
	}

	assistant := strings.TrimSpace(resp.AssistantMessage)
	if assistant == "" {
		assistant = "(sem resposta)"
	}

	s.mu.Lock()
	if strings.TrimSpace(resp.SessionID) != "" {
		s.sessionID = strings.TrimSpace(resp.SessionID)
	}
	s.history = append(s.history, Message{Role: "assistant", Content: assistant})
	s.mu.Unlock()

	return assistant, nil
}

type agentChatRequest struct {
	Message   string  `json:"message"`
	SessionID *string `json:"sessionId,omitempty"`
	MaxTokens *int    `json:"maxTokens,omitempty"`
}

type agentChatSyncResponse struct {
	SessionID               string `json:"sessionId"`
	AssistantMessage        string `json:"assistantMessage"`
	TokensUsed              int    `json:"tokensUsed"`
	ConversationTokensTotal int    `json:"conversationTokensTotal"`
	LatencyMs               int    `json:"latencyMs"`
}

type agentChatStreamEvent struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	SessionID string `json:"sessionId"`
	Error     string `json:"error"`
	LatencyMs int    `json:"latencyMs"`
}

func (s *Service) buildAgentChatRequest(message, sessionID string, maxTokens int) agentChatRequest {
	req := agentChatRequest{Message: message}
	if strings.TrimSpace(sessionID) != "" {
		tmp := strings.TrimSpace(sessionID)
		req.SessionID = &tmp
	}
	if maxTokens > 0 {
		tmp := maxTokens
		req.MaxTokens = &tmp
	}
	return req
}

func normalizeAgentChatBaseURL(endpoint string) (string, error) {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return "", fmt.Errorf("endpoint do chat nao informado")
	}
	u, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(u.Scheme) == "" || strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("endpoint de chat invalido")
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

func (s *Service) callAgentChatSync(ctx context.Context, cfg Config, message, sessionID string) (*agentChatSyncResponse, error) {
	baseURL, err := normalizeAgentChatBaseURL(cfg.Endpoint)
	if err != nil {
		return nil, err
	}

	requestBody := s.buildAgentChatRequest(message, sessionID, cfg.MaxTokens)
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("falha ao serializar request de chat: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	endpoint := baseURL + "/api/agent-auth/me/ai-chat"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("falha ao criar request de chat: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.APIKey))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao chamar chat: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("falha ao ler resposta de chat: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusRequestTimeout {
			return nil, fmt.Errorf("chat expirou (timeout): %s", strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("chat retornou status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result agentChatSyncResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("falha ao decodificar resposta de chat: %w", err)
	}
	return &result, nil
}

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
		// O backend deveria streamar; se respondeu JSON, tratamos como fallback.
		return "", "", false, fmt.Errorf("resposta sem SSE (content-type: %s)", contentType)
	}

	var contentBuf strings.Builder
	currentSessionID := ""
	hasToken := false

	scanner := bufio.NewScanner(resp.Body)
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

var blockedMessagePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<script[^>]*>`),
	regexp.MustCompile(`(?i)javascript:`),
	regexp.MustCompile(`(?i)eval\s*\(`),
	regexp.MustCompile(`(?i)on[a-z]+\s*=`),
	regexp.MustCompile(`(?i)<iframe[^>]*>`),
	regexp.MustCompile(`(?i)<object[^>]*>`),
	regexp.MustCompile(`(?i)<embed[^>]*>`),
}

func validateChatMessage(message string) error {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return fmt.Errorf("mensagem obrigatoria")
	}
	if len([]byte(trimmed)) > 2048 {
		return fmt.Errorf("mensagem excede 2048 bytes UTF-8")
	}
	if !utf8.ValidString(trimmed) {
		return fmt.Errorf("mensagem invalida: UTF-8 incorreto")
	}
	for _, pattern := range blockedMessagePatterns {
		if pattern.MatchString(trimmed) {
			return fmt.Errorf("mensagem contem padrao bloqueado")
		}
	}
	return nil
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

// TestConfig validates whether the provided configuration can reach the LLM.
func (s *Service) TestConfig(ctx context.Context, cfg Config) (string, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return "", fmt.Errorf("configuracao de IA incompleta: defina endpoint e token de agente")
	}

	resp, err := s.callAgentChatSync(ctx, cfg, "Teste de conectividade. Responda apenas com OK.", "")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.AssistantMessage), nil
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
