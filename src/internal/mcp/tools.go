// Package mcp implements a Model Context Protocol (MCP) server that exposes
// Discovery's capabilities as callable tools for AI assistants.
package mcp

import "encoding/json"

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
	Handler func(args map[string]any) (any, error) `json:"-"`
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

// Registry holds all registered tools.
type Registry struct {
	tools []Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry { return &Registry{} }

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) { r.tools = append(r.tools, t) }

// Tools returns all registered tools.
func (r *Registry) Tools() []Tool { return r.tools }

// Find returns the named tool or nil.
func (r *Registry) Find(name string) *Tool {
	for i := range r.tools {
		if r.tools[i].Name == name {
			return &r.tools[i]
		}
	}
	return nil
}

// Call invokes a tool by name with the given JSON argument object.
func (r *Registry) Call(name string, argsJSON json.RawMessage) (any, error) {
	tool := r.Find(name)
	if tool == nil {
		return nil, &ToolNotFoundError{Name: name}
	}
	var args map[string]any
	if len(argsJSON) > 0 {
		if err := json.Unmarshal(argsJSON, &args); err != nil {
			args = map[string]any{}
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	return tool.Handler(args)
}

// OpenAIFunctions returns all tools in OpenAI function-calling format.
func (r *Registry) OpenAIFunctions() []map[string]any {
	funcs := make([]map[string]any, len(r.tools))
	for i, t := range r.tools {
		funcs[i] = t.OpenAIFunction()
	}
	return funcs
}

// ToolNotFoundError is returned when a tool is not found in the registry.
type ToolNotFoundError struct {
	Name string
}

func (e *ToolNotFoundError) Error() string {
	return "tool not found: " + e.Name
}
