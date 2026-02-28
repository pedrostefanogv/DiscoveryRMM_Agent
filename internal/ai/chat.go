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
	"sync"
	"time"

	"winget-store/internal/mcp"
)

// Config holds the LLM API settings.
type Config struct {
	Endpoint string `json:"endpoint"` // e.g. "https://api.openai.com/v1/chat/completions"
	APIKey   string `json:"apiKey"`
	Model    string `json:"model"` // e.g. "gpt-4o-mini"
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
}

// NewService creates a chat service.
func NewService(registry *mcp.Registry) *Service {
	return &Service{
		registry: registry,
		history:  []Message{},
	}
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

// GetHistory returns a copy of the conversation history (for display).
func (s *Service) GetHistory() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Message, len(s.history))
	copy(out, s.history)
	return out
}

const systemPrompt = `Voce é o assistente Discovery, integrado a uma aplicacao de gerenciamento de inventario e pacotes Windows.
Voce pode usar as ferramentas disponíveis para consultar informacoes do computador, instalar/desinstalar/atualizar pacotes via winget, e exportar relatorios.
Responda sempre em portugues brasileiro de forma clara e objetiva. Quando o usuario pedir algo que envolva os dados do computador ou pacotes, use as ferramentas automaticamente.`

// Send processes a user message: appends it to history, calls the LLM
// (possibly multiple rounds for tool calls), and returns the assistant reply.
func (s *Service) Send(ctx context.Context, userMessage string) (string, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	if cfg.Endpoint == "" || cfg.APIKey == "" || cfg.Model == "" {
		return "", fmt.Errorf("configuracao de IA incompleta: defina endpoint, apiKey e model")
	}

	s.mu.Lock()
	s.history = append(s.history, Message{Role: "user", Content: userMessage})
	s.mu.Unlock()

	// Build tool definitions.
	tools := s.registry.OpenAIFunctions()

	// Allow up to 5 rounds of tool calling.
	for range 5 {
		s.mu.RLock()
		messages := s.buildMessages()
		s.mu.RUnlock()

		resp, err := s.callLLM(ctx, cfg, messages, tools)
		if err != nil {
			return "", err
		}

		msg := resp.Choices[0].Message

		// If the LLM didn't request any tool calls, treat as final answer.
		if len(msg.ToolCalls) == 0 {
			assistant := Message{Role: "assistant", Content: msg.Content}
			s.mu.Lock()
			s.history = append(s.history, assistant)
			s.mu.Unlock()
			return msg.Content, nil
		}

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
			result, callErr := s.registry.Call(tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			var content string
			if callErr != nil {
				content = fmt.Sprintf("Erro: %v", callErr)
			} else {
				b, _ := json.Marshal(result)
				content = string(b)
				// Truncate very large results.
				if len(content) > 20000 {
					content = content[:20000] + "... (truncado)"
				}
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

	return "", fmt.Errorf("limite de chamadas de ferramentas excedido")
}

func (s *Service) buildMessages() []map[string]any {
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
