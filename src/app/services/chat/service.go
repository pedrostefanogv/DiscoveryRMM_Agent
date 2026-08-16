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
	"discovery/app/core/mcp"
	"discovery/app/core/platform"
)

// Config is the frontend-facing AI configuration.
type Config struct {
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"apiKey"`
	Model        string `json:"model"`
	SystemPrompt string `json:"systemPrompt"`
	MaxTokens    int    `json:"maxTokens"`
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
	// SafeGo runs a function in a safe goroutine.
	SafeGo func(func())
	// ChatConfigFile is the config file name.
	ChatConfigFile string
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
	safeGo           func(func())
	chatConfigFile   string

	toolsRegistrationMu   sync.RWMutex
	lastToolsRegistration time.Time
}

// New creates a ChatService.
func New(reg *mcp.Registry, deps Deps) *Service {
	return &Service{
		chatSvc:          ai.NewService(reg),
		mcpRegistry:      reg,
		ctx:              deps.Ctx,
		logf:             deps.Logf,
		getDebugConfig:   deps.GetDebugConfig,
		getAgentConfig:   deps.GetAgentConfiguration,
		beginActivity:    deps.BeginActivity,
		emitEvent:        deps.EmitEvent,
		publishChatEvent: deps.PublishChatEvent,
		safeGo:           deps.SafeGo,
		chatConfigFile:   deps.ChatConfigFile,
	}
}

// Service returns the underlying ai.Service (for advanced use).
func (s *Service) Service() *ai.Service { return s.chatSvc }

// Registry returns the MCP registry.
func (s *Service) Registry() *mcp.Registry { return s.mcpRegistry }

func (s *Service) configPathCandidates() []string {
	return platform.ChatConfigPathCandidates(s.chatConfigFile)
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
		s.logf("[chat] configuração salva em " + path)
		return nil
	}
	if len(errs) == 0 {
		return fmt.Errorf("nenhum caminho válido para salvar configuração do chat")
	}
	return fmt.Errorf("falha ao salvar configuração do chat: %s", strings.Join(errs, " | "))
}

// SetConfig updates and persists the LLM API settings.
func (s *Service) SetConfig(cfg Config) error {
	if cfg.MaxTokens < 0 {
		return fmt.Errorf("maxTokens invalido: use 0 ou um valor positivo")
	}
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
	runtimeCfg, err := s.resolveRuntimeConfig(cfg)
	if err != nil {
		return "", err
	}
	return s.chatSvc.TestConfig(s.ctx(), runtimeCfg)
}

// GetConfig returns the current config (API key masked).
func (s *Service) GetConfig() Config {
	c := s.chatSvc.GetConfig()
	return Config{
		Endpoint:     c.Endpoint,
		APIKey:       c.APIKey,
		Model:        c.Model,
		SystemPrompt: c.SystemPrompt,
		MaxTokens:    c.MaxTokens,
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
	return s.chatSvc.Send(s.ctx(), message)
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

		_, err := s.chatSvc.SendStreamMultiRound(
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
			}
		} else {
			s.emitEvent("chat:done")
			s.publishChatEvent("chat:done", "")
		}
	})
}

// StopStream interrupts the active streamed AI response, if running.
func (s *Service) StopStream() bool {
	return s.chatSvc.StopStream()
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

func (s *Service) mcpExecuteForChat(ctx context.Context, toolName, argsJSON string) (string, error) {
	result, err := s.mcpRegistry.Call(toolName, json.RawMessage(argsJSON))
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
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(dbg.AuthToken))
	resp, err := http.DefaultClient.Do(req)
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
	if lastReg.IsZero() {
		if err := s.RegisterToolsOnServer(); err != nil {
			s.logf("[chat] aviso: registro inicial de tools falhou: " + err.Error())
		}
		return
	}
	if time.Since(lastReg) < 4*time.Minute {
		return
	}
	if err := s.RegisterToolsOnServer(); err != nil {
		s.logf("[chat] aviso: re-registro de tools falhou: " + err.Error())
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
