package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"discovery/app/core/ai"
	"discovery/app/core/consent"
	"discovery/app/core/mcp"
	"discovery/app/core/platform"
	"discovery/app/netutil"
)

// Config is the frontend-facing AI configuration.
type Config struct {
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"apiKey"`
	Model        string `json:"model"`
	SystemPrompt string `json:"systemPrompt"`
	MaxTokens    int    `json:"maxTokens"`
	// NotifyPreview controla se o toast de resposta concluída mostra um trecho
	// da resposta (padrão true). nil = campo ausente no arquivo persistido
	// (config antiga) e mantém o padrão; false = corpo genérico, para não
	// expor conteúdo do chat no Action Center/tela de bloqueio.
	NotifyPreview *bool `json:"notifyPreview,omitempty"`
}

// Message is a single message for the frontend.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Question represents an interactive question sent to the user.
type Question struct {
	ID        string   `json:"id"`
	Question  string   `json:"question"`
	Options   []string `json:"options,omitempty"`
	AllowText bool     `json:"allowText"`
}

// QuestionAnswer is the user's response to a Question.
type QuestionAnswer struct {
	QuestionID string `json:"questionId"`
	Answer     string `json:"answer"`
}

// Deps are the dependencies injected into the ChatService.
type Deps struct {
	// Ctx returns the application context.
	Ctx func() context.Context
	// Logf appends a log line.
	Logf func(string)
	// GetDebugConfig returns the debug config.
	GetDebugConfig func() DebugConfig
	// GetAgentConfiguration returns the agent configuration.
	GetAgentConfiguration func() AgentConfiguration
	// BeginActivity marks the start of an activity (idle mode).
	BeginActivity func(string) func()
	// EmitEvent emits a Wails event to the frontend.
	EmitEvent func(string, ...any)
	// PublishChatEvent publishes a chat event to SSE subscribers.
	PublishChatEvent func(string, string)
	// OnAssistantResponseComplete é chamado quando um turno de chat termina
	// com sucesso e a resposta final está pronta para exibição (após
	// chat:done). Recebe o texto final do assistente. Usado pelo App para
	// notificar o usuário quando a aba de chat não está visível em tela.
	OnAssistantResponseComplete func(content string)
	// OnAssistantResponseFailed é chamado quando um turno de chat termina em
	// erro real DURANTE o processamento (o usuário estava esperando a resposta)
	// — não em cancelamento e não em recusa por turno em andamento. Rejeições
	// instantâneas anteriores ao turno (IA desabilitada, config incompleta) não
	// passam por aqui: o usuário acabou de enviar e vê o erro na hora.
	OnAssistantResponseFailed func(errMsg string)
	// SafeGo runs a function in a safe goroutine.
	SafeGo func(func())
	// ChatConfigFile is the config file name.
	ChatConfigFile string
	// RequestToolConsent pede autorização do USUÁRIO antes de executar uma
	// ação com efeito no computador (ver core/mcp.ToolConsentFor). nil =
	// indisponível: tools que exigem consentimento são recusadas por segurança.
	RequestToolConsent func(ctx context.Context, req consent.Request) (bool, error)
}

// DebugConfig is a minimal view of the debug config used by chat.
type DebugConfig struct {
	AgentID   string
	ApiScheme string
	ApiServer string
	AuthToken string
}

// AgentConfiguration is a minimal view used by chat.
type AgentConfiguration struct {
	ChatAIEnabled *bool
}

// Service encapsulates the chat domain logic.
type Service struct {
	chatSvc     *ai.Service
	mcpRegistry *mcp.Registry

	ctx              func() context.Context
	logf             func(string)
	getDebugConfig   func() DebugConfig
	getAgentConfig   func() AgentConfiguration
	beginActivity    func(string) func()
	emitEvent        func(string, ...any)
	publishChatEvent func(string, string)
	// onAssistantResponseComplete notifica o App que a resposta final do turno
	// ficou pronta (nil = desabilitado).
	onAssistantResponseComplete func(content string)
	onAssistantResponseFailed   func(errMsg string)
	safeGo                      func(func())
	chatConfigFile              string
	requestToolConsent          func(ctx context.Context, req consent.Request) (bool, error)

	// notifyPreview é a preferência de privacidade do toast (padrão true).
	notifyPreviewMu sync.RWMutex
	notifyPreview   bool

	toolsRegistrationMu   sync.RWMutex
	lastToolsRegistration time.Time
}

// New creates a ChatService.
func New(reg *mcp.Registry, deps Deps) *Service {
	return &Service{
		chatSvc:                     ai.NewService(reg),
		mcpRegistry:                 reg,
		ctx:                         deps.Ctx,
		logf:                        deps.Logf,
		getDebugConfig:              deps.GetDebugConfig,
		getAgentConfig:              deps.GetAgentConfiguration,
		beginActivity:               deps.BeginActivity,
		emitEvent:                   deps.EmitEvent,
		publishChatEvent:            deps.PublishChatEvent,
		onAssistantResponseComplete: deps.OnAssistantResponseComplete,
		onAssistantResponseFailed:   deps.OnAssistantResponseFailed,
		safeGo:                      deps.SafeGo,
		chatConfigFile:              deps.ChatConfigFile,
		requestToolConsent:          deps.RequestToolConsent,
		// Padrão de privacidade: mostrar prévia (comportamento anterior). O
		// installer/config do chat pode desligar.
		notifyPreview: true,
	}
}

// NotifyPreviewEnabled informa se o toast de resposta pode mostrar um trecho da
// resposta. Seguro para chamada concorrente (o envio roda em goroutine).
func (s *Service) NotifyPreviewEnabled() bool {
	if s == nil {
		return false
	}
	s.notifyPreviewMu.RLock()
	defer s.notifyPreviewMu.RUnlock()
	return s.notifyPreview
}

// setNotifyPreview aplica a preferência persistida (nil = mantém a atual).
func (s *Service) setNotifyPreview(value *bool) {
	if s == nil || value == nil {
		return
	}
	s.notifyPreviewMu.Lock()
	s.notifyPreview = *value
	s.notifyPreviewMu.Unlock()
}

// Service returns the underlying ai.Service (for advanced use).
func (s *Service) Service() *ai.Service { return s.chatSvc }

// Registry returns the MCP registry.
func (s *Service) Registry() *mcp.Registry { return s.mcpRegistry }

func (s *Service) configPathCandidates() []string {
	return platform.ChatConfigPathCandidates(s.chatConfigFile)
}

// maskedAPIKey é o sentinel devolvido por GetConfig no lugar da chave real
// (B2). O save com este valor (ou vazio) preserva a chave armazenada — o
// frontend não pré-preenche o campo e enviar vazio significaria apagar.
const maskedAPIKey = "********"

// maskAPIKey retorna o sentinel quando a chave não está vazia.
func maskAPIKey(key string) string {
	if strings.TrimSpace(key) == "" {
		return ""
	}
	return maskedAPIKey
}

// LoadPersistedConfig loads the persisted chat config.
func (s *Service) LoadPersistedConfig() {
	for _, path := range s.configPathCandidates() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			s.logf("[chat] falha ao ler configuração persistida: " + err.Error())
			return
		}
		if cfg.MaxTokens < 0 {
			cfg.MaxTokens = 0
		}
		s.setNotifyPreview(cfg.NotifyPreview)
		s.chatSvc.SetConfig(ai.Config{
			Endpoint:     cfg.Endpoint,
			APIKey:       cfg.APIKey,
			AgentID:      s.getDebugConfig().AgentID,
			Model:        cfg.Model,
			SystemPrompt: cfg.SystemPrompt,
			MaxTokens:    cfg.MaxTokens,
		})
		s.logf("[chat] configuração carregada de " + path)
		return
	}
}

func (s *Service) persistConfig(cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("falha ao serializar configuração do chat: %w", err)
	}
	var errs []string
	for _, path := range s.configPathCandidates() {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			errs = append(errs, path+": "+err.Error())
			continue
		}
		// A3: endurece a DACL do arquivo quando o processo é elevado — o
		// 0o600 é no-op no Windows e o arquivo herda Users:(M) do diretório.
		if platform.IsElevated() {
			if aclErr := platform.HardenSecretFileACL(path); aclErr != nil {
				s.logf("[chat] aviso: nao foi possivel restringir ACL de " + path + ": " + aclErr.Error())
			}
		}
		s.logf("[chat] configuração salva em " + path)
		return nil
	}
	if len(errs) == 0 {
		return fmt.Errorf("nenhum caminho válido para salvar configuração do chat")
	}
	return fmt.Errorf("falha ao salvar configuração do chat: %s", strings.Join(errs, " | "))
}

// SetConfig updates and persists the LLM API settings.
// B2: o frontend nunca recebe a chave real (Get mascara). Save sem redigitar
// chega com vazio ou com o sentinel — preserva a chave atual nesses casos
// (antes, salvar outros campos apagava a chave armazenada).
func (s *Service) SetConfig(cfg Config) error {
	if cfg.MaxTokens < 0 {
		return fmt.Errorf("maxTokens invalido: use 0 ou um valor positivo")
	}
	if key := strings.TrimSpace(cfg.APIKey); key == "" || key == maskedAPIKey {
		cfg.APIKey = s.chatSvc.GetConfig().APIKey
	}
	if cfg.NotifyPreview == nil {
		// UI antiga (sem o campo) ou save parcial: preserva a preferência atual
		// em vez de resetar o padrão.
		enabled := s.NotifyPreviewEnabled()
		cfg.NotifyPreview = &enabled
	}
	s.setNotifyPreview(cfg.NotifyPreview)
	s.chatSvc.SetConfig(ai.Config{
		Endpoint:     cfg.Endpoint,
		APIKey:       cfg.APIKey,
		AgentID:      s.getDebugConfig().AgentID,
		Model:        cfg.Model,
		SystemPrompt: cfg.SystemPrompt,
		MaxTokens:    cfg.MaxTokens,
	})
	return s.persistConfig(cfg)
}

// TestConfig checks whether the informed LLM settings are valid without saving them.
func (s *Service) TestConfig(cfg Config) (string, error) {
	// B2: teste com o sentinel usa a chave real armazenada.
	if strings.TrimSpace(cfg.APIKey) == maskedAPIKey {
		cfg.APIKey = s.chatSvc.GetConfig().APIKey
	}
	runtimeCfg, err := s.resolveRuntimeConfig(cfg)
	if err != nil {
		return "", err
	}
	return s.chatSvc.TestConfig(s.ctx(), runtimeCfg)
}

// GetConfig returns the current config (API key masked).
// B2: a chave é substituída pelo sentinel — o comentário antigo dizia
// "masked" mas devolvia a chave integral (leak para frontend/debug HTTP).
func (s *Service) GetConfig() Config {
	c := s.chatSvc.GetConfig()
	preview := s.NotifyPreviewEnabled()
	return Config{
		Endpoint:      c.Endpoint,
		APIKey:        maskAPIKey(c.APIKey),
		Model:         c.Model,
		SystemPrompt:  c.SystemPrompt,
		MaxTokens:     c.MaxTokens,
		NotifyPreview: &preview,
	}
}

// SendMessage sends a user message and returns the assistant response.
func (s *Service) SendMessage(message string) (string, error) {
	done := s.beginActivity("chat IA")
	defer done()

	if cfg := s.getAgentConfig(); cfg.ChatAIEnabled != nil && !*cfg.ChatAIEnabled {
		return "", fmt.Errorf("Chat AI desabilitado pela configuração do servidor")
	}
	s.ensureToolsRegistered()
	current := s.chatSvc.GetConfig()
	runtimeCfg, err := s.resolveRuntimeConfig(Config{
		Endpoint:     current.Endpoint,
		Model:        current.Model,
		SystemPrompt: current.SystemPrompt,
		MaxTokens:    current.MaxTokens,
	})
	if err != nil {
		return "", err
	}
	s.chatSvc.SetConfig(runtimeCfg)
	return s.chatSvc.SendWithA2ui(s.ctx(), message, func(a2uiMsg string) {
		// O endpoint síncrono devolve as interfaces A2UI no JSON (não há SSE):
		// repassa ao frontend pelo mesmo evento do streaming.
		s.emitEvent("chat:a2ui", a2uiMsg)
		s.publishChatEvent("chat:a2ui", a2uiMsg)
	})
}

// StartStream sends a chat message and streams the response via Wails events.
func (s *Service) StartStream(message string) {
	done := s.beginActivity("chat IA")

	if cfg := s.getAgentConfig(); cfg.ChatAIEnabled != nil && !*cfg.ChatAIEnabled {
		s.emitEvent("chat:error", "Chat AI desabilitado pela configuração do servidor")
		s.publishChatEvent("chat:error", "Chat AI desabilitado pela configuração do servidor")
		done()
		return
	}

	s.safeGo(func() {
		defer done()
		s.ensureToolsRegistered()
		current := s.chatSvc.GetConfig()
		runtimeCfg, cfgErr := s.resolveRuntimeConfig(Config{
			Endpoint:     current.Endpoint,
			Model:        current.Model,
			SystemPrompt: current.SystemPrompt,
			MaxTokens:    current.MaxTokens,
		})
		if cfgErr != nil {
			s.emitEvent("chat:error", cfgErr.Error())
			s.publishChatEvent("chat:error", cfgErr.Error())
			return
		}
		s.chatSvc.SetConfig(runtimeCfg)

		content, err := s.chatSvc.SendStreamMultiRoundWithProgress(
			s.ctx(),
			message,
			func(token string) {
				s.emitEvent("chat:token", token)
				s.publishChatEvent("chat:token", token)
			},
			func(status string) {
				s.emitEvent("chat:thinking", status)
				s.publishChatEvent("chat:thinking", status)
			},
			func(round, maxRounds int) {
				s.emitEvent("chat:loop_progress", map[string]int{"round": round, "maxRounds": maxRounds})
				progressJSON, _ := json.Marshal(map[string]int{"round": round, "maxRounds": maxRounds})
				s.publishChatEvent("chat:loop_progress", string(progressJSON))
			},
			s.mcpExecuteForChat,
			func(a2uiMsg string) {
				s.emitEvent("chat:a2ui", a2uiMsg)
				s.publishChatEvent("chat:a2ui", a2uiMsg)
			},
		)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				s.emitEvent("chat:stopped")
				s.publishChatEvent("chat:stopped", "")
			} else {
				s.emitEvent("chat:error", err.Error())
				s.publishChatEvent("chat:error", err.Error())
				// Recusa por turno em andamento (ErrTurnBusy) não é falha da
				// resposta — não deve gerar notificação. Erro real gera.
				if s.onAssistantResponseFailed != nil && !errors.Is(err, ai.ErrTurnBusy) {
					s.onAssistantResponseFailed(err.Error())
				}
			}
		} else {
			s.emitEvent("chat:done")
			s.publishChatEvent("chat:done", "")
			// Resposta final pronta: o App decide se deve notificar o usuário
			// (aba de chat fora de tela). O hook é nil-safe e não bloqueia a
			// liberação do turno.
			if s.onAssistantResponseComplete != nil {
				s.onAssistantResponseComplete(content)
			}
		}
	})
}

// maxAttachedImageBytes limita o tamanho de cada data URL anexada (base64).
// Um print PNG grande é redimensionado pelo backend antes de virar anexo.
// Alinhado ao teto do servidor (AiChatHelpers.MaxImageBase64Chars = 6 MiB):
// acima disso o servidor descarta a imagem silenciosamente.
const maxAttachedImageBytes = 6 << 20

// StartStreamWithImages envia uma mensagem com imagens anexadas (data URLs) —
// usado pelo ícone de captura de tela do chat. As imagens são validadas
// (prefixo data:image/ e tamanho) e enviadas ao servidor no primeiro round,
// onde viram conteúdo multimodal para o LLM.
func (s *Service) StartStreamWithImages(message string, imagesJSON string) {
	images := []string{}
	if strings.TrimSpace(imagesJSON) != "" {
		if err := json.Unmarshal([]byte(imagesJSON), &images); err != nil {
			s.logf("[chat] StartStreamWithImages: payload de imagens invalido: " + err.Error())
			images = nil
		}
	}
	filtered := make([]string, 0, len(images))
	for _, img := range images {
		img = strings.TrimSpace(img)
		if !strings.HasPrefix(img, "data:image/") {
			s.logf("[chat] anexo ignorado: nao e uma data URL de imagem")
			continue
		}
		if len(img) > maxAttachedImageBytes {
			s.logf(fmt.Sprintf("[chat] anexo ignorado: %d bytes excede o limite de %d", len(img), maxAttachedImageBytes))
			continue
		}
		filtered = append(filtered, img)
	}
	s.chatSvc.SetPendingImages(filtered)
	s.StartStream(message)
}

// StopStream interrupts the active streamed AI response, if running.
func (s *Service) StopStream() bool {
	return s.chatSvc.StopStream()
}

// HasActiveStream retorna true se há um turno de chat em execução (B1: o
// binding de ações A2UI só enfileira com turno ativo).
func (s *Service) HasActiveStream() bool {
	return s.chatSvc.HasActiveStream()
}

// SubmitA2uiAction encaminha uma ação do usuário em uma surface A2UI para o
// serviço de chat. A ação é registrada como um "tool result" pendente que o
// próximo round do loop multi-round enviará ao LLM, permitindo que o agente
// reaja ao clique/input do usuário.
func (s *Service) SubmitA2uiAction(surfaceID, name string, context map[string]any) {
	s.chatSvc.SubmitA2uiAction(surfaceID, name, context)
}

// ClearHistory resets the conversation.
func (s *Service) ClearHistory() {
	s.chatSvc.ClearHistory()
}

// GetHistory returns the conversation for display.
func (s *Service) GetHistory() []Message {
	history := s.chatSvc.GetHistory()
	msgs := make([]Message, 0, len(history))
	for _, m := range history {
		if m.Role == "tool" || (m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) > 0) {
			continue
		}
		msgs = append(msgs, Message{Role: m.Role, Content: m.Content})
	}
	return msgs
}

// GetAvailableTools returns the list of MCP tools for display.
func (s *Service) GetAvailableTools() []map[string]string {
	tools := s.mcpRegistry.Tools()
	result := make([]map[string]string, len(tools))
	for i, t := range tools {
		result[i] = map[string]string{
			"name":        t.Name,
			"description": t.Description,
		}
	}
	return result
}

// parseToolArgs desserializa os argumentos da tool. JSON vazio/null vira um
// mapa vazio; JSON malformado devolve erro (a política de consentimento precisa
// do mapa; o Registry.Call faria a mesma validação depois).
func parseToolArgs(argsJSON string) (map[string]any, error) {
	args := map[string]any{}
	if strings.TrimSpace(argsJSON) == "" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return map[string]any{}, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func (s *Service) mcpExecuteForChat(ctx context.Context, toolName, argsJSON string) (string, error) {
	args, argsErr := parseToolArgs(argsJSON)

	// Gate de consentimento: tools/ações com efeito no computador (gravar
	// arquivo, instalar/desinstalar, parar serviço, reiniciar...) exigem
	// aprovação do USUÁRIO antes de rodar. A decisão é por ação — ver
	// core/mcp.ToolConsentFor. Antes, a única "confirmação" era o parâmetro
	// confirm=true preenchido pelo próprio LLM.
	// Payload malformado não passa pelo gate: não há execução possível (o
	// Registry.Call devolve o erro claro) e não faz sentido pedir autorização
	// para uma chamada que não vai rodar.
	if req := mcp.ToolConsentFor(toolName, args); argsErr == nil && req != nil {
		if s.requestToolConsent == nil {
			return "", fmt.Errorf("autorizacao do usuario indisponivel neste contexto: a acao '%s' nao foi executada", toolName)
		}
		approved, consentErr := s.requestToolConsent(ctx, *req)
		if consentErr != nil {
			return "", consentErr
		}
		if !approved {
			// Negativa é RESULTADO estruturado, não erro: o LLM informa o
			// usuário e não repete a chamada.
			denied, _ := json.Marshal(map[string]any{
				"approved": false,
				"tool":     toolName,
				"message":  "Ação NAO autorizada pelo usuario. Nao insista: explique o motivo e peca novamente somente se for realmente necessario.",
			})
			return string(denied), nil
		}

		// A aprovação do usuário É a confirmação explícita exigida pelos
		// handlers destrutivos (confirm=true em uninstall_package,
		// upgrade_all_packages, service_control, scheduled_task,
		// process_control, printer e power_action). Sem esta marca o
		// Registry.Call recusava a chamada por "parametro obrigatorio confirm
		// ausente" DEPOIS de o usuário já ter autorizado — o LLM precisava
		// repetir a chamada e o usuário recebia um SEGUNDO pedido idêntico.
		// Só marca quando o payload é JSON válido: args malformado continua
		// devolvendo o erro claro do Registry.Call.
		if argsErr == nil {
			args["confirm"] = true
			if b, mErr := json.Marshal(args); mErr == nil {
				argsJSON = string(b)
			}
		}
	}

	result, err := s.mcpRegistry.Call(ctx, toolName, json.RawMessage(argsJSON))
	if err != nil {
		return "", err
	}
	b, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return fmt.Sprintf(`{"result":%q}`, fmt.Sprint(result)), nil
	}
	return string(b), nil
}

// RegisterToolsOnServer envia a lista de tools MCP para a API.
func (s *Service) RegisterToolsOnServer() error {
	dbg := s.getDebugConfig()
	baseURL := strings.TrimSpace(dbg.ApiScheme) + "://" + strings.TrimSpace(dbg.ApiServer)
	if strings.TrimSpace(dbg.ApiScheme) == "" || strings.TrimSpace(dbg.ApiServer) == "" {
		return fmt.Errorf("apiScheme/apiServer nao configurados")
	}
	tools := s.mcpRegistry.Tools()
	type toolEntry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Schema      any    `json:"parametersSchema"`
	}
	entries := make([]toolEntry, 0, len(tools))
	for _, t := range tools {
		entries = append(entries, toolEntry{
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.InputSchema(),
		})
	}
	body := map[string]any{"tools": entries}
	payload, _ := json.Marshal(body)
	endpoint := baseURL + "/api/v1/agent-auth/me/agent-tools/registry"
	req, err := http.NewRequestWithContext(s.ctx(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		s.logf("[chat] registro tools request: " + err.Error())
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// B2: usa o helper padrão de headers agent-auth (inclui AgentID) — o
	// Bearer manual antigo falhava em servidores que exigem o header de ID.
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, strings.TrimSpace(dbg.AuthToken), strings.TrimSpace(dbg.AgentID)); err != nil {
		s.logf("[chat] registro tools headers: " + err.Error())
		return err
	}
	// B2: cliente com timeout — http.DefaultClient (sem timeout) podia
	// pendurar o chamador indefinidamente.
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.logf("[chat] registro tools falhou: " + err.Error())
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		s.logf(fmt.Sprintf("[chat] registro tools retornou HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes))))
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	s.logf(fmt.Sprintf("[chat] %d tools MCP registradas com sucesso no servidor", len(entries)))
	s.toolsRegistrationMu.Lock()
	s.lastToolsRegistration = time.Now()
	s.toolsRegistrationMu.Unlock()
	return nil
}

func (s *Service) ensureToolsRegistered() {
	s.toolsRegistrationMu.RLock()
	lastReg := s.lastToolsRegistration
	s.toolsRegistrationMu.RUnlock()
	if lastReg.IsZero() || time.Since(lastReg) >= 4*time.Minute {
		// B2: registro em background — não bloqueia o envio da mensagem.
		// Uma falha de rede no registro não deve impedir o chat de rodar.
		s.safeGo(func() {
			if err := s.RegisterToolsOnServer(); err != nil {
				s.logf("[chat] aviso: registro de tools falhou: " + err.Error())
			}
		})
	}
}

func (s *Service) resolveRuntimeConfig(input Config) (ai.Config, error) {
	endpoint := strings.TrimSpace(input.Endpoint)
	token := strings.TrimSpace(input.APIKey)
	model := strings.TrimSpace(input.Model)
	systemPrompt := strings.TrimSpace(input.SystemPrompt)
	maxTokens := input.MaxTokens
	if maxTokens < 0 {
		return ai.Config{}, fmt.Errorf("maxTokens invalido: use 0 ou um valor positivo")
	}
	dbg := s.getDebugConfig()
	scheme := strings.TrimSpace(dbg.ApiScheme)
	server := strings.TrimSpace(dbg.ApiServer)
	if endpoint == "" && (scheme == "http" || scheme == "https") && server != "" {
		endpoint = scheme + "://" + server
	}
	if token == "" {
		token = strings.TrimSpace(dbg.AuthToken)
	}
	if endpoint == "" || token == "" {
		return ai.Config{}, fmt.Errorf("configuração de IA incompleta: informe endpoint/token no chat ou apiScheme/apiServer/authToken no Debug")
	}
	return ai.Config{
		Endpoint:     endpoint,
		APIKey:       token,
		AgentID:      strings.TrimSpace(dbg.AgentID),
		Model:        model,
		SystemPrompt: systemPrompt,
		MaxTokens:    maxTokens,
	}, nil
}
