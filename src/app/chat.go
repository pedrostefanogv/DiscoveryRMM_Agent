package app

import (
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
	// NotifyPreview: mostrar um trecho da resposta no toast nativo (privacidade
	// — o conteúdo aparece no Action Center e na tela de bloqueio).
	NotifyPreview *bool `json:"notifyPreview,omitempty"`
}

// initChatLogger inicializa o banco de chat em
// %ProgramData%\Discovery\logs\chat.db (separado de logs.db por privacidade).
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

	// Em binários de teste o logger global é desligado (mesma guarda do
	// agent.log/logs.db): sem isso o go test criaria chat.db em produção.
	if shouldEnable && logFilePersistenceEnabled() {
		chatLogger := ai.NewChatLogger("")
		chatLogger.Enable(platform.LogDir())
		a.chatSvc.Service().SetChatLogger(chatLogger)
		a.Logs.Append("[chat] log detalhado de chat ativado em " + platform.ChatDBPath())
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
		Endpoint:      cfg.Endpoint,
		APIKey:        cfg.APIKey,
		Model:         cfg.Model,
		SystemPrompt:  cfg.SystemPrompt,
		MaxTokens:     cfg.MaxTokens,
		NotifyPreview: cfg.NotifyPreview,
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
		Endpoint:      c.Endpoint,
		APIKey:        c.APIKey,
		Model:         c.Model,
		SystemPrompt:  c.SystemPrompt,
		MaxTokens:     c.MaxTokens,
		NotifyPreview: c.NotifyPreview,
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

// StartChatStreamWithImages envia uma mensagem do chat com imagens anexadas
// (prints capturados pelo ícone de câmera). imagesJSON é um array JSON de data
// URLs (data:image/...). As imagens são enviadas ao servidor no primeiro round
// e viram conteúdo multimodal para o LLM.
func (a *App) StartChatStreamWithImages(message string, imagesJSON string) {
	a.chatSvc.StartStreamWithImages(message, imagesJSON)
}

// StopChatStream interrupts the active streamed AI response, if running.
func (a *App) StopChatStream() bool {
	return a.chatSvc.StopStream()
}

// HasActiveChatStream informa se há um turno de chat em execução no core.
// Usado pelo timer de segurança da UI para reconciliar o estado quando um
// evento terminal (chat:done/error/stopped) se perde — antes disso a UI se
// liberava cedo demais e o próximo send batia no TryLock do backend, exibindo
// "já existe uma resposta em andamento" para o usuário.
func (a *App) HasActiveChatStream() bool {
	return a.chatSvc.HasActiveStream()
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
