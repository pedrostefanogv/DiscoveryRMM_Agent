package agentconfig

import (
	"discovery/app/core/maintenance"
	"discovery/app/core/mcp"
)

// Defaults da janela de manutenção.
const (
	// DefaultMaintenanceWindowDurationMinutes é a duração padrão da janela.
	DefaultMaintenanceWindowDurationMinutes = 60
	// DefaultRebootGraceSeconds é o aviso padrão antes do reboot coordenado.
	DefaultRebootGraceSeconds = 60
)

// Effective converte a config (possivelmente nil) em Window resolvida e no
// grace de reboot. Janela ausente/desligada → Window{Enabled:false} e grace
// default (irrelevante: sem janela o deferral nunca acontece).
func (c *MaintenanceWindowConfig) Effective() (maintenance.Window, int) {
	w := maintenance.Window{Enabled: false, DurationMinutes: DefaultMaintenanceWindowDurationMinutes}
	grace := DefaultRebootGraceSeconds
	if c == nil {
		return w, grace
	}
	w.Enabled = c.Enabled != nil && *c.Enabled
	w.Days = append([]int(nil), c.DaysOfWeek...)
	w.StartUTC = c.StartUTC
	if c.DurationMinutes != nil && *c.DurationMinutes > 0 {
		w.DurationMinutes = *c.DurationMinutes
	}
	if c.RebootGraceSeconds != nil && *c.RebootGraceSeconds >= 0 {
		grace = *c.RebootGraceSeconds
	}
	return w, grace
}

// EffectiveMCPToolPolicy resolve a policy de tools MCP: mode normalizado
// ("all" quando ausente — compat) e allow (pode ser nil).
func (c *MCPToolPolicyConfig) EffectiveMCPToolPolicy() (string, []string) {
	if c == nil {
		return mcp.MCPToolPolicyModeAll, nil
	}
	mode := mcp.NormalizeMCPToolPolicyMode(derefString(c.Mode))
	allow := append([]string(nil), c.Allow...)
	return mode, allow
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
