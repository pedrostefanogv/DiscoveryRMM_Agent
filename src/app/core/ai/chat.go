// Package ai provides an AI chat service that uses an OpenAI-compatible API
// with function calling backed by the MCP tool registry.
package ai

import (
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

	"discovery/app/netutil"
	"discovery/app/core/mcp"
	"discovery/app/core/tlsutil"
)

// Config holds the LLM API settings.
type Config struct {
	Endpoint     string `json:"endpoint"` // Agent base URL (e.g. "https://server") or explicit chat endpoint
	APIKey       string `json:"apiKey"`   // agent bearer token (mdz_...)
	AgentID      string `json:"agentId"`
	Model        string `json:"model"` // kept for compatibility; not used by AgentAuth backend
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

	chatLogger *ChatLogger

	// a2uiActionMu protege a ação A2UI pendente (userAction) que será enviada
	// ao LLM no próximo round do loop multi-round.
	a2uiActionMu sync.Mutex
	a2uiAction   *A2uiAction
}

// A2uiAction representa uma ação do usuário em uma surface A2UI.
type A2uiAction struct {
	SurfaceID string
	Name      string
	Context   map[string]any
}

// NewService creates a chat service.
func NewService(registry *mcp.Registry) *Service {
	return &Service{
		registry: registry,
		history:  []Message{},
	}
}

// SubmitA2uiAction registra uma ação do usuário em uma surface A2UI. A ação é
// consumida pelo próximo round do loop multi-round (SendStreamMultiRound) e
// enviada ao LLM como um tool result, permitindo que o agente reaja ao clique.
func (s *Service) SubmitA2uiAction(surfaceID, name string, context map[string]any) {
	s.a2uiActionMu.Lock()
	defer s.a2uiActionMu.Unlock()
	s.a2uiAction = &A2uiAction{SurfaceID: surfaceID, Name: name, Context: context}
}

// takeA2uiAction consome e retorna a ação A2UI pendente (se houver).
func (s *Service) takeA2uiAction() *A2uiAction {
	s.a2uiActionMu.Lock()
	defer s.a2uiActionMu.Unlock()
	action := s.a2uiAction
	s.a2uiAction = nil
	return action
}

// peekA2uiAction retorna a ação A2UI pendente SEM consumi-la (se houver).
func (s *Service) peekA2uiAction() *A2uiAction {
	s.a2uiActionMu.Lock()
	defer s.a2uiActionMu.Unlock()
	return s.a2uiAction
}

