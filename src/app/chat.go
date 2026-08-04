package app

import (
	"os"
	"path/filepath"

	"discovery/app/services/chat"
	"discovery/app/core/ai"
	"discovery/app/core/mcp"
	"discovery/app/core/platform"
)

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
	// Criar o ChatLogger na pasta logs/ dentro do diretório de dados do agente
	chatLogger := ai.NewChatLogger("")
	chatLogger.Enable(filepath.Join(platform.DataDir(), "logs"))

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
		a.chatSvc.Service().SetChatLogger(chatLogger)
		a.logs.append("[chat] log detalhado de chat ativado em " + filepath.Join(platform.DataDir(), "logs", "chat_logs.jsonl"))
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

// ChatMessage is a single message for the frontend.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SetChatConfig updates and persists the LLM API settings.
func (a *App) SetChatConfig(cfg ChatConfig) error {
	return a.chatSvc.SetConfig(chat.Config{
		Endpoint:     cfg.Endpoint,
		APIKey:       cfg.APIKey,
		Model:        cfg.Model,
		SystemPrompt: cfg.SystemPrompt,
		MaxTokens:    cfg.MaxTokens,
	})
}

// TestChatConfig checks whether the informed LLM settings are valid without saving them.
func (a *App) TestChatConfig(cfg ChatConfig) (string, error) {
	return a.chatSvc.TestConfig(chat.Config{
		Endpoint:     cfg.Endpoint,
		APIKey:       cfg.APIKey,
		Model:        cfg.Model,
		SystemPrompt: cfg.SystemPrompt,
		MaxTokens:    cfg.MaxTokens,
	})
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
	return a.chatSvc.SendMessage(message)
}

// StartChatStream sends a chat message and streams the response via Wails events.
func (a *App) StartChatStream(message string) {
	a.chatSvc.StartStream(message)
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
		msgs = append(msgs, ChatMessage{Role: m.Role, Content: m.Content})
	}
	return msgs
}

// GetAvailableTools returns the list of MCP tools for display.
func (a *App) GetAvailableTools() []map[string]string {
	return a.chatSvc.GetAvailableTools()
}

// GetMCPRegistry returns the registry (used by main.go for MCP server mode).
func (a *App) GetMCPRegistry() *mcp.Registry {
	return a.chatSvc.Registry()
}

// RegisterAgentToolsOnServer envia a lista de tools MCP para a API.
func (a *App) RegisterAgentToolsOnServer() error {
	return a.chatSvc.RegisterToolsOnServer()
}
