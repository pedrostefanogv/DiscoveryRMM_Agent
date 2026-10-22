package chat

import (
	"context"
	"strings"
	"testing"

	"discovery/app/core/mcp"
)

// Gate de allowlist de tools MCP por tenant (mcpToolPolicy).
func TestMCPToolPolicyGate(t *testing.T) {
	called := 0
	reg := mcp.NewRegistry()
	reg.Register(mcp.Tool{
		Name: "ping",
		Handler: func(_ context.Context, _ map[string]any) (any, error) {
			called++
			return map[string]any{"ok": true}, nil
		},
	})

	t.Run("mode all (default): zero mudança de comportamento", func(t *testing.T) {
		called = 0
		s := New(reg, Deps{})
		if _, err := s.mcpExecuteForChat(context.Background(), "ping", ""); err != nil {
			t.Fatalf("mode all não deveria bloquear: %v", err)
		}
		if called != 1 {
			t.Fatalf("tool deveria executar (called=%d)", called)
		}
	})

	t.Run("allowlist: tool permitida executa", func(t *testing.T) {
		called = 0
		s := New(reg, Deps{GetAgentConfiguration: func() AgentConfiguration {
			return AgentConfiguration{MCPToolMode: "allowlist", MCPToolAllow: []string{"ping"}}
		}})
		if _, err := s.mcpExecuteForChat(context.Background(), "ping", ""); err != nil {
			t.Fatalf("tool na allowlist deveria executar: %v", err)
		}
		if called != 1 {
			t.Fatalf("tool deveria executar (called=%d)", called)
		}
	})

	t.Run("allowlist: tool fora da lista é recusada com tool_not_allowed", func(t *testing.T) {
		called = 0
		s := New(reg, Deps{GetAgentConfiguration: func() AgentConfiguration {
			return AgentConfiguration{MCPToolMode: "allowlist", MCPToolAllow: []string{"other_tool"}}
		}})
		_, err := s.mcpExecuteForChat(context.Background(), "ping", "")
		if err == nil {
			t.Fatal("tool fora da allowlist deveria ser recusada")
		}
		if !strings.HasPrefix(err.Error(), "tool_not_allowed") {
			t.Fatalf("erro deveria ser tool_not_allowed, got %q", err.Error())
		}
		if called != 0 {
			t.Fatalf("tool não deveria executar (called=%d)", called)
		}
	})

	t.Run("allowlist vazia: tudo recusa", func(t *testing.T) {
		called = 0
		s := New(reg, Deps{GetAgentConfiguration: func() AgentConfiguration {
			return AgentConfiguration{MCPToolMode: "allowlist"}
		}})
		if _, err := s.mcpExecuteForChat(context.Background(), "ping", ""); err == nil {
			t.Fatal("allowlist vazia deveria recusar")
		}
		if called != 0 {
			t.Fatalf("tool não deveria executar (called=%d)", called)
		}
	})
}
