package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"discovery/internal/ai"
	"discovery/internal/mcp"
	"discovery/internal/platform"
)

func chatConfigPathCandidates() []string {
	return platform.ChatConfigPathCandidates(chatConfigFile)
}

func (a *App) loadPersistedChatConfig() {
	for _, path := range chatConfigPathCandidates() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cfg ChatConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			a.logs.append("[chat] falha ao ler configuração persistida: " + err.Error())
			return
		}

		if cfg.MaxTokens < 0 {
			cfg.MaxTokens = 0
		}

		a.chatSvc.SetConfig(ai.Config{
			Endpoint:     cfg.Endpoint,
			APIKey:       cfg.APIKey,
			AgentID:      a.GetDebugConfig().AgentID,
			Model:        cfg.Model,
			SystemPrompt: cfg.SystemPrompt,
			MaxTokens:    cfg.MaxTokens,
		})
		a.logs.append("[chat] configuração carregada de " + path)
		return
	}
}

func (a *App) persistChatConfig(cfg ChatConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("falha ao serializar configuração do chat: %w", err)
	}

	var errs []string
	for _, path := range chatConfigPathCandidates() {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			errs = append(errs, path+": "+err.Error())
			continue
		}
		a.logs.append("[chat] configuração salva em " + path)
		return nil
	}

	if len(errs) == 0 {
		return fmt.Errorf("nenhum caminho válido para salvar configuração do chat")
	}
	return fmt.Errorf("falha ao salvar configuração do chat: %s", strings.Join(errs, " | "))
}

