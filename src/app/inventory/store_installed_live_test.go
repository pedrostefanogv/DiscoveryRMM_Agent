package inventory

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"discovery/app/appstore"
	"discovery/app/core/models"
)

// TestLiveWingetListMarksInstalledApps é um teste de fumaça NÃO determinístico:
// roda o "winget list" REAL (se disponível) e confirma que um item de catálogo
// cujo Id aparece na listagem de instalados é marcado como instalado.
//
// Não assume que nenhum app específico esteja instalado: usa o primeiro pacote
// que o winget reportar. O parser de PRODUÇÃO é coberto, com dados verbatim
// deste host, em app/updates (TestParseInstalledListOutputRealHostChromeId).
func TestLiveWingetListMarksInstalledApps(t *testing.T) {
	if _, err := exec.LookPath("winget"); err != nil {
		t.Skip("winget indisponivel neste ambiente")
	}
	out, err := exec.Command("winget", "list", "--accept-source-agreements").CombinedOutput()
	if err != nil {
		t.Skipf("winget list falhou neste ambiente: %v", err)
	}

	// Extrai pares (Id, Versão) da coluna do meio: qualquer linha de dados com
	// pelo menos 3 campos serve. Suficiente para o smoke; a fidelidade do parser
	// real é responsabilidade do teste do pacote updates.
	var probe models.InstalledPackage
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		candidate := fields[len(fields)-2]
		if !strings.Contains(candidate, ".") || strings.Contains(candidate, "-") && !strings.Contains(candidate, ".") {
			continue
		}
		name := strings.Join(fields[:len(fields)-3], " ")
		if name == "" {
			name = candidate
		}
		probe = models.InstalledPackage{Name: name, ID: candidate, Source: "winget"}
		break
	}
	if probe.ID == "" {
		t.Skip("nao foi possivel extrair um pacote instalado do winget list")
	}
	t.Logf("cobaia do winget list real: %q (Id=%q)", probe.Name, probe.ID)

	svc := &Service{
		installedPackages: func(context.Context) ([]models.InstalledPackage, error) {
			return []models.InstalledPackage{probe}, nil
		},
	}

	// O item de catálogo com o MESMO Id tem de ser marcado (degrau 1: Id exato).
	catalog := models.Catalog{
		Count: 1,
		Packages: []models.AppItem{{
			ID:               probe.ID,
			Name:             probe.Name,
			InstallationType: string(appstore.InstallationWinget),
		}},
	}
	got := svc.MarkCatalogInstalled(context.Background(), catalog)
	if !got.Packages[0].Installed {
		t.Fatalf("item instalado %q (Id=%q) do winget list real nao foi marcado como instalado", probe.Name, probe.ID)
	}
	t.Logf("smoke OK: %q (Id=%q) marcado como instalado por Id exato", probe.Name, probe.ID)
}
