package inventory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"discovery/app/appstore"
	"discovery/app/core/models"
)

// installedPackagesFunc devolve a lista de instalados do gerenciador sem rodar
// "winget list" — é o ponto de injeção usado pela loja.
func installedPackagesFunc(items []models.InstalledPackage, err error) func(context.Context) ([]models.InstalledPackage, error) {
	return func(context.Context) ([]models.InstalledPackage, error) {
		if err != nil {
			return nil, err
		}
		return items, nil
	}
}

func marketplaceCatalog(pkgs ...models.AppItem) models.Catalog {
	return models.Catalog{Count: len(pkgs), Packages: pkgs}
}

func wingetItem(id, name string) models.AppItem {
	return models.AppItem{
		ID:               id,
		Name:             name,
		InstallationType: string(appstore.InstallationWinget),
	}
}

// TestMarkCatalogInstalledWingetMatchesARPIdentifier é o caso REAL do usuário:
// o Chrome instalado via ARP aparece no "winget list" com o Id de ARP
// ("ARP\Machine\X86\Google Chrome"), enquanto a loja usa o Id de catálogo
// ("google.chrome.exe"). Só um match por Id deixaria o card preso em
// "Instalar" — o casamento por nome de exibição é o que resolve.
func TestMarkCatalogInstalledWingetMatchesARPIdentifier(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "Google Chrome", ID: "ARP\\Machine\\X86\\Google Chrome", Version: "157.0.8103.39", Source: "winget"},
		}, nil),
	}

	catalog := marketplaceCatalog(wingetItem("google.chrome.exe", "Google Chrome"))
	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if !got.Packages[0].Installed {
		t.Fatalf("Google Chrome instalado (Id ARP) deveria ficar Installed=true; veio false — é exatamente o bug relatado pelo usuário")
	}
}

// TestMarkCatalogInstalledWingetExactID casa pelo Id exato, ignorando caixa.
func TestMarkCatalogInstalledWingetExactID(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "7-Zip 26.05 (x64 edition)", ID: "7zip.7zip", Version: "26.05.0", Source: "winget"},
		}, nil),
	}

	catalog := marketplaceCatalog(wingetItem("7ZIP.7Zip", "7-Zip 26.05 (x64 edition)"))
	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if !got.Packages[0].Installed {
		t.Fatalf("Id exato (case-insensitive) deveria casar; veio Installed=false")
	}
}

// TestMarkCatalogInstalledRejectsVariant guarda anti-variante: o Chrome Beta
// instalado NÃO pode marcar o Chrome stable como instalado (e vice-versa).
// Neste host existe de fato um Google.Chrome.Beta.EXE instalado.
func TestMarkCatalogInstalledRejectsVariant(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "Google Chrome Beta", ID: "Google.Chrome.Beta.EXE", Version: "157.0.8103.39", Source: "winget"},
		}, nil),
	}

	catalog := marketplaceCatalog(wingetItem("google.chrome.exe", "Google Chrome"))
	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if got.Packages[0].Installed {
		t.Fatalf("Chrome stable foi marcado instalado por causa do Chrome Beta instalado (falso positivo)")
	}
}

// TestMarkCatalogInstalledAbsentStaysFalse: app realmente ausente permanece false.
func TestMarkCatalogInstalledAbsentStaysFalse(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "7-Zip", ID: "7zip.7zip", Version: "26.05.0", Source: "winget"},
		}, nil),
	}

	catalog := marketplaceCatalog(
		wingetItem("mozilla.firefox", "Mozilla Firefox"),
		models.AppItem{ID: "foxit-pdf-editor", Name: "Foxit Editor", InstallationType: string(appstore.InstallationCustom)},
	)
	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	for _, pkg := range got.Packages {
		if pkg.Installed {
			t.Fatalf("pacote %q nao esta instalado e nao deveria ser marcado", pkg.ID)
		}
	}
}

