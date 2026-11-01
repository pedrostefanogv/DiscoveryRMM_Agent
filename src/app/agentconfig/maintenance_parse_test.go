package agentconfig

import (
	"testing"
	"time"
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

func TestParseAgentConfigurationMaintenanceWindowTimeZone(t *testing.T) {
	data := []byte(`{
		"maintenanceWindow": {
			"enabled": true,
			"daysOfWeek": [2, 3, 4, 5, 6],
			"startUtc": "02:00",
			"startTime": "23:00",
			"timeZone": "America/Sao_Paulo",
			"durationMinutes": 60
		}
	}`)

	cfg, err := ParseAgentConfiguration(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mw := cfg.MaintenanceWindow
	if mw == nil || mw.TimeZone == nil || *mw.TimeZone != "America/Sao_Paulo" || mw.StartTime != "23:00" {
		t.Fatalf("timeZone/startTime ausentes: %+v", mw)
	}
	// O espelho legado continua presente (a UI grava os três campos).
	if mw.StartUTC != "02:00" {
		t.Fatalf("startUtc: %q", mw.StartUTC)
	}

	win, _ := mw.Effective()
	if win.Location == nil || win.Location.String() != "America/Sao_Paulo" {
		t.Fatalf("Effective deveria carregar a Location: %+v", win.Location)
	}
	if win.StartLocal != "23:00" || win.StartUTC != "02:00" {
		t.Fatalf("Effective horários: local=%q utc=%q", win.StartLocal, win.StartUTC)
	}
	// 02:30 UTC de 2024-06-11 == 23:30 local de 2024-06-10: dentro.
	if !win.Contains(time.Date(2024, 6, 11, 2, 30, 0, 0, time.UTC)) {
		t.Fatal("02:30 UTC deveria estar dentro da janela local 23:00 (UTC-3)")
	}
	// 01:00 UTC do dia 10 == 22:00 local do dia 9: fora.
	if win.Contains(time.Date(2024, 6, 10, 1, 0, 0, 0, time.UTC)) {
		t.Fatal("01:00 UTC não deveria estar dentro da janela local 23:00")
	}
}

func TestParseAgentConfigurationMaintenanceWindowTimeZoneHierarchy(t *testing.T) {
	// timeZone: site > client > server; startTime: startTime do cliente
	// prevalece sobre o do servidor.
	data := []byte(`{
		"server": {
			"maintenanceWindow": {
				"enabled": true,
				"startUtc": "02:00",
				"startTime": "23:00",
				"timeZone": "America/Sao_Paulo",
				"durationMinutes": 60
			}
		},
		"client": {
			"maintenanceWindow": {"startTime": "22:00"}
		},
		"site": {
			"maintenanceWindow": {"timeZone": "America/New_York"}
		}
	}`)

	cfg, err := ParseAgentConfiguration(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mw := cfg.MaintenanceWindow
	if mw == nil {
		t.Fatal("maintenanceWindow ausente")
	}
	if mw.TimeZone == nil || *mw.TimeZone != "America/New_York" {
		t.Fatalf("timeZone deveria vir do site: %+v", mw.TimeZone)
	}
	if mw.StartTime != "22:00" {
		t.Fatalf("startTime deveria vir do client: %q", mw.StartTime)
	}
	win, _ := mw.Effective()
	if win.Location == nil || win.Location.String() != "America/New_York" || win.StartLocal != "22:00" {
		t.Fatalf("Effective: loc=%v startLocal=%q", win.Location, win.StartLocal)
	}

	// Sem site/cliente, o timeZone do server vale.
	data = []byte(`{
		"server": {
			"maintenanceWindow": {"enabled": true, "startTime": "23:00", "timeZone": "America/Sao_Paulo"}
		}
	}`)
	cfg, err = ParseAgentConfiguration(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if win, _ := cfg.MaintenanceWindow.Effective(); win.Location == nil || win.Location.String() != "America/Sao_Paulo" {
		t.Fatalf("timeZone herdada do server: %v", win.Location)
	}
}

func TestParseAgentConfigurationMaintenanceWindowInvalidTimeZone(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{
			name: "timezone inválida cai no legado (UTC)",
			json: `{"maintenanceWindow":{"enabled":true,"startUtc":"02:00","startTime":"23:00","timeZone":"Mars/Olympus"}}`,
		},
		{
			name: "timezone vazia = UTC",
			json: `{"maintenanceWindow":{"enabled":true,"startUtc":"02:00","startTime":"23:00","timeZone":""}}`,
		},
		{
			name: "sem startTime mesmo com timezone válida → legado",
			json: `{"maintenanceWindow":{"enabled":true,"startUtc":"02:00","timeZone":"America/Sao_Paulo"}}`,
		},
		{
			name: "startTime inválido mesmo com timezone válida → legado",
			json: `{"maintenanceWindow":{"enabled":true,"startUtc":"02:00","startTime":"25:99","timeZone":"America/Sao_Paulo"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseAgentConfiguration([]byte(tc.json))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			win, _ := cfg.MaintenanceWindow.Effective()
			if win.Location != nil {
				t.Fatalf("Location deveria ser nil (legado): %v", win.Location)
			}
			// Caminho legado: startUtc 02:00 UTC em 2024-06-10.
			if !win.Contains(time.Date(2024, 6, 10, 2, 30, 0, 0, time.UTC)) {
				t.Fatal("caminho legado deveria conter 02:30 UTC")
			}
			if got, want := win.NextStart(time.Date(2024, 6, 10, 12, 0, 0, 0, time.UTC)), time.Date(2024, 6, 11, 2, 0, 0, 0, time.UTC); !got.Equal(want) {
				t.Fatalf("NextStart legado=%s, want %s", got, want)
			}
		})
	}
}
