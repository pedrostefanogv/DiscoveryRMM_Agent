package inventory

import (
	"testing"

	"discovery/app/core/models"
	"discovery/app/updates"
)

// ── Correlação de updates: o nome do registro difere do nome/Id do gerenciador ──

// TestBuildAgentSoftwareEnvelope_MatchesChocoByNuspecTitle cobre o caso em que
// o app é instalado por Chocolatey: o inventário de registro guarda o TÍTULO
// ("Visual Studio 2022 Build Tools") e o "choco outdated" só devolve o Id
// ("visualstudio2022buildtools"), que não aparece no "winget list".
func TestBuildAgentSoftwareEnvelope_MatchesChocoByNuspecTitle(t *testing.T) {
	report := models.InventoryReport{
		Software: []models.SoftwareItem{
			{Name: "Visual Studio 2022 Build Tools", Version: "117.14.37", Source: "registry"},
		},
	}
	pending := []models.UpgradeItem{
		{Name: "visualstudio2022buildtools", ID: "visualstudio2022buildtools", CurrentVersion: "117.14.37", AvailableVersion: "117.14.41", Source: "chocolatey"},
	}
	displayNames := installedDisplayNameIndex{byName: map[string][]models.InstalledPackage{
		normalizeUpdateKey("Visual Studio 2022 Build Tools"): {
			{Name: "Visual Studio 2022 Build Tools", ID: "visualstudio2022buildtools", Version: "117.14.37", Source: "chocolatey"},
		},
	}}

	_env := buildAgentSoftwareEnvelopeWithIndex(report, "agent-1", pending, nil, nil, displayNames)
	item := _env.Software[0]
	update := models.UpgradeItem{ID: item.UpdatePackageID, AvailableVersion: item.AvailableVersion, Source: item.UpdateSource}
	ok := item.UpdateAvailable
	if !ok {
		t.Fatalf("titulo do nuspec nao correlacionou o update choco")
	}
	if update.AvailableVersion != "117.14.41" || update.ID != "visualstudio2022buildtools" {
		t.Fatalf("update = %+v", update)
	}
	if got := normalizeSoftwareUpdateSource(update.Source); got != "chocolatey" {
		t.Fatalf("updateSource = %q", got)
	}
}

// TestBuildAgentSoftwareEnvelope_MatchesWingetDisplayName garante que o nome de
// exibição do "winget list" (que difere do nome do registro e da linha de
// upgrade) resolve o Id do pacote — caso do "Subsistema do Windows para Linux"
// (registro) / "Windows Subsystem for Linux" (winget list) / Microsoft.WSL.
func TestBuildAgentSoftwareEnvelope_MatchesWingetDisplayName(t *testing.T) {
	report := models.InventoryReport{
		Software: []models.SoftwareItem{
			{Name: "Subsistema do Windows para Linux", Version: "2.7.11.0", Source: "registry"},
		},
	}
	pending := []models.UpgradeItem{
		{Name: "Subsistema do Windows para Linux", ID: "Microsoft.WSL", CurrentVersion: "2.7.11.0", AvailableVersion: "2.7.13", Source: "winget"},
	}
	installed := []models.InstalledPackage{
		{Name: "Windows Subsystem for Linux", ID: "Microsoft.WSL", Version: "2.7.11.0", Source: "winget"},
	}

	env := buildAgentSoftwareEnvelope(report, "agent-1", pending, installed, nil)
	item, ok := findSoftwareByName(env, "Subsistema do Windows para Linux")
	if !ok {
		t.Fatalf("app do registro nao esta no envelope: %+v", env.Software)
	}
	if !item.UpdateAvailable || item.UpdatePackageID != "Microsoft.WSL" || item.AvailableVersion != "2.7.13" {
		t.Fatalf("correlacao pelo nome do winget list falhou: %+v", item)
	}
}

