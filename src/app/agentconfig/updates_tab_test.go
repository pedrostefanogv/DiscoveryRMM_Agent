package agentconfig

import (
	"encoding/json"
	"testing"
)

// Testes do kill switch da aba "Atualizações" (cfg.updatesTabEnabled).
//
// Semântica (contrato congelado): site ?? client ?? server; ausente em tudo => true.
// O servidor SEMPRE envia o valor resolvido — o default TRUE existe apenas para
// payload antigo/incompleto, de modo que a ausência do campo nunca desliga a aba.
//
// Harness: ParseAgentConfiguration com o mesmo estilo dos testes de P2P winget /
// agentHomeTab (payload flat vs. hierárquico {"server":{...},"client":{...},"site":{...}}).

// (a) Flat com o campo explícito false => kill switch desligado.
func TestUpdatesTabEnabled_FlatExplicitFalse(t *testing.T) {
	cfg, err := ParseAgentConfiguration([]byte(`{"updatesTabEnabled":false}`))
	if err != nil {
		t.Fatalf("expected flat parse, got %v", err)
	}
	if cfg.UpdatesTabEnabled == nil {
		t.Fatal("expected updatesTabEnabled non-nil após payload flat explícito")
	}
	if *cfg.UpdatesTabEnabled {
		t.Fatal("expected updatesTabEnabled=false quando o payload manda false")
	}
}

// (a') Flat com o campo explícito true => habilitado.
func TestUpdatesTabEnabled_FlatExplicitTrue(t *testing.T) {
	cfg, err := ParseAgentConfiguration([]byte(`{"updatesTabEnabled":true}`))
	if err != nil {
		t.Fatalf("expected flat parse, got %v", err)
	}
	if cfg.UpdatesTabEnabled == nil || !*cfg.UpdatesTabEnabled {
		t.Fatal("expected updatesTabEnabled=true quando o payload manda true")
	}
}

// (b) Flat SEM o campo => default TRUE (não-nil).
// Comportamento documentado do código: ParseAgentConfiguration materializa o
// default true quando o campo vem nil, exatamente como faz para
// automationP2pWingetInstallEnabled. Portanto o esperado é NÃO-nil e true.
func TestUpdatesTabEnabled_FlatAbsentDefaultsTrue(t *testing.T) {
	cfg, err := ParseAgentConfiguration([]byte(`{"siteId":"s1"}`))
	if err != nil {
		t.Fatalf("expected parse, got %v", err)
	}
	if cfg.UpdatesTabEnabled == nil {
		t.Fatal("expected updatesTabEnabled default NÃO-nil quando ausente no payload flat")
	}
	if !*cfg.UpdatesTabEnabled {
		t.Fatal("expected updatesTabEnabled default true quando ausente no payload flat (ausência não desliga a aba)")
	}
}

// (c) Hierárquico: server=false, client/site ausentes => false (server vale).
func TestUpdatesTabEnabled_HierarchicalServerFalse(t *testing.T) {
	payload := []byte(`{
		"server": {"updatesTabEnabled": false},
		"client": {},
		"site": {}
	}`)
	cfg, err := ParseAgentConfiguration(payload)
	if err != nil {
		t.Fatalf("expected hierarchical parse, got %v", err)
	}
	if cfg.UpdatesTabEnabled == nil || *cfg.UpdatesTabEnabled {
		t.Fatal("expected server=false a desligar a aba quando client/site não sobrescrevem")
	}
}

// (d) Hierárquico: server=true, client=false, site ausente => false (client vence).
func TestUpdatesTabEnabled_HierarchicalClientWins(t *testing.T) {
	payload := []byte(`{
		"server": {"updatesTabEnabled": true},
		"client": {"updatesTabEnabled": false},
		"site": {}
	}`)
	cfg, err := ParseAgentConfiguration(payload)
	if err != nil {
		t.Fatalf("expected hierarchical parse, got %v", err)
	}
	if cfg.UpdatesTabEnabled == nil || *cfg.UpdatesTabEnabled {
		t.Fatal("expected client=false a vencer o server=true")
	}
}

// (e) Hierárquico: server=true, client=false, site=true => true (site vence).
func TestUpdatesTabEnabled_HierarchicalSiteWinsOverClient(t *testing.T) {
	payload := []byte(`{
		"server": {"updatesTabEnabled": true},
		"client": {"updatesTabEnabled": false},
		"site": {"updatesTabEnabled": true}
	}`)
	cfg, err := ParseAgentConfiguration(payload)
	if err != nil {
		t.Fatalf("expected hierarchical parse, got %v", err)
	}
	if cfg.UpdatesTabEnabled == nil || !*cfg.UpdatesTabEnabled {
		t.Fatal("expected site=true a vencer o client=false")
	}
}

// (f) Hierárquico: server=false, client ausente, site=true => true (site reabilita).
func TestUpdatesTabEnabled_HierarchicalSiteReenables(t *testing.T) {
	payload := []byte(`{
		"server": {"updatesTabEnabled": false},
		"client": {},
		"site": {"updatesTabEnabled": true}
	}`)
	cfg, err := ParseAgentConfiguration(payload)
	if err != nil {
		t.Fatalf("expected hierarchical parse, got %v", err)
	}
	if cfg.UpdatesTabEnabled == nil || !*cfg.UpdatesTabEnabled {
		t.Fatal("expected site=true a reabilitar a aba mesmo com server=false")
	}
}

// (g) Hierárquico SEM o campo em nenhum nível => true (default).
func TestUpdatesTabEnabled_HierarchicalAbsentDefaultsTrue(t *testing.T) {
	payload := []byte(`{
		"server": {"siteId": "srv-1"},
		"client": {"clientId": "cli-1"},
		"site": {"siteId": "site-1"}
	}`)
	cfg, err := ParseAgentConfiguration(payload)
	if err != nil {
		t.Fatalf("expected hierarchical parse, got %v", err)
	}
	if cfg.UpdatesTabEnabled == nil {
		t.Fatal("expected updatesTabEnabled default NÃO-nil quando ausente na hierarquia")
	}
	if !*cfg.UpdatesTabEnabled {
		t.Fatal("expected updatesTabEnabled default true quando ausente em todos os níveis")
	}
}

// Hierárquico: server=true (explícito), client/site ausentes => true (server vale
// e a herança não inventa override).
func TestUpdatesTabEnabled_HierarchicalServerTrueOnly(t *testing.T) {
	payload := []byte(`{
		"server": {"updatesTabEnabled": true},
		"client": {},
		"site": {}
	}`)
	cfg, err := ParseAgentConfiguration(payload)
	if err != nil {
		t.Fatalf("expected hierarchical parse, got %v", err)
	}
	if cfg.UpdatesTabEnabled == nil || !*cfg.UpdatesTabEnabled {
		t.Fatal("expected server=true preservado quando não há override de client/site")
	}
}

// O mesmo valor deve sobreviver ao roundtrip de marshalling (o agente grava
// AgentConfiguration em disco/cache).
func TestUpdatesTabEnabled_MarshalRoundtrip(t *testing.T) {
	cfg, err := ParseAgentConfiguration([]byte(`{"updatesTabEnabled":false}`))
	if err != nil {
		t.Fatalf("expected parse, got %v", err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded AgentConfiguration
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if decoded.UpdatesTabEnabled == nil || *decoded.UpdatesTabEnabled {
		t.Fatal("expected updatesTabEnabled=false a sobreviver ao roundtrip JSON")
	}
}
