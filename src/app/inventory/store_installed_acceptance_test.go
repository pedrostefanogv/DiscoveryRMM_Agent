package inventory

import (
	"context"
	"testing"

	"discovery/app/appstore"
	"discovery/app/core/models"
)

// TestFinalAcceptanceUserCase é o teste de aceitação do caso relatado pelo
// usuário, com os dados REAIS do host. Verifica TODAS as regressões que a
// revisão adversarial levantou, de uma vez.
func TestFinalAcceptanceUserCase(t *testing.T) {
	installed := []models.InstalledPackage{
		// Chrome estável instalado via ARP (Id real observado neste host).
		{Name: "Google Chrome", ID: "ARP\\Machine\\X86\\Google Chrome", Version: "157.0.8103.39", Source: "winget"},
		// Chrome Beta TAMBÉM instalado neste host.
		{Name: "Google Chrome Beta", ID: "Google.Chrome.Beta.EXE", Version: "157.0.8103.39", Source: "winget"},
		// Entradas genéricas que causavam falso positivo por nome.
		{Name: "Setup", ID: "ARP\\Machine\\X64\\SomeSetup", Version: "1.0", Source: "winget"},
		{Name: "Update", ID: "ARP\\Machine\\X64\\SomeUpdate", Version: "2.0", Source: "winget"},
		// Variante clássica.
		{Name: "Visual Studio Code - Insiders", ID: "Microsoft.VisualStudioCode.Insiders", Version: "1.99", Source: "winget"},
	}

	svc := &Service{
		installedPackages: func(context.Context) ([]models.InstalledPackage, error) {
			return installed, nil
		},
	}

	catalog := models.Catalog{Count: 6, Packages: []models.AppItem{
		// O caso do usuário: o card "Google Chrome (EXE)" com id google.chrome.exe.
		{ID: "google.chrome.exe", Name: "Google Chrome (EXE)", InstallationType: string(appstore.InstallationWinget)},
		{ID: "google.chrome", Name: "Google Chrome", InstallationType: string(appstore.InstallationWinget)},
		// Falsos positivos que precisam ficar false.
		{ID: "some.vendor.setup", Name: "Setup", InstallationType: string(appstore.InstallationWinget)},
		{ID: "some.vendor.update", Name: "Update", InstallationType: string(appstore.InstallationWinget)},
		{ID: "microsoft.visualstudiocode", Name: "Visual Studio Code", InstallationType: string(appstore.InstallationWinget)},
		// App realmente ausente.
		{ID: "mozilla.firefox", Name: "Mozilla Firefox", InstallationType: string(appstore.InstallationWinget)},
	}}

	got := svc.MarkCatalogInstalled(context.Background(), catalog)
	byID := map[string]bool{}
	for _, p := range got.Packages {
		byID[p.ID] = p.Installed
		t.Logf("%-32s installed=%v", p.ID, p.Installed)
	}

	mustTrue := []string{"google.chrome.exe", "google.chrome"}
	for _, id := range mustTrue {
		if !byID[id] {
			t.Errorf("ACEITACAO: %q deveria estar instalado (caso do usuario)", id)
		}
	}
	mustFalse := []string{"some.vendor.setup", "some.vendor.update", "microsoft.visualstudiocode", "mozilla.firefox"}
	for _, id := range mustFalse {
		if byID[id] {
			t.Errorf("FALSO POSITIVO: %q nao esta instalado e foi marcado", id)
		}
	}
}