// SetLogger configures an optional callback for chat diagnostics.
func (s *Service) SetLogger(logger func(string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = logger
}

// SetChatLogger configura o logger JSONL dedicado para logs de chat.
func (s *Service) SetChatLogger(cl *ChatLogger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chatLogger = cl
}

// IsChatLogEnabled retorna true se o log de chat JSONL está ativo.
func (s *Service) IsChatLogEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.chatLogger == nil {
		return false
	}
	return s.chatLogger.IsEnabled()
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
	// Limpa também a ação A2UI pendente (se o usuário clicou num botão e depois
	// limpou o chat, a ação não deve ser processada na próxima mensagem).
	s.a2uiActionMu.Lock()
	s.a2uiAction = nil
	s.a2uiActionMu.Unlock()
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

=== REGRA MAIS IMPORTANTE — USE SEMPRE AS FERRAMENTAS ===
Voce possui ferramentas (tools) para consultar e agir diretamente na maquina do usuario.
NUNCA peca ao usuario para abrir PowerShell, Prompt de Comando, Painel de Controle, Explorador de Arquivos ou Executar manualmente qualquer comando.
SEMPRE use as ferramentas disponiveis para verificar fatos, buscar informacoes ou executar acoes na maquina.
Se o usuario perguntar "o X esta instalado?", use list_installed_packages ou get_inventory — nunca diga "va em Painel de Controle > Programas".
Se o usuario pedir para instalar algo, use search_packages primeiro, depois install_package — nunca diga "baixe do site".
Voce e um assistente automatizado que age diretamente na maquina. Aja como tal.

=== REGRA OBRIGATORIA — ARGUMENTOS DAS FERRAMENTAS ===
Toda ferramenta que possui parametros obrigatorios (required: true) DEVE ser chamada com esses parametros preenchidos.
NUNCA chame uma ferramenta com argumentos vazios, null ou ausentes se ela exige parametros obrigatorios.
Se receber um erro mencionando "parametro obrigatorio", corrija os argumentos e tente novamente com os valores corretos — NAO reenvie a mesma chamada com argumentos vazios.

Exemplos de chamadas CORRETAS:
- search_packages: SEMPRE envie {"query": "<termo de busca>"} — NUNCA chame sem "query"
- install_package: SEMPRE envie {"id": "<ID do pacote>"} — NUNCA chame sem "id"
- create_ticket: SEMPRE envie {"title": "<titulo>", "description": "<descricao>"} — NUNCA chame sem ambos
- get_agent_info: pode chamar sem argumentos (nao tem parametros obrigatorios)
- list_installed_packages: pode chamar sem argumentos (nao tem parametros obrigatorios)

=== LOJA DE APLICATIVOS (APP STORE) ===
O Discovery possui uma loja interna de aplicativos gerenciada (discovery://store), com um catalogo de programas aprovados pela empresa.
O fluxo correto para instalar programas e:
1. search_packages(query) — busca o programa no catalogo winget
2. Mostre os resultados ao usuario e aguarde a confirmacao do ID correto
3. install_package(id) — instala o programa
4. Confirme a instalacao com detalhes (nome, versao)

Para verificar o que ja esta instalado: use list_installed_packages (retorna todos os programas detectados pelo winget).
Para ver atualizacoes pendentes: use get_pending_updates.

=== FERRAMENTAS DISPONIVEIS ===

**Inventario e Sistema:**
- get_inventory — inventario completo: hardware, SO, discos, rede, memoria, GPU, bateria, BitLocker, software instalado, usuarios logados
- export_inventory_markdown — exporta relatorio em Markdown
- export_inventory_pdf — exporta relatorio em PDF
- get_osquery_status — verifica se o osquery esta instalado
- get_logs — logs recentes de operacoes do winget
- query_event_log — consulta o Windows Event Log (System, Application, Setup) com filtros de nivel, fonte e periodo

**Pacotes e Programas (Winget):**
- list_installed_packages — lista todos os programas instalados detectados pelo winget
- search_packages(query) — pesquisa programas no catalogo winget (ate 20 resultados)
- install_package(id) — instala um programa pelo ID winget (ex: "Mozilla.Firefox")
- uninstall_package(id) — desinstala um programa pelo ID
- upgrade_package(id) — atualiza um programa especifico
- upgrade_all_packages — atualiza todos os programas com atualizacao disponivel
- get_pending_updates — lista programas com atualizacoes pendentes (versao atual vs disponivel)
- get_package_actions — mapa de acoes disponiveis por pacote (install, uninstall, upgrade)

**Impressoras:**
- list_printers — lista impressoras instaladas
- install_printer(name, driverName, portName, portAddress?) — instala uma impressora local/TCP-IP
- install_shared_printer(connectionPath, setDefault?) — instala impressora compartilhada (UNC)
- remove_printer(name) — remove uma impressora
- get_printer_config(printerName) — consulta configuracao de uma impressora
- list_print_jobs(printerName) — lista jobs na fila
- remove_print_job(printerName, jobId) — cancela um job
- spooler_status — status do servico Spooler
- restart_spooler — reinicia o Spooler
- clear_queue(printerName) — limpa a fila de uma impressora
- list_drivers — lista drivers de impressora instalados

**Chamados de Suporte:**
IMPORTANTE — SEMPRE que o usuario pedir para abrir chamado, ticket, reportar problema ou solicitar suporte, use as ferramentas abaixo. NUNCA oriente o usuario a acessar portal web, ligar para central ou enviar e-mail.
Fluxo correto para criar um chamado:
1. get_agent_info — obtenha hostname, IP, SO e versao da maquina
2. Monte o titulo no formato "<problema> — <hostname>" (ex: "Computador lento — DESKTOP-XPTO")
3. Na descricao, inclua automaticamente os dados da maquina (hostname, SO, IP) alem do problema relatado
4. Escolha a prioridade: 1=Baixa (duvidas gerais), 2=Media (problemas parciais), 3=Alta (impede trabalho), 4=Critica (sistema parado)
5. create_ticket(title, description, priority, category) — crie o chamado e mostre o numero do protocolo
- list_tickets — lista chamados de suporte deste agente/maquina
- get_ticket_details(ticketId) — detalhes de um chamado especifico
- create_ticket(title, description, priority?, category?) — abre um novo chamado vinculado automaticamente a esta maquina
- add_ticket_comment(ticketId, content, isInternal?) — adiciona comentario em um chamado

**Rede:**
- ping_host(host, count?, timeoutSeconds?) — verifica se um host esta online (apenas redes privadas)
- flush_dns — limpa o cache DNS (ipconfig /flushdns)

**Memorias Locais:**
- memory/list — lista anotacoes locais do agente
- memory/create(content) — cria uma nova anotacao
- memory/delete(id) — remove uma anotacao pelo ID

**Navegacao Interna:**
- get_internal_navigation_routes — lista rotas disponiveis (store, updates, inventory, tickets, logs, chat, knowledge, debug)
- build_internal_navigation_link(target, title?, subtitle?, meta?) — monta link discovery:// clicavel para abrir telas do app

=== REGRAS DE COMPORTAMENTO ===
1. Faca somente o que o usuario pedir; nao execute nada extra por conta propria.
2. Para QUALQUER pergunta sobre fatos da maquina ("esta instalado?", "qual versao?", "quantos GB?"), use as ferramentas — NUNCA de instrucoes manuais.
3. Antes de instalar, desinstalar ou atualizar, sempre confirme o ID correto (use search_packages ou list_installed_packages) e peca aprovacao do usuario.
4. Peca aprovacao explicita antes de qualquer acao que altere o computador. Explique em uma frase simples o que sera feito e aguarde confirmacao.
5. Ao mostrar dados, resuma as informacoes mais relevantes em linguagem clara; nao despeje dados brutos.
6. Quando uma acao for concluida, confirme de forma acolhedora com detalhes uteis (nome e versao do programa, etc.).

=== EXEMPLOS DE COMO RESPONDER ===

Usuario: "O Firefox esta instalado?"
Resposta correta: [Chame list_installed_packages, filtre por Firefox, informe se esta instalado e qual versao]
Resposta ERRADA: "Abra o Painel de Controle > Programas e Recursos..." ou "Execute Get-WmiObject no PowerShell..."

Usuario: "Instala o Chrome"
Resposta correta: [Chame search_packages("Chrome"), mostre os resultados, pergunte qual ID instalar]
Resposta ERRADA: "Va ate o site google.com/chrome e baixe o instalador..."

Usuario: "Quanta memoria RAM tem?"
Resposta correta: [Chame get_inventory, extraia o campo de memoria, responda com o valor]
Resposta ERRADA: "Abra o Gerenciador de Tarefas > Desempenho..."

Usuario: "Abre um chamado, o computador esta muito lento"
Resposta correta: [Chame get_agent_info para obter hostname e dados da maquina, depois chame create_ticket com titulo "Computador lento — <hostname>", descricao detalhada do problema incluindo SO e versao, e prioridade adequada]
Resposta ERRADA: "Acesse o portal de suporte..." ou "Abra o navegador e va ate a pagina de help desk..." ou "Envie um e-mail para o suporte..."

Usuario: "Registra um ticket: nao consigo acessar a VPN"
Resposta correta: [Chame get_agent_info, depois create_ticket com category="VPN", prioridade 3 (Alta), titulo "Falha de acesso VPN — <hostname>", descricao com detalhes do erro]
Resposta ERRADA: "Ligue para a central de suporte..." ou "Voce precisa acessar o portal pelo navegador..."

=== RECURSOS DE FORMATACAO ===
Voce pode usar Markdown para enriquecer suas respostas:
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

=== BOTOES INTERATIVOS ===
O chat possui botoes dinamicos. Qualquer linha da sua resposta que comece com "- " ou "* " sera exibida como um botao clicavel para o usuario. Use esse recurso sempre que fizer sentido para facilitar a interacao:
- Ao oferecer opcoes ou escolhas, liste cada alternativa em sua propria linha com "- " no inicio (maximo 6 opcoes). Escreva cada opcao de forma curta e direta, pois o texto vira o rotulo do botao.
- Ao pedir confirmacao, inclua opcoes como "- Sim, pode prosseguir" e "- Nao, cancelar" para que o usuario responda com um clique.
- Ao sugerir proximos passos apos uma acao concluida, liste as sugestoes com "- " para que tambem virem botoes.
Nunca use "- " para informacoes descritivas que nao sejam opcoes clicaveis; use frases corridas ou paragrafos para explicacoes.

=== PERGUNTAS INTERATIVAS (ask_user) ===
Voce tem a ferramenta ask_user para fazer perguntas bloqueantes ao usuario — use APENAS quando realmente precisar de input obrigatorio para continuar.
Exemplos de quando usar ask_user:
- "Qual dos 3 resultados de busca voce quer instalar?" com options=['Google Chrome','Firefox','Brave'] e allowText='true'
- "Confirma a instalacao do Google Chrome v131?" com options=['Sim, instalar','Nao, cancelar']
- "Qual o IP da impressora que voce quer configurar?" com options=[] (so texto livre)

Regras:
- SEMPRE forneca options quando houver alternativas claras (max 6)
- SEMPRE defina allowText='true' se o usuario puder precisar digitar algo fora das opcoes
- Para confirmacoes simples use botoes "- " (NAO bloqueiam). Use ask_user so quando bloquear for realmente necessario
- A pergunta deve ser clara e direta, sem jargoes

=== NAVEGACAO INTERNA DO APP ===
- Use get_internal_navigation_routes para ver as telas disponiveis e build_internal_navigation_link para gerar links clicaveis.
- Sempre que fizer sentido, ofereca links discovery:// para telas relevantes (Store, Updates, Tickets, Inventory).
- Para card clicavel: [Titulo | Subtitulo | Meta](discovery://rota)
- Para botao simples: [Abrir](discovery://rota)
- A Loja de Aplicativos fica em discovery://store — ofereca esse link quando o usuario quiser explorar programas disponiveis.`

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
		err := fmt.Errorf("configuracao de IA incompleta: defina endpoint e token de agente")
		s.logChatEntry(ChatLogEntry{
			Type:    "chat_request",
			Method:  "sync",
			Error:   err.Error(),
			UserMsg: TruncateForLog(userMessage, 2000),
		})
		return "", err
	}
	if err := validateChatMessage(userMessage); err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:    "chat_request",
			Method:  "sync",
			Error:   err.Error(),
			UserMsg: TruncateForLog(userMessage, 2000),
		})
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
	Message   string           `json:"message"`
	SessionID *string          `json:"sessionId,omitempty"`
	MaxTokens *int             `json:"maxTokens,omitempty"`
	Tools     []map[string]any `json:"tools,omitempty"`
}