// TestParseInstalledListOutput_NameDiffersFromRegistry garante que o índice
// local carrega o NOME de exibição do winget (antes só o Id era aproveitado).
func TestParseInstalledListOutput_NameDiffersFromRegistry(t *testing.T) {
	raw := "Name                                 Id                         Version    Available  Source\r\n" +
		"--------------------------------------------------------------------------------------\r\n" +
		"Windows Subsystem for Linux          Microsoft.WSL              2.7.11.0   2.7.13     winget\r\n"
	items := updates.ParseInstalledListOutput(raw)
	if len(items) != 1 {
		t.Fatalf("esperado 1 pacote, veio %d", len(items))
	}
	if items[0].Name != "Windows Subsystem for Linux" || items[0].ID != "Microsoft.WSL" {
		t.Fatalf("pacote = %+v", items[0])
	}
}

// ── Apps que só existem no gerenciador de pacotes ──

func TestMergePackageManagerSoftware_AddsPendingUpdateWithoutRegistryEntry(t *testing.T) {
	software := []models.SoftwareItem{
		{Name: "AnyDesk", Version: "ad 9.7.15", Source: "registry"},
	}
	installed := []models.InstalledPackage{
		{Name: "AnyDesk", ID: "AnyDesk.AnyDesk", Version: "ad 9.7.15", Source: "winget"},
	}
	pending := []models.UpgradeItem{
		{Name: "Subsistema do Windows para Linux", ID: "Microsoft.WSL", CurrentVersion: "2.7.11.0", AvailableVersion: "2.7.13", Source: "winget"},
	}

	merged := mergePackageManagerSoftware(software, installed, pending, "registry", installedDisplayNameIndex{})

	if len(merged) != 2 {
		t.Fatalf("esperado 2 itens (1 do registro + 1 so do gerenciador), veio %d: %+v", len(merged), merged)
	}
	added := merged[1]
	if added.Name != "Subsistema do Windows para Linux" || added.Version != "2.7.11.0" {
		t.Fatalf("item adicionado = %+v", added)
	}

	// O item adicionado deve ficar correlacionado ao update pendente.
	env := buildAgentSoftwareEnvelope(models.InventoryReport{Software: merged}, "agent-1", pending, installed, nil)
	found := false
	for _, item := range env.Software {
		if item.Name != "Subsistema do Windows para Linux" {
			continue
		}
		found = true
		if !item.UpdateAvailable || item.AvailableVersion != "2.7.13" || item.UpdatePackageID != "Microsoft.WSL" {
			t.Fatalf("app so-do-winget sem update: %+v", item)
		}
	}
	if !found {
		t.Fatalf("app do winget nao chegou ao envelope")
	}
}

func TestMergePackageManagerSoftware_IgnoresPackagesAlreadyInRegistry(t *testing.T) {
	software := []models.SoftwareItem{
		{Name: "AnyDesk", Version: "ad 9.7.15"},
		{Name: "7-Zip 26.03 (x64)", Version: "26.03"},
	}
	installed := []models.InstalledPackage{
		{Name: "AnyDesk", ID: "AnyDesk.AnyDesk", Version: "ad 9.7.15", Source: "winget"},
		{Name: "7-Zip", ID: "7zip.7zip", Version: "26.03", Source: "winget"},
		{Name: "   ", ID: "vazio", Version: "1.0"},
	}

	merged := mergePackageManagerSoftware(software, installed, nil, "registry", installedDisplayNameIndex{})
	if len(merged) != 3 {
		t.Fatalf("esperado 3 itens (2 do registro + 7-Zip implicitamente), veio %d: %+v", len(merged), merged)
	}
}

func TestMergePackageManagerSoftware_NoopWithoutPackageManagerData(t *testing.T) {
	software := []models.SoftwareItem{{Name: "AnyDesk", Version: "ad 9.7.15"}}
	merged := mergePackageManagerSoftware(software, nil, nil, "registry", installedDisplayNameIndex{})
	if len(merged) != 1 {
		t.Fatalf("sem dados do gerenciador a lista nao deve mudar, veio %+v", merged)
	}
}

// ── Duplicatas título × Id do Chocolatey ──
//
// O registro de ARP guarda o TÍTULO ("Adobe Acrobat Reader DC") e o
// "choco outdated"/"choco list" só devolvem o Id ("adobereader"). Sem cruzar
// esses apelidos, o merge acrescentava o Id como um SEGUNDO app, com a mesma
// versão e o mesmo update — a duplicata vista na aba Aplicativos.

