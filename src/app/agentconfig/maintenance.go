package agentconfig

import (
	"strings"
	"time"

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
//
// Fuso horário: se TimeZone for um ID IANA válido E StartTime for um "HH:mm"
// válido, a janela é avaliada em HORA LOCAL dessa zona (Window.Location e
// Window.StartLocal preenchidos). Caso contrário — timeZone ausente/vazia/
// inválida ou startTime ausente/inválido — mantém-se o caminho legado:
// Window.Location nil e StartUTC interpretado como UTC.
func (c *MaintenanceWindowConfig) Effective() (maintenance.Window, int) {
	w := maintenance.Window{Enabled: false, DurationMinutes: DefaultMaintenanceWindowDurationMinutes}
	grace := DefaultRebootGraceSeconds
	if c == nil {
		return w, grace
	}
	w.Enabled = c.Enabled != nil && *c.Enabled
	w.Days = append([]int(nil), c.DaysOfWeek...)
	w.StartUTC = c.StartUTC
	w.StartLocal = strings.TrimSpace(c.StartTime)
	if c.TimeZone != nil {
		if tz := strings.TrimSpace(*c.TimeZone); tz != "" {
			if loc, err := time.LoadLocation(tz); err == nil {
				w.Location = loc
			}
			// ID IANA inválido/desconhecido → Location nil = legado (UTC).
		}
	}
	if w.Location != nil {
		// Sem startTime válido não há hora local para montar a janela:
		// volta ao caminho legado (startUtc em UTC).
		if _, ok := maintenance.ParseClock(w.StartLocal); !ok {
			w.Location = nil
		}
	}
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
