package remotesession

import (
	"testing"
	"time"
)

func TestParseConsentConfig_Prompt(t *testing.T) {
	payload := map[string]any{
		"consent": map[string]any{
			"required":       true,
			"mode":           "prompt",
			"timeoutSeconds": float64(60),
			"timeoutAction":  "deny",
			"deadlineUtc":    time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339),
			"title":          "Autorização",
			"message":        "Permitir?",
			"operator":       "Ana",
		},
	}
	cfg := ParseConsentConfig(payload)
	if cfg == nil {
		t.Fatal("esperava config")
	}
	if cfg.Mode != "prompt" || !cfg.Required || cfg.TimeoutAction != "deny" {
		t.Fatalf("config inesperada: %+v", cfg)
	}
	decision := cfg.TimeoutDecision("timeout")
	if decision.Accepted || !decision.Auto || decision.Outcome != "timeout-denied" {
		t.Fatalf("timeout deny inesperado: %+v", decision)
	}
}

func TestParseConsentConfig_ApproveAndDefaults(t *testing.T) {
	payload := map[string]any{
		"consent": map[string]any{
			"required":      true,
			"timeoutAction": "approve",
		},
	}
	cfg := ParseConsentConfig(payload)
	if cfg == nil || cfg.Mode != "prompt" {
		t.Fatalf("mode default deveria ser prompt: %+v", cfg)
	}
	decision := cfg.TimeoutDecision("timeout")
	if !decision.Accepted || decision.Outcome != "timeout-granted" {
		t.Fatalf("timeout approve inesperado: %+v", decision)
	}
}

func TestParseBannerConfig(t *testing.T) {
	payload := map[string]any{
		"banner": map[string]any{
			"enabled":         true,
			"text":            "Sessão remota: Ana",
			"allowDisconnect": true,
		},
	}
	banner := ParseBannerConfig(payload)
	if banner == nil || !banner.Enabled || !banner.AllowDisconnect {
		t.Fatalf("banner inesperado: %+v", banner)
	}
}
