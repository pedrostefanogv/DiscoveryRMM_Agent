package mcp

import (
	"fmt"
	"strings"
)

// MCPToolPolicyModeAll é o modo default (compat): todas as tools permitidas.
const MCPToolPolicyModeAll = "all"

// MCPToolPolicyModeAllowlist restringe a execução às tools em Allow.
const MCPToolPolicyModeAllowlist = "allowlist"

// NormalizeMCPToolPolicyMode normaliza o modo da policy: "allowlist" quando
// configurado assim; qualquer outra coisa (vazio/valor desconhecido) cai em
// "all" — compatibilidade: sem policy explícita o comportamento não muda.
func NormalizeMCPToolPolicyMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case MCPToolPolicyModeAllowlist:
		return MCPToolPolicyModeAllowlist
	default:
		return MCPToolPolicyModeAll
	}
}

// ToolAllowedByPolicy aplica a allowlist de tools MCP por tenant.
//
//	mode "all" (default)   → sempre permitido (zero mudança de comportamento);
//	mode "allowlist"       → só toolName ∈ allow é permitido; caso contrário
//	                       devolve erro claro "tool_not_allowed".
//
// O gate de consentimento para tools destrutivas (ToolConsentFor) é
// independente e continua valendo: a allowlist é uma camada ADICIONAL por
// tenant, não um substituto.
func ToolAllowedByPolicy(mode string, allow []string, toolName string) error {
	if NormalizeMCPToolPolicyMode(mode) != MCPToolPolicyModeAllowlist {
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(toolName))
	for _, a := range allow {
		if strings.ToLower(strings.TrimSpace(a)) == name {
			return nil
		}
	}
	return fmt.Errorf("tool_not_allowed: a tool %q nao esta na allowlist deste tenant", toolName)
}
