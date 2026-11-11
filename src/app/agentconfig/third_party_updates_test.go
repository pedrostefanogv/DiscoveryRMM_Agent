package agentconfig

import (
	"testing"
)

// D22: origem de terceiros da política de patches (windowsUpdateThirdPartyEnabled).
//
// Diferente de updatesTabEnabled, este campo NÃO tem default forçado no parse:
// nil = payload antigo/servidor antigo (mantém a coleta de choco/winget, compat).
// O servidor novo SEMPRE envia o valor resolvido da política (Site > Client >
// Global): false = o agente não coleta nem reporta apps de terceiros.

func TestWindowsUpdateThirdPartyEnabled_FlatFalse(t *testing.T) {
	cfg, err := ParseAgentConfiguration([]byte(`{"windowsUpdateThirdPartyEnabled":false}`))
	if err != nil {
		t.Fatalf("expected flat parse, got %v", err)
	}
	if cfg.WindowsUpdateThirdPartyEnabled == nil || *cfg.WindowsUpdateThirdPartyEnabled {
		t.Fatal("expected windowsUpdateThirdPartyEnabled=false quando o payload manda false")
	}
}

func TestWindowsUpdateThirdPartyEnabled_FlatTrue(t *testing.T) {
	cfg, err := ParseAgentConfiguration([]byte(`{"windowsUpdateThirdPartyEnabled":true}`))
	if err != nil {
		t.Fatalf("expected flat parse, got %v", err)
	}
	if cfg.WindowsUpdateThirdPartyEnabled == nil || !*cfg.WindowsUpdateThirdPartyEnabled {
		t.Fatal("expected windowsUpdateThirdPartyEnabled=true quando o payload manda true")
	}
}

func TestWindowsUpdateThirdPartyEnabled_FlatAbsentStaysNil(t *testing.T) {
	// Ausente = servidor antigo: NIL (o agente decide "mantém coleta" no gate).
	// Não há default forçado true/false aqui — diferente de updatesTabEnabled.
	cfg, err := ParseAgentConfiguration([]byte(`{"siteId":"s1"}`))
	if err != nil {
		t.Fatalf("expected parse, got %v", err)
	}
	if cfg.WindowsUpdateThirdPartyEnabled != nil {
		t.Fatal("expected windowsUpdateThirdPartyEnabled NIL quando ausente (payload antigo)")
	}
}
