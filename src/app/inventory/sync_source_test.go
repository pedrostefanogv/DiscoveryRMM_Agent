package inventory

import (
	"testing"

	"discovery/app/core/models"
)

// ── Source derivado da coleta (nunca um palpite sobre o provider ativo) ──

func TestDetectSoftwareCollectionSource(t *testing.T) {
	cases := []struct {
		name     string
		software []models.SoftwareItem
		want     string
	}{
		{"nativo", []models.SoftwareItem{{Name: "A", Source: "registry"}}, "registry"},
		{"osquery", []models.SoftwareItem{{Name: "A", Source: "osquery/programs"}}, "osquery/programs"},
		{"primeiro vazio", []models.SoftwareItem{{Name: "A", Source: ""}, {Name: "B", Source: "registry"}}, "registry"},
		{"lista vazia", nil, "registry"},
	}
	for _, tc := range cases {
		if got := detectSoftwareCollectionSource(tc.software); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestMergePackageManagerSoftware_UsesDetectedSource garante que os apps
// adicionados herdam a origem da coleta real, em vez de um prefixo fixo.
func TestMergePackageManagerSoftware_UsesDetectedSource(t *testing.T) {
	software := []models.SoftwareItem{{Name: "Existente", Version: "1.0", Source: "osquery/programs"}}
	installed := []models.InstalledPackage{{Name: "Novo App", ID: "Novo.App", Version: "2.0", Source: "winget"}}

	merged := mergePackageManagerSoftware(software, installed, nil, "", installedDisplayNameIndex{})
	if len(merged) != 2 {
		t.Fatalf("esperado 2 itens, veio %d", len(merged))
	}
	if merged[1].Source != "osquery/programs" {
		t.Fatalf("source do item adicionado = %q, esperado osquery/programs", merged[1].Source)
	}
}

// ── A correlação de updates precisa sobreviver a um caminho de coleta "cru" ──

// TestBuildAgentSoftwareEnvelope_MergesEvenWithoutPriorMerge garante que o
// envelope aplica o merge internamente: qualquer caminho que envie inventário
// (startup, loop periódico de 6h, force-sync) recebe os apps do gerenciador.
// Antes o merge vivia só no SyncInventoryOnStartup e os demais caminhos
// enviavam a lista crua — o dashboard perdia os updates até o próximo ciclo.
func TestBuildAgentSoftwareEnvelope_MergesEvenWithoutPriorMerge(t *testing.T) {
	report := models.InventoryReport{
		Software: []models.SoftwareItem{
			{Name: "AnyDesk", Version: "ad 9.7.15", Source: "registry"},
		},
	}
	pending := []models.UpgradeItem{
		{Name: "Subsistema do Windows para Linux", ID: "Microsoft.WSL", CurrentVersion: "2.7.11.0", AvailableVersion: "2.7.13", Source: "winget"},
	}
	installed := []models.InstalledPackage{
		{Name: "AnyDesk", ID: "AnyDesk.AnyDesk", Version: "ad 9.7.15", Source: "winget"},
	}

	env := buildAgentSoftwareEnvelope(report, "agent-1", pending, installed, nil)

	item, ok := findSoftwareByName(env, "Subsistema do Windows para Linux")
	if !ok {
		t.Fatalf("app so-do-gerenciador nao entrou no envelope: %+v", env.Software)
	}
	if !item.UpdateAvailable || item.AvailableVersion != "2.7.13" {
		t.Fatalf("app so-do-gerenciador sem update: %+v", item)
	}
}