// ChatConfig is the frontend-facing AI configuration.
type ChatConfig struct {
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"apiKey"`
	Model        string `json:"model"`
	SystemPrompt string `json:"systemPrompt"`
	MaxTokens    int    `json:"maxTokens"`
}

// initChatLogger inicializa o logger JSONL de chat baseado na configuração
// do config.json. Se o campo chatLog estiver ausente (nil), ativa o log
// por padrão e persiste a configuração. Se estiver explicitamente false,
// desativa. Se true, ativa.
func (a *App) initChatLogger() {
	// Criar o ChatLogger no diretório de dados do agente
	chatLogger := ai.NewChatLogger("")
	chatLogger.Enable(platform.DataDir())

	// Verificar config do installer para decidir se ativa ou não
	shouldEnable := true // padrão: ativado

	inst, _, err := loadInstallerConfig()
	if err == nil {
		if inst.ChatLog.Enabled != nil {
			shouldEnable = *inst.ChatLog.Enabled
		} else {
			// Campo ausente: ativar por padrão e persistir
			go a.ensureChatLogConfigEnabled(&inst)
		}
	}

	if shouldEnable {
		a.chatSvc.SetChatLogger(chatLogger)
		a.logs.append("[chat] log detalhado de chat ativado em " + platform.DataDir() + "/chat_logs.jsonl")
	} else {
		chatLogger.Disable()
		a.logs.append("[chat] log detalhado de chat desativado pela configuração")
	}
}

// ensureChatLogConfigEnabled persiste o campo chatLog.enabled = true
// no config.json quando ele está ausente, garantindo que o log fique
// ativo por padrão.
func (a *App) ensureChatLogConfigEnabled(inst *InstallerConfig) {
	if inst == nil {
		return
	}
	enabled := true
	inst.ChatLog.Enabled = &enabled

	// Persistir usando a mesma lógica de persistInstallerConfig
	basePath := ""
	for _, path := range installerConfigPathCandidates() {
		if _, err := os.Stat(path); err == nil {
			basePath = path
			break
		}
	}
	if _, err := persistInstallerConfig(basePath, *inst); err != nil {
		a.logs.append("[chat] aviso: falha ao persistir chatLog.enabled no config.json: " + err.Error())
	} else {
		a.logs.append("[chat] chatLog.enabled = true adicionado ao config.json")
	}
}

func (a *App) resolveAgentChatRuntimeConfig(input ChatConfig) (ai.Config, error) {
	endpoint := strings.TrimSpace(input.Endpoint)
	token := strings.TrimSpace(input.APIKey)
	model := strings.TrimSpace(input.Model)
	systemPrompt := strings.TrimSpace(input.SystemPrompt)
	maxTokens := input.MaxTokens

	if maxTokens < 0 {
		return ai.Config{}, fmt.Errorf("maxTokens invalido: use 0 ou um valor positivo")
	}

	dbg := a.GetDebugConfig()
	scheme := strings.TrimSpace(strings.ToLower(dbg.ApiScheme))
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

// ChatMessage is a single message for the frontend.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SetChatConfig updates and persists the LLM API settings.
func (a *App) SetChatConfig(cfg ChatConfig) error {
	if cfg.MaxTokens < 0 {
		return fmt.Errorf("maxTokens invalido: use 0 ou um valor positivo")
	}

	a.chatSvc.SetConfig(ai.Config{
		Endpoint:     cfg.Endpoint,
		APIKey:       cfg.APIKey,
		AgentID:      a.GetDebugConfig().AgentID,
		Model:        cfg.Model,
		SystemPrompt: cfg.SystemPrompt,
		MaxTokens:    cfg.MaxTokens,
	})

	if err := a.persistChatConfig(cfg); err != nil {
		return err
	}
	return nil
}

// TestChatConfig checks whether the informed LLM settings are valid without saving them.
func (a *App) TestChatConfig(cfg ChatConfig) (string, error) {
	ctx := a.ctx
	runtimeCfg, err := a.resolveAgentChatRuntimeConfig(cfg)
	if err != nil {
		return "", err
	}

	return a.chatSvc.TestConfig(ctx, runtimeCfg)
}

// GetChatConfig returns the current config (API key masked).
func (a *App) GetChatConfig() ChatConfig {
	c := a.chatSvc.GetConfig()
	return ChatConfig{
		Endpoint:     c.Endpoint,
		APIKey:       c.APIKey,
		Model:        c.Model,
		SystemPrompt: c.SystemPrompt,
		MaxTokens:    c.MaxTokens,
	}
}

// SendChatMessage sends a user message and returns the assistant response.
func (a *App) SendChatMessage(message string) (string, error) {
	done := a.beginActivity("chat IA")
	defer done()

	if cfg := a.GetAgentConfiguration(); cfg.ChatAIEnabled != nil && !*cfg.ChatAIEnabled {
		return "", fmt.Errorf("Chat AI desabilitado pela configuração do servidor")
	}

	current := a.chatSvc.GetConfig()
	runtimeCfg, err := a.resolveAgentChatRuntimeConfig(ChatConfig{
		Endpoint:     current.Endpoint,
		Model:        current.Model,
		SystemPrompt: current.SystemPrompt,
		MaxTokens:    current.MaxTokens,
	})
	if err != nil {
		return "", err
	}
	a.chatSvc.SetConfig(runtimeCfg)

	return a.chatSvc.Send(a.ctx, message)
}

// StartChatStream sends a chat message and streams the response via Wails events.
func (a *App) StartChatStream(message string) {
	done := a.beginActivity("chat IA")

	if cfg := a.GetAgentConfiguration(); cfg.ChatAIEnabled != nil && !*cfg.ChatAIEnabled {
		wailsRuntime.EventsEmit(a.ctx, "chat:error", "Chat AI desabilitado pela configuração do servidor")
		done()
		return
	}

	a.safeGo(func() {
		defer done()

		current := a.chatSvc.GetConfig()
		runtimeCfg, cfgErr := a.resolveAgentChatRuntimeConfig(ChatConfig{
			Endpoint:     current.Endpoint,
			Model:        current.Model,
			SystemPrompt: current.SystemPrompt,
			MaxTokens:    current.MaxTokens,
		})
		if cfgErr != nil {
			wailsRuntime.EventsEmit(a.ctx, "chat:error", cfgErr.Error())
			return
		}
		a.chatSvc.SetConfig(runtimeCfg)

		_, err := a.chatSvc.SendStream(
			a.ctx,
			message,
			func(token string) {
				wailsRuntime.EventsEmit(a.ctx, "chat:token", token)
			},
			func(status string) {
				wailsRuntime.EventsEmit(a.ctx, "chat:thinking", status)
			},
		)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				wailsRuntime.EventsEmit(a.ctx, "chat:stopped")
			} else {
				wailsRuntime.EventsEmit(a.ctx, "chat:error", err.Error())
			}
		} else {
			wailsRuntime.EventsEmit(a.ctx, "chat:done")
		}
	})
}

// StopChatStream interrupts the active streamed AI response, if running.
func (a *App) StopChatStream() bool {
	return a.chatSvc.StopStream()
}

// ClearChatHistory resets the conversation.
func (a *App) ClearChatHistory() {
	a.chatSvc.ClearHistory()
}

// GetChatHistory returns the conversation for display.
func (a *App) GetChatHistory() []ChatMessage {
	history := a.chatSvc.GetHistory()
	msgs := make([]ChatMessage, 0, len(history))
	for _, m := range history {
		if m.Role == "tool" || (m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) > 0) {
			continue
		}
		msgs = append(msgs, ChatMessage{Role: m.Role, Content: m.Content})
	}
	return msgs
}

// GetAvailableTools returns the list of MCP tools for display.
func (a *App) GetAvailableTools() []map[string]string {
	tools := a.mcpRegistry.Tools()
	result := make([]map[string]string, len(tools))
	for i, t := range tools {
		result[i] = map[string]string{
			"name":        t.Name,
			"description": t.Description,
		}
	}
	return result
}

// GetMCPRegistry returns the registry (used by main.go for MCP server mode).
func (a *App) GetMCPRegistry() *mcp.Registry {
	return a.mcpRegistry
}