func chocoDisplayIndex(title, id, version string) installedDisplayNameIndex {
	pkg := models.InstalledPackage{Name: title, ID: id, Version: version, Source: "chocolatey"}
	return installedDisplayNameIndex{
		byName: map[string][]models.InstalledPackage{normalizeUpdateKey(title): {pkg}},
		byID:   map[string][]models.InstalledPackage{normalizeUpdateKey(id): {pkg}},
	}
}

func TestMergePackageManagerSoftware_SkipsChocoIdWhenRegistryTitleExists(t *testing.T) {
	software := []models.SoftwareItem{
		{Name: "Adobe Acrobat Reader DC", Version: "2026.1.21662", Source: "registry"},
	}
	installed := []models.InstalledPackage{
		{Name: "adobereader", ID: "adobereader", Version: "2026.1.21662", Source: "chocolatey"},
	}
	pending := []models.UpgradeItem{
		{Name: "adobereader", ID: "adobereader", CurrentVersion: "2026.1.21662", AvailableVersion: "2026.2.21931", Source: "chocolatey"},
	}

	merged := mergePackageManagerSoftware(
		software, installed, pending, "registry",
		chocoDisplayIndex("Adobe Acrobat Reader DC", "adobereader", "2026.1.21662"))

	if len(merged) != 1 {
		t.Fatalf("Id do choco nao deveria duplicar o app do registro, veio %d: %+v", len(merged), merged)
	}
	if merged[0].Name != "Adobe Acrobat Reader DC" {
		t.Fatalf("nome do item = %q", merged[0].Name)
	}
}

func TestMergePackageManagerSoftware_NamesManagerOnlyAppByNuspecTitle(t *testing.T) {
	software := []models.SoftwareItem{{Name: "Outro App", Version: "1.0", Source: "registry"}}
	pending := []models.UpgradeItem{
		{Name: "fvm", ID: "fvm", CurrentVersion: "4.1.2", AvailableVersion: "4.3.1", Source: "chocolatey"},
	}

	merged := mergePackageManagerSoftware(
		software, nil, pending, "registry",
		chocoDisplayIndex("Flutter Version Management (FVM)", "fvm", "4.1.2"))

	if len(merged) != 2 {
		t.Fatalf("esperado 2 itens (registro + app so-do-choco), veio %d: %+v", len(merged), merged)
	}
	if merged[1].Name != "Flutter Version Management (FVM)" {
		t.Fatalf("app so-do-choco deveria usar o titulo do nuspec, veio %q", merged[1].Name)
	}
}

// TestBuildAgentSoftwareEnvelope_DoesNotDuplicateChocoTitleAndId fecha o caso
// de ponta a ponta: o envelope não pode conter o título do registro E o Id do
// choco para o mesmo aplicativo.
func TestBuildAgentSoftwareEnvelope_DoesNotDuplicateChocoTitleAndId(t *testing.T) {
	report := models.InventoryReport{
		Software: []models.SoftwareItem{
			{Name: "Visual Studio 2022 Build Tools", Version: "117.14.37", Source: "registry"},
		},
	}
	pending := []models.UpgradeItem{
		{Name: "visualstudio2022buildtools", ID: "visualstudio2022buildtools", CurrentVersion: "117.14.37", AvailableVersion: "117.14.41", Source: "chocolatey"},
	}
	index := chocoDisplayIndex("Visual Studio 2022 Build Tools", "visualstudio2022buildtools", "117.14.37")

	env := buildAgentSoftwareEnvelopeWithIndex(report, "agent-1", pending, nil, nil, index)

	if len(env.Software) != 1 {
		t.Fatalf("esperado 1 app (sem duplicata Id/titulo), veio %d: %+v", len(env.Software), env.Software)
	}
	item := env.Software[0]
	if !item.UpdateAvailable || item.UpdatePackageID != "visualstudio2022buildtools" || item.AvailableVersion != "117.14.41" {
		t.Fatalf("item sem update correlacionado: %+v", item)
	}
}
