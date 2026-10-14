package appstore

import (
	"context"
	"testing"
)

// buildCatalogFromPolicy precisa repassar os 8 campos do contrato §1.2 para o
// AppItem que vai à UI (antes a conversão descartava os campos novos).
func TestBuildCatalogFromPolicyPropagatesCustomFields(t *testing.T) {
	cache := &PolicyCache{}
	cache.Set(EffectivePolicy{
		FetchedAt: "2026-10-12T00:00:00Z",
		Items: []Item{
			{
				InstallationType:     string(InstallationCustom),
				PackageID:            "pkg-custom-1",
				Name:                 "Meu App Custom",
				Publisher:            "Acme",
				Version:              "2.0.0",
				InstallCommand:       "setup.exe /S",
				InstallCommandKind:   CommandKindPowerShellScript,
				UninstallCommandKind: CommandKindCmdScript,
				DetectionMode:        DetectionModeInventoryLink,
				InventorySoftwareID:  "11111111-2222-3333-4444-555555555555",
				DetectionName:        "Meu App",
				DetectionPublisher:   "Acme Ltda",
				DetectionMinVersion:  "1.5.0",
				DetectionCommand:     "exit 0",
			},
		},
	})

	svc := &Service{
		cache:                 cache,
		logf:                  func(string) {},
		featureEnabled:        func(*bool) bool { return true },
		getAgentConfiguration: func() AgentConfiguration { return AgentConfiguration{} },
		getDebugConfig:        func() DebugConfig { return DebugConfig{} },
	}

	catalog, err := svc.GetCatalogFromAppStore(context.Background())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(catalog.Packages) != 1 {
		t.Fatalf("esperava 1 pacote, veio %d", len(catalog.Packages))
	}

	pkg := catalog.Packages[0]
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"InstallCommandKind", pkg.InstallCommandKind, CommandKindPowerShellScript},
		{"UninstallCommandKind", pkg.UninstallCommandKind, CommandKindCmdScript},
		{"DetectionMode", pkg.DetectionMode, DetectionModeInventoryLink},
		{"InventorySoftwareID", pkg.InventorySoftwareID, "11111111-2222-3333-4444-555555555555"},
		{"DetectionName", pkg.DetectionName, "Meu App"},
		{"DetectionPublisher", pkg.DetectionPublisher, "Acme Ltda"},
		{"DetectionMinVersion", pkg.DetectionMinVersion, "1.5.0"},
		{"DetectionCommand", pkg.DetectionCommand, "exit 0"},
		{"InstallationType", pkg.InstallationType, string(InstallationCustom)},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s = %v, esperado %v", tc.name, tc.got, tc.want)
		}
	}
	// Installed é runtime: o catálogo não vem com ele preenchido.
	if pkg.Installed {
		t.Fatal("Installed não deve vir preenchido pela conversão da política")
	}
}
