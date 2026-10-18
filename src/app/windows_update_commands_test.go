package app

import (
	"testing"
	"time"

	"discovery/app/core/windowsupdate"
)

func TestIsWindowsUpdateCommandType(t *testing.T) {
	for _, cmdType := range []string{"windowsupdate", "windows_update", "windows-update", " WindowsUpdate "} {
		if !IsWindowsUpdateCommandType(cmdType) {
			t.Fatalf("%q deveria ser comando de windows update", cmdType)
		}
	}
	for _, cmdType := range []string{"", "softwareupdate", "restart", "systeminfo"} {
		if IsWindowsUpdateCommandType(cmdType) {
			t.Fatalf("%q NAO deveria ser comando de windows update", cmdType)
		}
	}
}

func TestParseWindowsUpdateInstallPayload(t *testing.T) {
	issued := time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339)
	payload := map[string]any{
		"action": "install",
		"updates": []any{
			map[string]any{
				"updateKey": "guid:3c3f1f1a-1111-2222-3333-444455556666:12",
				"updateId":  "3c3f1f1a-1111-2222-3333-444455556666",
				"revision":  float64(12),
				"kbIds":     []any{"KB5034441"},
			},
		},
		"postAction":   "restart",
		"ttlSeconds":   float64(600),
		"issuedAtUtc":  issued,
	}

	parsed, err := parseWindowsUpdateInstallPayload(payload)
	if err != nil {
		t.Fatalf("parse falhou: %v", err)
	}
	if len(parsed.Updates) != 1 || parsed.Updates[0].UpdateKey == "" {
		t.Fatalf("updates invalidos: %+v", parsed.Updates)
	}
	if parsed.PostAction != "restart" {
		t.Fatalf("postAction=%q", parsed.PostAction)
	}
	if parsed.TTLSeconds != 600 {
		t.Fatalf("ttl=%d", parsed.TTLSeconds)
	}
	if parsed.expired(time.Now().UTC()) {
		t.Fatal("comando dentro do TTL nao pode estar expirado")
	}
}

func TestParseWindowsUpdateInstallPayloadRejectsInvalid(t *testing.T) {
	cases := []map[string]any{
		{"action": "install"},
		{"action": "install", "updates": []any{}},
		{"action": "install", "updates": []any{map[string]any{"updateKey": "lixo"}}},
		{"action": "install", "updates": []any{map[string]any{"updateKey": "kb:KB5034441"}}, "postAction": "explode"},
	}
	for _, payload := range cases {
		if _, err := parseWindowsUpdateInstallPayload(payload); err == nil {
			t.Fatalf("payload deveria ser invalido: %+v", payload)
		}
	}
}

func TestInstallPayloadExpires(t *testing.T) {
	payload := map[string]any{
		"action": "install",
		"updates": []any{
			map[string]any{"updateKey": "kb:KB5034441"},
		},
		"ttlSeconds":  float64(60),
		"issuedAtUtc": time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339),
	}
	parsed, err := parseWindowsUpdateInstallPayload(payload)
	if err != nil {
		t.Fatalf("parse falhou: %v", err)
	}
	if !parsed.expired(time.Now().UTC()) {
		t.Fatal("comando deveria estar expirado")
	}
}

func TestParseWindowsUpdatePolicySettings(t *testing.T) {
	settings, err := parseWindowsUpdatePolicySettings(map[string]any{
		"settings": map[string]any{
			"noAutoUpdate":                 float64(0),
			"auOptions":                    float64(2),
			"deferQualityUpdates":          float64(1),
			"deferQualityUpdatesPeriodInDays": float64(7),
			"pauseQualityUpdates":          float64(0),
		},
	})
	if err != nil {
		t.Fatalf("parse falhou: %v", err)
	}
	if settings.AUOptions == nil || *settings.AUOptions != 2 {
		t.Fatalf("auOptions invalido: %+v", settings.AUOptions)
	}
	if settings.DeferQualityUpdatesPeriodInDays == nil || *settings.DeferQualityUpdatesPeriodInDays != 7 {
		t.Fatalf("defer invalido: %+v", settings.DeferQualityUpdatesPeriodInDays)
	}
}

func TestParseWindowsUpdatePolicySettingsRejectsMissing(t *testing.T) {
	if _, err := parseWindowsUpdatePolicySettings(map[string]any{}); err == nil {
		t.Fatal("payload sem settings deveria falhar")
	}
}

func TestWindowsUpdateInstallTargetValidation(t *testing.T) {
	// Chave canonica invalida deve ser rejeitada antes de chegar ao WUA.
	if windowsupdate.ValidUpdateKey("guid:abc:1") {
		t.Fatal("guid invalido aceito")
	}
	if !windowsupdate.ValidUpdateKey("guid:3c3f1f1a-1111-2222-3333-444455556666:1") {
		t.Fatal("guid valido rejeitado")
	}
}
