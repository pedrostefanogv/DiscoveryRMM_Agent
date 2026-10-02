// Package mcp implements a Model Context Protocol (MCP) server that exposes
// Discovery's capabilities as callable tools for AI assistants.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
)

// ToolParam describes a single parameter of a tool.
type ToolParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// Tool describes a single MCP tool.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Params      []ToolParam `json:"params,omitempty"`
	// Handler is called when the tool is invoked. args is the JSON object of params.
	// ctx permite cancelamento/timeout (StopStream, timeouts por tool).
	Handler func(ctx context.Context, args map[string]any) (any, error) `json:"-"`
}

// InputSchema builds the JSON-Schema object expected by the MCP spec and also
// by OpenAI-compatible function-calling APIs.
func (t Tool) InputSchema() map[string]any {
	props := make(map[string]any, len(t.Params))
	required := make([]string, 0, len(t.Params))
	for _, p := range t.Params {
		props[p.Name] = map[string]any{
			"type":        p.Type,
			"description": p.Description,
		}
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// OpenAIFunction returns the tool definition in the OpenAI function-calling
// format used by chat completion APIs.
func (t Tool) OpenAIFunction() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.InputSchema(),
		},
	}
}

// MCPToolEntry returns the tool in the MCP tools/list response format.
func (t Tool) MCPToolEntry() map[string]any {
	return map[string]any{
		"name":        t.Name,
		"description": t.Description,
		"inputSchema": t.InputSchema(),
	}
}

// Registry holds all registered tools. Thread-safe: Register/Tools/Find/Call
// podem ser chamados concorrentemente (o chat multi-round executa tools
// enquanto o servidor stdio MCP e o re-registro periodico rodam).
type Registry struct {
	mu    sync.RWMutex
	tools []Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry { return &Registry{} }

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	// O nome é normalizado para o formato aceito pelo provedor ANTES de entrar no
	// registro: um nome fora do padrão (ex.: "memory/list") derrubava o turno
	// INTEIRO do chat — "Provider stream error: Invalid 'tools[0].function.name'"
	// (OpenAI/OpenRouter exigem ^[a-zA-Z0-9_-]{1,64}$). Como o registro passa a
	// guardar o nome normalizado, o roteamento da tool_call também casa.
	t.Name = SanitizeToolName(t.Name)
	r.mu.Lock()
	defer r.mu.Unlock()
	// Nome repetido é ambíguo e o provedor recusa função duplicada: renomeia com
	// sufixo numérico determinístico (ex.: tool, tool_2).
	if r.hasNameLocked(t.Name) {
		base := t.Name
		for i := 2; r.hasNameLocked(t.Name); i++ {
			t.Name = fmt.Sprintf("%s_%d", base, i)
		}
		log.Printf("[mcp] nome de tool duplicado %q registrado como %q", base, t.Name)
	}
	r.tools = append(r.tools, t)
}

// hasNameLocked informa se o nome já existe (chamada com r.mu travado).
func (r *Registry) hasNameLocked(name string) bool {
	for i := range r.tools {
		if r.tools[i].Name == name {
			return true
		}
	}
	return false
}

// providerToolNamePattern é o padrão exigido pelos provedores OpenAI/OpenRouter.
var providerToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// SanitizeToolName converte um nome arbitrário para o formato aceito pelo
// provedor: qualquer caractere fora de [a-zA-Z0-9_-] vira '_' (barras, espaços,
// pontos) e o resultado é limitado a 64 caracteres.
func SanitizeToolName(name string) string {
	trimmed := strings.TrimSpace(name)
	if providerToolNamePattern.MatchString(trimmed) {
		return trimmed
	}
	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "tool"
	}
	if runes := []rune(out); len(runes) > 64 {
		out = string(runes[:64])
	}
	return out
}

// Tools returns a snapshot of all registered tools.
func (r *Registry) Tools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, len(r.tools))
	copy(out, r.tools)
	return out
}

// Find returns a copy of the named tool or nil.
func (r *Registry) Find(name string) *Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := range r.tools {
		if r.tools[i].Name == name {
			t := r.tools[i]
			return &t
		}
	}
	return nil
}

// Call invokes a tool by name with the given JSON argument object.
//
// O ctx é propagado ao handler, permitindo cancelamento (StopStream) e
// timeouts por tool. Handlers antigos que ignoram ctx continuam funcionando.
func (r *Registry) Call(ctx context.Context, name string, argsJSON json.RawMessage) (any, error) {
	tool := r.Find(name)
	if tool == nil {
		return nil, &ToolNotFoundError{Name: name}
	}
	var args map[string]any
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			// NÃO fazer fallback silencioso para {}: o LLM precisa saber que
			// os argumentos são inválidos para se autocorrigir no próximo round.
			log.Printf("[mcp] Registry.Call(%q): unmarshal args falhou (argsJSON=%s): %v", name, string(argsJSON), err)
			return nil, fmt.Errorf("argumentos invalidos para '%s': JSON malformado (%v) — reenvie a chamada com um objeto JSON valido contendo: %s", name, err, tool.ParamsDesc())
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Validar parametros obrigatorios ANTES de chamar o handler.
	// Isso garante mensagens de erro consistentes e informativas para o LLM,
	// ajudando-o a corrigir os argumentos no proximo round.
	for _, p := range tool.Params {
		if !p.Required {
			continue
		}
		val, ok := args[p.Name]
		if !ok {
			return nil, fmt.Errorf("parametro obrigatorio '%s' (%s) ausente — use: %s", p.Name, p.Type, tool.ParamsDesc())
		}
		switch v := val.(type) {
		case string:
			if strings.TrimSpace(v) == "" {
				return nil, fmt.Errorf("parametro obrigatorio '%s' (%s) nao pode ser vazio — preencha com: %s", p.Name, p.Type, p.Description)
			}
		case nil:
			return nil, fmt.Errorf("parametro obrigatorio '%s' (%s) recebeu null — use: %s", p.Name, p.Type, tool.ParamsDesc())
		}
	}

	return tool.Handler(ctx, args)
}

// OpenAIFunctions returns all tools in OpenAI function-calling format.
func (r *Registry) OpenAIFunctions() []map[string]any {
	funcs := make([]map[string]any, len(r.tools))
	for i, t := range r.tools {
		funcs[i] = t.OpenAIFunction()
	}
	return funcs
}

// ParamsDesc returns a human-readable description of the tool's parameters
// for use in error messages (e.g., "query: string (obrigatorio), limit: integer").
func (t Tool) ParamsDesc() string {
	if len(t.Params) == 0 {
		return "sem parametros obrigatorios"
	}
	parts := make([]string, len(t.Params))
	for i, p := range t.Params {
		req := ""
		if p.Required {
			req = " (obrigatorio)"
		}
		parts[i] = fmt.Sprintf("%s: %s%s", p.Name, p.Type, req)
	}
	return strings.Join(parts, ", ")
}

// ToolNotFoundError is returned when a tool is not found in the registry.
type ToolNotFoundError struct {
	Name string
}

func (e *ToolNotFoundError) Error() string {
	return "tool not found: " + e.Name
}
