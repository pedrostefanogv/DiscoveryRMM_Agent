package agentconfig

import (
	"testing"
)

func TestParseAgentConfigurationMaintenanceWindowFlat(t *testing.T) {
	data := []byte(`{
		"maintenanceWindow": {
			"enabled": true,
			"daysOfWeek": [2, 7],
			"startUtc": "02:00",
			"durationMinutes": 90,
			"rebootGraceSeconds": 30
		},
		"mcpToolPolicy": {"mode": "allowlist", "allow": ["ping", "get_inventory"]}
	}`)

	cfg, err := ParseAgentConfiguration(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.MaintenanceWindow == nil {
		t.Fatal("maintenanceWindow ausente")
	}
	if cfg.MaintenanceWindow.Enabled == nil || !*cfg.MaintenanceWindow.Enabled {
		t.Fatal("enabled deveria ser true")
	}
	if len(cfg.MaintenanceWindow.DaysOfWeek) != 2 {
		t.Fatalf("daysOfWeek: %v", cfg.MaintenanceWindow.DaysOfWeek)
	}
	if cfg.MaintenanceWindow.StartUTC != "02:00" || cfg.MaintenanceWindow.DurationMinutes == nil || *cfg.MaintenanceWindow.DurationMinutes != 90 {
		t.Fatalf("startUtc/duration: %+v", cfg.MaintenanceWindow)
	}

	win, grace := cfg.MaintenanceWindow.Effective()
	if !win.Enabled || win.StartUTC != "02:00" || win.DurationMinutes != 90 || len(win.Days) != 2 {
		t.Fatalf("Effective window: %+v", win)
	}
	if grace != 30 {
		t.Fatalf("grace=%d, want 30", grace)
	}

	if cfg.MCPToolPolicy == nil {
		t.Fatal("mcpToolPolicy ausente")
	}
	mode, allow := cfg.MCPToolPolicy.EffectiveMCPToolPolicy()
	if mode != "allowlist" || len(allow) != 2 {
		t.Fatalf("policy: %q %v", mode, allow)
	}
}

func TestParseAgentConfigurationMaintenanceWindowDefaults(t *testing.T) {
	// Flat sem as seções: nil (kill switch off) e grace default.
	cfg, err := ParseAgentConfiguration([]byte(`{"recoveryEnabled": true}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	win, grace := cfg.MaintenanceWindow.Effective()
	if win.Enabled || grace != DefaultRebootGraceSeconds {
		t.Fatalf("esperado janela desligada e grace 60: %+v grace=%d", win, grace)
	}
	mode, allow := cfg.MCPToolPolicy.EffectiveMCPToolPolicy()
	if mode != "all" || allow != nil {
		t.Fatalf("esperado mode all sem policy: %q %v", mode, allow)
	}

	// enabled: false explícita → desligada.
	cfg, err = ParseAgentConfiguration([]byte(`{"maintenanceWindow":{"enabled":false,"startUtc":"02:00"}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if win, _ := cfg.MaintenanceWindow.Effective(); win.Enabled {
		t.Fatal("janela deveria estar desligada")
	}

	// durationMinutes ausente → default 60; rebootGraceSeconds ausente → 60.
	cfg, err = ParseAgentConfiguration([]byte(`{"maintenanceWindow":{"enabled":true,"startUtc":"23:00"}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	win, grace = cfg.MaintenanceWindow.Effective()
	if !win.Enabled || win.DurationMinutes != 60 || grace != 60 {
		t.Fatalf("defaults: %+v grace=%d", win, grace)
	}
}

func TestParseAgentConfigurationMaintenanceWindowHierarchical(t *testing.T) {
	// Server define base; client sobrescreve duration; site sobrescreve enabled.
	data := []byte(`{
		"server": {
			"recoveryEnabled": true,
			"maintenanceWindow": {
				"enabled": true,
				"startUtc": "02:00",
				"durationMinutes": 60,
				"rebootGraceSeconds": 120
			},
			"mcpToolPolicy": {"mode": "all"}
		},
		"client": {
			"maintenanceWindow": {"durationMinutes": 120},
			"mcpToolPolicy": {"mode": "allowlist", "allow": ["ping"]}
		},
		"site": {
			"mcpToolPolicy": {"allow": ["ping", "get_inventory"]}
		}
	}`)

	cfg, err := ParseAgentConfiguration(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mw := cfg.MaintenanceWindow
	if mw == nil || mw.Enabled == nil || !*mw.Enabled {
		t.Fatalf("enabled herdado do server: %+v", mw)
	}
	if mw.DurationMinutes == nil || *mw.DurationMinutes != 120 {
		t.Fatalf("duration do client: %+v", mw)
	}
	if mw.StartUTC != "02:00" || mw.RebootGraceSeconds == nil || *mw.RebootGraceSeconds != 120 {
		t.Fatalf("demais campos do server: %+v", mw)
	}

	// mcpToolPolicy: site não define mode → herda allowlist do client; allow do site.
	mode, allow := cfg.MCPToolPolicy.EffectiveMCPToolPolicy()
	if mode != "allowlist" {
		t.Fatalf("mode=%q, want allowlist (herdado do client)", mode)
	}
	if len(allow) != 2 || allow[0] != "ping" {
		t.Fatalf("allow=%v, want do site", allow)
	}
}