type agentChatSyncResponse struct {
	SessionID               string `json:"sessionId"`
	AssistantMessage        string `json:"assistantMessage"`
	TokensUsed              int    `json:"tokensUsed"`
	ConversationTokensTotal int    `json:"conversationTokensTotal"`
	LatencyMs               int    `json:"latencyMs"`
}

// agentChatStreamEvent está definido em chat_stream.go

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
	s.mu.RLock()
	if s.registry != nil {
		tools := s.registry.OpenAIFunctions()
		if len(tools) > 0 {
			req.Tools = tools
		}
	}
	s.mu.RUnlock()
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
	idx := strings.Index(path, "/api/v1/agent-auth")
	if idx >= 0 {
		u.Path = path[:idx]
		u.RawQuery = ""
		u.Fragment = ""
		return strings.TrimRight(u.String(), "/"), nil
	}
	if strings.Contains(path, "/api/") {
		return "", fmt.Errorf("endpoint deve apontar para a base do servidor ou /api/v1/agent-auth")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (s *Service) callAgentChatSync(ctx context.Context, cfg Config, message, sessionID string) (*agentChatSyncResponse, error) {
	startTime := time.Now()
	baseURL, err := normalizeAgentChatBaseURL(cfg.Endpoint)
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:    "chat_request",
			Method:  "sync",
			Error:   err.Error(),
			UserMsg: TruncateForLog(message, 2000),
		})
		return nil, err
	}

	requestBody := s.buildAgentChatRequest(message, sessionID, cfg.MaxTokens)
	payload, err := json.Marshal(requestBody)
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:    "chat_request",
			Method:  "sync",
			Error:   fmt.Sprintf("marshal error: %v", err),
			UserMsg: TruncateForLog(message, 2000),
		})
		return nil, fmt.Errorf("falha ao serializar request de chat: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	endpoint := baseURL + "/api/v1/agent-auth/me/ai-chat"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:       "chat_request",
			Method:     "sync",
			Endpoint:   endpoint,
			MessageLen: len(message),
			SessionID:  sessionID,
			Error:      fmt.Sprintf("request create error: %v", err),
		})
		return nil, fmt.Errorf("falha ao criar request de chat: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.APIKey, cfg.AgentID); err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:       "chat_request",
			Method:     "sync",
			Endpoint:   endpoint,
			MessageLen: len(message),
			SessionID:  sessionID,
			Error:      fmt.Sprintf("auth header error: %v", err),
		})
		return nil, err
	}

	resp, err := tlsutil.NewHTTPClient(120 * time.Second).Do(req)
	elapsed := time.Since(startTime)
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:       "chat_response",
			Method:     "sync",
			Endpoint:   endpoint,
			MessageLen: len(message),
			SessionID:  sessionID,
			Error:      fmt.Sprintf("request failed: %v", err),
			LatencyMs:  int(elapsed.Milliseconds()),
		})
		return nil, fmt.Errorf("falha ao chamar chat: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:       "chat_response",
			Method:     "sync",
			Endpoint:   endpoint,
			MessageLen: len(message),
			SessionID:  sessionID,
			StatusCode: resp.StatusCode,
			Error:      fmt.Sprintf("read body error: %v", err),
			LatencyMs:  int(elapsed.Milliseconds()),
		})
		return nil, fmt.Errorf("falha ao ler resposta de chat: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		s.logChatEntry(ChatLogEntry{
			Type:       "chat_response",
			Method:     "sync",
			Endpoint:   endpoint,
			MessageLen: len(message),
			SessionID:  sessionID,
			StatusCode: resp.StatusCode,
			Error:      strings.TrimSpace(string(body)),
			LatencyMs:  int(elapsed.Milliseconds()),
		})
		if resp.StatusCode == http.StatusRequestTimeout {
			return nil, fmt.Errorf("chat expirou (timeout): %s", strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("chat retornou status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result agentChatSyncResponse
	if err := json.Unmarshal(body, &result); err != nil {
		s.logChatEntry(ChatLogEntry{
			Type:       "chat_response",
			Method:     "sync",
			Endpoint:   endpoint,
			MessageLen: len(message),
			SessionID:  sessionID,
			StatusCode: resp.StatusCode,
			Error:      fmt.Sprintf("parse error: %v", err),
			LatencyMs:  int(elapsed.Milliseconds()),
		})
		return nil, fmt.Errorf("falha ao decodificar resposta de chat: %w", err)
	}

	// Log de sucesso com detalhes da resposta
	s.logChatEntry(ChatLogEntry{
		Type:        "chat_response",
		Method:      "sync",
		Endpoint:    endpoint,
		MessageLen:  len(message),
		SessionID:   result.SessionID,
		StatusCode:  resp.StatusCode,
		TokensUsed:  result.TokensUsed,
		LatencyMs:   int(elapsed.Milliseconds()),
		ResponseLen: len(result.AssistantMessage),
		UserMsg:     TruncateForLog(message, 2000),
		Assistant:   TruncateForLog(result.AssistantMessage, 4000),
	})
	return &result, nil
}

// callAgentChatStream e SendStream estão definidas em chat_stream.go

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

// logChatEntry escreve uma entrada no log JSONL de chat, se o logger estiver ativo.
func (s *Service) logChatEntry(entry ChatLogEntry) {
	s.mu.RLock()
	cl := s.chatLogger
	s.mu.RUnlock()
	if cl != nil {
		cl.Log(entry)
	}
}

// LogChatEntry é a versão pública de logChatEntry para uso externo (ex.: chat_bridge).
func (s *Service) LogChatEntry(entry ChatLogEntry) {
	s.logChatEntry(entry)
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

// SendStream está definida em chat_stream.go

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
		buf.WriteString("- ")
		buf.WriteString(item)
		buf.WriteByte('\n')
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