// TestMarkCatalogInstalledFailSafeOnError: erro na coleta nunca derruba a
// listagem da loja — todos ficam false e o catalogo segue.
func TestMarkCatalogInstalledFailSafeOnError(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc(nil, errors.New("winget list falhou")),
	}

	catalog := marketplaceCatalog(wingetItem("google.chrome.exe", "Google Chrome"), wingetItem("7zip.7zip", "7-Zip"))
	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if got.Count != catalog.Count || len(got.Packages) != 2 {
		t.Fatalf("catalogo deveria seguir intacto em falha de coleta; veio %d item(ns)", len(got.Packages))
	}
	for _, pkg := range got.Packages {
		if pkg.Installed {
			t.Fatalf("em falha de coleta nenhum item pode ser marcado instalado (%q)", pkg.ID)
		}
	}
}

// TestMarkCatalogInstalledEmptyListStaysFalse: lista vazia do winget nao pode
// marcar nada como instalado.
func TestMarkCatalogInstalledEmptyListStaysFalse(t *testing.T) {
	svc := &Service{installedPackages: installedPackagesFunc(nil, nil)}

	catalog := marketplaceCatalog(wingetItem("google.chrome.exe", "Google Chrome"))
	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if got.Packages[0].Installed {
		t.Fatalf("com lista de instalados vazia nada pode ser marcado instalado")
	}
}

// TestMarkCatalogInstalledChocolatey casa itens de origem Chocolatey pelo Id.
func TestMarkCatalogInstalledChocolatey(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "Notepad++", ID: "notepadplusplus", Version: "8.6", Source: "chocolatey"},
		}, nil),
	}

	catalog := marketplaceCatalog(models.AppItem{
		ID:               "notepadplusplus",
		Name:             "Notepad++",
		InstallationType: string(appstore.InstallationChocolatey),
	})
	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if !got.Packages[0].Installed {
		t.Fatalf("item Chocolatey instalado deveria ser marcado; veio false")
	}
}

// TestMarkCatalogInstalledAmbiguousNameNotMarked: homonimos sem versao que
// desempate sao inconclusivos — fail-safe, nao marca.
func TestMarkCatalogInstalledAmbiguousNameNotMarked(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "Contoso Suite", ID: "ARP\\Machine\\X86\\Contoso", Version: "1.0", Source: "winget"},
			{Name: "Contoso Suite", ID: "ARP\\Machine\\X64\\Contoso", Version: "2.0", Source: "winget"},
		}, nil),
	}

	catalog := marketplaceCatalog(wingetItem("contoso.suite", "Contoso Suite"))
	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if got.Packages[0].Installed {
		t.Fatalf("homonimos sem desempate por versao sao inconclusivos e nao podem marcar Installed=true")
	}
}

// TestMarkCatalogInstalledAmbiguousResolvedByVersion: a versao exata do catalogo
// desempata homonimos legitimos.
func TestMarkCatalogInstalledAmbiguousResolvedByVersion(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "Contoso Suite", ID: "ARP\\Machine\\X86\\Contoso", Version: "1.0", Source: "winget"},
			{Name: "Contoso Suite", ID: "ARP\\Machine\\X64\\Contoso", Version: "2.0", Source: "winget"},
		}, nil),
	}

	item := wingetItem("contoso.suite", "Contoso Suite")
	item.Version = "2.0"
	got := svc.MarkCatalogInstalled(context.Background(), marketplaceCatalog(item))

	if !got.Packages[0].Installed {
		t.Fatalf("a versao exata do catalogo deveria desempatar os homonimos")
	}
}

// TestMarkCatalogInstalledCustomNotRegressed: o comportamento Custom
// (InventoryLink) precisa continuar exatamente como antes.
func TestMarkCatalogInstalledCustomNotRegressed(t *testing.T) {
	fakeInv := &fakeSoftwareInventory{software: []models.SoftwareItem{
		{Name: "Foxit PDF Reader", Version: "2026.2"},
	}}
	svc := &Service{
		inventory: fakeInv,
		// Sem itens de marketplace: a lista do gerenciador nem deve ser usada.
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "Nao Deve Ser Usado", ID: "x.y", Source: "winget"},
		}, nil),
	}

	catalog := marketplaceCatalog(
		models.AppItem{
			ID:               "custom-1",
			Name:             "Foxit",
			InstallationType: string(appstore.InstallationCustom),
			DetectionMode:    appstore.DetectionModeInventoryLink,
			DetectionName:    "foxit pdf reader",
		},
		models.AppItem{
			ID:               "custom-2",
			Name:             "Ausente",
			InstallationType: string(appstore.InstallationCustom),
			DetectionMode:    appstore.DetectionModeInventoryLink,
			DetectionName:    "app inexistente",
		},
	)

	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if !got.Packages[0].Installed {
		t.Fatalf("custom-1 deveria continuar sendo detectado pelo inventario")
	}
	if got.Packages[1].Installed {
		t.Fatalf("custom-2 nao existe no inventario e nao pode ser marcado")
	}
	if fakeInv.calls != 1 {
		t.Fatalf("o inventario deveria ser coletado uma unica vez, foi %d", fakeInv.calls)
	}
}

