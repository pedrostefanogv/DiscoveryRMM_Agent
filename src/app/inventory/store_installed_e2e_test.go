package inventory

import (
	"context"
	"testing"

	"discovery/app/appstore"
	"discovery/app/core/models"
)

// chromeInstalledAsARP reproduz a linha REAL que o "winget list" deste host
// devolve para o Google Chrome instalado: o Id é o de ARP, NÃO o Id de catálogo
// "Google.Chrome.EXE". (O parser de produção é coberto em
// app/updates/parse_installed_real_host_test.go, no pacote onde ele vive.)
func chromeInstalledAsARP() []models.InstalledPackage {
	return []models.InstalledPackage{
		{Name: "Google Chrome", ID: "ARP\\Machine\\X86\\Google Chrome", Version: "157.0.8103.39", Source: "winget"},
		{Name: "Google Chrome Beta", ID: "Google.Chrome.Beta.EXE", Version: "157.0.8103.39", Source: "winget"},
		{Name: "7-Zip 26.05 (x64 edition)", ID: "7zip.7zip", Version: "26.05.00.0", Source: "winget"},
	}
}

// TestRealHostChromeIsMarkedInstalled é a prova ponta a ponta do bug relatado:
// com os dados REAIS do host (Chrome instalado com Id de ARP e um Chrome Beta
// também instalado), o item de catálogo "google.chrome.exe" precisa sair com
// Installed=true, e o stable NÃO pode ser confundido com o Beta.
func TestRealHostChromeIsMarkedInstalled(t *testing.T) {
	svc := &Service{
		installedPackages: func(context.Context) ([]models.InstalledPackage, error) {
			return chromeInstalledAsARP(), nil
		},
	}

	// Catálogo equivalente ao exibido na tela da Loja.
	catalog := models.Catalog{
		Count: 5,
		Packages: []models.AppItem{
			{ID: "google.chrome.exe", Name: "Google Chrome (EXE)", InstallationType: string(appstore.InstallationWinget)},
			{ID: "google.chrome", Name: "Google Chrome", InstallationType: string(appstore.InstallationWinget)},
			{ID: "mozilla.firefox.pt-br", Name: "Mozilla Firefox (pt-BR)", InstallationType: string(appstore.InstallationWinget)},
			{ID: "foxit-pdf-editor", Name: "Foxit Editor", InstallationType: string(appstore.InstallationCustom)},
			{ID: "7zip.7zip", Name: "7-Zip 26.05 (x64 edition)", InstallationType: string(appstore.InstallationWinget)},
		},
	}

	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	byID := map[string]bool{}
	for _, p := range got.Packages {
		byID[p.ID] = p.Installed
	}

	if !byID["google.chrome.exe"] {
		t.Fatalf("E2E: 'google.chrome.exe' (nome de catalogo 'Google Chrome (EXE)') deveria sair Installed=true — era o bug do usuario")
	}
	if !byID["google.chrome"] {
		t.Fatalf("E2E: 'google.chrome' deveria sair Installed=true")
	}
	if !byID["7zip.7zip"] {
		t.Fatalf("E2E: 7-Zip (Id exato) deveria sair Installed=true")
	}
	if byID["mozilla.firefox.pt-br"] {
		t.Fatalf("E2E: Firefox (pt-BR) NAO esta instalado (so existe o Chrome) e nao pode ser marcado")
	}
	if byID["foxit-pdf-editor"] {
		t.Fatalf("E2E: custom sem deteccao nao pode ser marcado")
	}
}
