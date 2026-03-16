package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/samber/lo"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"discovery/internal/ai"
	"discovery/internal/mcp"
	"discovery/internal/watchdog"
)

func chatConfigPathCandidates() []string {
	paths := make([]string, 0, 4)

	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Discovery", chatConfigFile))
		}
	}

	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		paths = append(paths, filepath.Join(filepath.Dir(exe), chatConfigFile))
	}

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".discovery", chatConfigFile))
	}

	paths = append(paths, filepath.Join(".", chatConfigFile))
	return lo.Uniq(paths)
}

func (a *App) loadPersistedChatConfig() {
	for _, path := range chatConfigPathCandidates() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cfg ChatConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			a.logs.append("[chat] falha ao ler configuracao persistida: " + err.Error())
			return
		}

		if cfg.MaxTokens < 0 {
			cfg.MaxTokens = 0
		}

		a.chatSvc.SetConfig(ai.Config{
			Endpoint:     cfg.Endpoint,
			APIKey:       cfg.APIKey,
			Model:        cfg.Model,
			SystemPrompt: cfg.SystemPrompt,
			MaxTokens:    cfg.MaxTokens,
		})
		a.logs.append("[chat] configuracao carregada de " + path)
		return
	}
}

func (a *App) persistChatConfig(cfg ChatConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("falha ao serializar configuracao do chat: %w", err)
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
		a.logs.append("[chat] configuracao salva em " + path)
		return nil
	}

	if len(errs) == 0 {
		return fmt.Errorf("nenhum caminho valido para salvar configuracao do chat")
	}
	return fmt.Errorf("falha ao salvar configuracao do chat: %s", strings.Join(errs, " | "))
}

// ChatConfig is the frontend-facing AI configuration.
type ChatConfig struct {
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"apiKey"`
	Model        string `json:"model"`
	SystemPrompt string `json:"systemPrompt"`
	MaxTokens    int    `json:"maxTokens"`
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
		return ai.Config{}, fmt.Errorf("configuracao de IA incompleta: informe endpoint/token no chat ou apiScheme/apiServer/authToken no Debug")
	}

	return ai.Config{
		Endpoint:     endpoint,
		APIKey:       token,
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
	if ctx == nil {
		ctx = context.Background()
	}
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

	streamMonitor := watchdog.NewStreamMonitor(
		"ai-chat-stream",
		90*time.Second,
		func() {
			a.chatSvc.StopStream()
			if a.ctx != nil {
				wailsRuntime.EventsEmit(a.ctx, "chat:error", "Stream interrompido automaticamente por inatividade")
			}
		},
	)

	go func() {
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

		streamMonitor.Start(a.ctx)
		defer streamMonitor.Stop()

		if a.watchdogSvc != nil {
			a.watchdogSvc.Heartbeat(watchdog.ComponentAI)
		}

		_, err := a.chatSvc.SendStream(
			a.ctx,
			message,
			func(token string) {
				streamMonitor.Activity()
				if a.watchdogSvc != nil {
					a.watchdogSvc.Heartbeat(watchdog.ComponentAI)
				}
				wailsRuntime.EventsEmit(a.ctx, "chat:token", token)
			},
			func(status string) {
				streamMonitor.Activity()
				if a.watchdogSvc != nil {
					a.watchdogSvc.Heartbeat(watchdog.ComponentAI)
				}
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
	}()
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