// TestMarkCatalogInstalledGenericNameNotMarked protege contra o falso positivo de
// nome genérico: um item de catálogo chamado "Setup"/"Driver"/"Update" NÃO pode
// ser marcado como instalado só porque existe uma linha ARP homônima de outro
// produto. É o mesmo bug relatado, invertido.
func TestMarkCatalogInstalledGenericNameNotMarked(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "Setup", ID: "ARP\\Machine\\X64\\SomeVendor", Version: "1.0", Source: "winget"},
			{Name: "Driver", ID: "ARP\\Machine\\X64\\OtherVendor", Version: "2.0", Source: "winget"},
			{Name: "Update", ID: "ARP\\Machine\\X64\\ThirdVendor", Version: "3.0", Source: "winget"},
			{Name: "Runtime", ID: "ARP\\Machine\\X64\\FourthVendor", Version: "4.0", Source: "winget"},
		}, nil),
	}

	for _, name := range []string{"Setup", "Driver", "Update", "Runtime"} {
		catalog := marketplaceCatalog(wingetItem("vendor."+strings.ToLower(name), name))
		got := svc.MarkCatalogInstalled(context.Background(), catalog)
		if got.Packages[0].Installed {
			t.Fatalf("nome generico %q foi marcado como instalado por um homonimo de outro produto (falso positivo)", name)
		}
	}
}

// TestMarkCatalogInstalledInsidersVariantNotConfused trava a regressao do caso
// classico: "Visual Studio Code" (stable) não pode ser marcado instalado por
// causa do "Visual Studio Code - Insiders" instalado.
func TestMarkCatalogInstalledInsidersVariantNotConfused(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "Visual Studio Code - Insiders", ID: "Microsoft.VisualStudioCode.Insiders", Version: "1.99", Source: "winget"},
		}, nil),
	}

	catalog := marketplaceCatalog(wingetItem("microsoft.visualstudiocode", "Visual Studio Code"))
	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if got.Packages[0].Installed {
		t.Fatalf("VS Code stable foi marcado instalado por causa do VS Code Insiders instalado (falso positivo)")
	}
}

// TestMarkCatalogInstalledLocaleMismatchNotMarked: um item localizado "(pt-BR)"
// não pode casar com uma instalação de OUTRO idioma.
func TestMarkCatalogInstalledLocaleMismatchNotMarked(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "Mozilla Firefox (x64 en-US)", ID: "ARP\\Machine\\X64\\Mozilla Firefox", Version: "159.0.1", Source: "winget"},
		}, nil),
	}

	catalog := marketplaceCatalog(wingetItem("mozilla.firefox.pt-br", "Mozilla Firefox (pt-BR)"))
	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if got.Packages[0].Installed {
		t.Fatalf("Firefox pt-BR nao pode ser marcado instalado por uma instalacao en-US")
	}
}

// TestMarkCatalogInstalledLocaleMatchMarks confirma o caso positivo de idioma:
// catálogo "(pt-BR)" casa com instalação "(x64 pt-BR)".
func TestMarkCatalogInstalledLocaleMatchMarks(t *testing.T) {
	svc := &Service{
		installedPackages: installedPackagesFunc([]models.InstalledPackage{
			{Name: "Mozilla Firefox (x64 pt-BR)", ID: "ARP\\Machine\\X64\\Mozilla Firefox", Version: "159.0.1", Source: "winget"},
		}, nil),
	}

	catalog := marketplaceCatalog(wingetItem("mozilla.firefox.pt-br", "Mozilla Firefox (pt-BR)"))
	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if !got.Packages[0].Installed {
		t.Fatalf("Firefox pt-BR deveria casar com a instalacao x64 pt-BR pelo nome de produto + idioma")
	}
}
