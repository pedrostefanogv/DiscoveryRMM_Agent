package app

import (
	"path/filepath"

	"discovery/app/core/ai"
	"discovery/app/core/mcp"
	"discovery/app/core/platform"
	"discovery/app/services/chat"
)

// ChatConfig is the frontend-facing AI configuration.
type ChatConfig struct {
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"apiKey"`
	Model        string `json:"model"`
	SystemPrompt string `json:"systemPrompt"`
	MaxTokens    int    `json:"maxTokens"`
}

// initChatLogger inicializa o logger JSONL de chat em
// %ProgramData%\Discovery\logs\chat_logs.jsonl.
//
// Padrão: ATIVADO — todas as interações de chat são salvas por padrão
// (contrato documentado em debug.ChatLogConfig: Enabled nil ou true = ativo).
// Apenas um valor explicitamente false no config.json desativa (opt-out).
func (a *App) initChatLogger() {
	// Config do installer decide apenas o OPT-OUT: campo chatLog.enabled
	// explicitamente false desativa; ausente ou true mantém o log ativo.
	shouldEnable := true

	inst, _, err := loadInstallerConfig()
	if err == nil && inst.ChatLog.Enabled != nil {
		shouldEnable = *inst.ChatLog.Enabled
	}

	if shouldEnable {
		chatLogger := ai.NewChatLogger("")
		chatLogger.Enable(filepath.Join(platform.DataDir(), "logs"))
		a.chatSvc.Service().SetChatLogger(chatLogger)
		a.Logs.Append("[chat] log detalhado de chat ativado em " + filepath.Join(platform.DataDir(), "logs", "chat_logs.jsonl"))
	} else {
		chatLogger := ai.NewChatLogger("")
		chatLogger.Disable()
		a.Logs.Append("[chat] log detalhado de chat desativado explicitamente pela configuração (chatLog.enabled=false)")
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
