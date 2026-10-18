//go:build windows

package inventory

import (
	"context"
	"strings"
	"testing"

	"discovery/app/appstore"
	"discovery/app/core/models"
)

// fakeSoftwareInventory implementa o InventoryService só com o que a detecção
// usa (CollectSoftware); os demais métodos devolvem vazio.
type fakeSoftwareInventory struct {
	software []models.SoftwareItem
	err      error
	calls    int
}

func (f *fakeSoftwareInventory) GetInventory(context.Context) (models.InventoryReport, error) {
	return models.InventoryReport{}, nil
}
func (f *fakeSoftwareInventory) GetNetworkConnections(context.Context) (models.NetworkConnectionsReport, error) {
	return models.NetworkConnectionsReport{}, nil
}
func (f *fakeSoftwareInventory) CollectSoftware(context.Context) ([]models.SoftwareItem, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.software, nil
}
func (f *fakeSoftwareInventory) CollectStartupItems(context.Context) ([]models.StartupItem, error) {
	return nil, nil
}
func (f *fakeSoftwareInventory) CollectScheduledTasks(context.Context) ([]models.ScheduledTaskInfo, error) {
	return nil, nil
}
func (f *fakeSoftwareInventory) CollectListeningPorts(context.Context) ([]models.ListeningPortInfo, error) {
	return nil, nil
}

func TestIsCustomPackageInstalledNoneModeIsFailSafe(t *testing.T) {
	fake := &fakeSoftwareInventory{software: []models.SoftwareItem{{Name: "Foxit PDF Reader", Version: "2026.2"}}}
	svc := &Service{inventory: fake}

	cases := []appstore.Item{
		{}, // DetectionMode None
		{DetectionMode: appstore.DetectionModeNone, DetectionName: "Foxit PDF Reader"},
		{DetectionMode: appstore.DetectionModeInventoryLink}, // sem nome
		{DetectionMode: appstore.DetectionModeCommand},       // sem comando
		{DetectionMode: 99, DetectionCommand: "exit 0"},      // modo desconhecido
	}
	for _, item := range cases {
		installed, detail := svc.IsCustomPackageInstalled(context.Background(), item)
		if installed {
			t.Fatalf("modo %d sem configuracao valida nao pode ser instalado (detail=%q)", item.DetectionMode, detail)
		}
	}
	if fake.calls != 0 {
		t.Fatalf("nenhuma coleta deveria ter acontecido para modos sem deteccao, houve %d", fake.calls)
	}
}

func TestIsCustomPackageInstalledInventoryLinkMatchesNameIgnoringAccentsAndCase(t *testing.T) {
	fake := &fakeSoftwareInventory{software: []models.SoftwareItem{
		{Name: "Aplicação Móvel Sênior", Version: "3.1.0", Publisher: "Acme Ltda"},
		{Name: "Outro App", Version: "1.0.0"},
	}}
	svc := &Service{inventory: fake}

	installed, detail := svc.IsCustomPackageInstalled(context.Background(), appstore.Item{
		DetectionMode: appstore.DetectionModeInventoryLink,
		DetectionName: "aplicacao movel senior",
	})
	if !installed {
		t.Fatalf("nome sem acento/minusculo deveria casar, detail=%q", detail)
	}
	if !strings.Contains(detail, "3.1.0") {
		t.Fatalf("detail deveria trazer a versao encontrada, veio %q", detail)
	}
	if fake.calls != 1 {
		t.Fatalf("deveria coletar o inventario uma vez, coletou %d", fake.calls)
	}
}

func TestIsCustomPackageInstalledInventoryLinkPublisherAndMinVersion(t *testing.T) {
	fake := &fakeSoftwareInventory{software: []models.SoftwareItem{
		{Name: "Contoso Suite", Version: "1.9.0", Publisher: "Contoso"},
		{Name: "Contoso Suite", Version: "2.4.0", Publisher: "Outra Editora"},
	}}
	svc := &Service{inventory: fake}

	// Fabricante informado: só a linha da Contoso serve; versão mínima 2.0 não
	// é atendida por ela → não instalado.
	installed, _ := svc.IsCustomPackageInstalled(context.Background(), appstore.Item{
		DetectionMode:       appstore.DetectionModeInventoryLink,
		DetectionName:       "contoso suite",
		DetectionPublisher:  "Contoso",
		DetectionMinVersion: "2.0.0",
	})
	if installed {
		t.Fatal("versao 1.9.0 nao atende a minima 2.0.0")
	}

	// Sem fabricante, a linha de 2.4.0 atende a mínima.
	installed, detail := svc.IsCustomPackageInstalled(context.Background(), appstore.Item{
		DetectionMode:       appstore.DetectionModeInventoryLink,
		DetectionName:       "contoso suite",
		DetectionMinVersion: "2.0.0",
	})
	if !installed {
		t.Fatalf("versao 2.4.0 deveria atender a minima 2.0.0, detail=%q", detail)
	}

	// Fabricante que não existe em nenhuma linha.
	installed, _ = svc.IsCustomPackageInstalled(context.Background(), appstore.Item{
		DetectionMode:      appstore.DetectionModeInventoryLink,
		DetectionName:      "contoso suite",
		DetectionPublisher: "Fabrica Inexistente",
	})
	if installed {
		t.Fatal("fabricante divergente nao pode ser considerado instalado")
	}
}

func TestIsCustomPackageInstalledInventoryLinkFailSafeOnError(t *testing.T) {
	fake := &fakeSoftwareInventory{err: context.DeadlineExceeded}
	svc := &Service{inventory: fake}

	installed, detail := svc.IsCustomPackageInstalled(context.Background(), appstore.Item{
		DetectionMode: appstore.DetectionModeInventoryLink,
		DetectionName: "qualquer app",
	})
	if installed {
		t.Fatal("erro de coleta nao pode resultar em instalado")
	}
	if !strings.Contains(detail, "inventario") && !strings.Contains(detail, "inventário") {
		t.Fatalf("detalhe deveria explicar a falha de coleta, veio %q", detail)
	}
}

func TestIsCustomPackageInstalledInventoryLinkWithoutCollector(t *testing.T) {
	svc := &Service{}
	installed, detail := svc.IsCustomPackageInstalled(context.Background(), appstore.Item{
		DetectionMode: appstore.DetectionModeInventoryLink,
		DetectionName: "qualquer app",
	})
	if installed {
		t.Fatalf("sem provider de inventario nao pode ser instalado (detail=%q)", detail)
	}
}

func TestIsCustomPackageInstalledCommandModeUsesExitCode(t *testing.T) {
	svc := &Service{}

	installed, detail := svc.IsCustomPackageInstalled(context.Background(), appstore.Item{
		DetectionMode:    appstore.DetectionModeCommand,
		DetectionCommand: "exit 0",
	})
	if !installed {
		t.Fatalf("exit 0 deveria ser tratado como instalado, detail=%q", detail)
	}

	installed, detail = svc.IsCustomPackageInstalled(context.Background(), appstore.Item{
		DetectionMode:    appstore.DetectionModeCommand,
		DetectionCommand: "exit 1",
	})
	if installed {
		t.Fatal("exit != 0 nao pode ser tratado como instalado")
	}
	if !strings.Contains(detail, "exit status 1") {
		t.Fatalf("detalhe deveria registrar o exit code, veio %q", detail)
	}

	// Comando inválido: erro de execução não derruba nada e não vira instalado.
	installed, detail = svc.IsCustomPackageInstalled(context.Background(), appstore.Item{
		DetectionMode:    appstore.DetectionModeCommand,
		DetectionCommand: "isto-nao-e-um-comando-valido discovery",
	})
	if installed {
		t.Fatalf("comando invalido nao pode resultar em instalado (detail=%q)", detail)
	}
}

func TestMarkCatalogInstalledCollectsInventoryOnce(t *testing.T) {
	fake := &fakeSoftwareInventory{software: []models.SoftwareItem{
		{Name: "App Detectado", Version: "2.0.0"},
	}}
	svc := &Service{inventory: fake}

	catalog := models.Catalog{Packages: []models.AppItem{
		{
			ID:               "custom-1",
			InstallationType: string(appstore.InstallationCustom),
			DetectionMode:    appstore.DetectionModeInventoryLink,
			DetectionName:    "app detectado",
		},
		{
			ID:               "custom-2",
			InstallationType: string(appstore.InstallationCustom),
			DetectionMode:    appstore.DetectionModeInventoryLink,
			DetectionName:    "app ausente",
		},
		{
			ID:               "custom-3",
			InstallationType: string(appstore.InstallationCustom),
			// DetectionMode Command não deve rodar PowerShell na listagem.
			DetectionMode:    appstore.DetectionModeCommand,
			DetectionCommand: "exit 0",
		},
		{
			ID:               "winget-1",
			InstallationType: "Winget",
			DetectionMode:    appstore.DetectionModeInventoryLink,
			DetectionName:    "app detectado",
		},
	}}

	got := svc.MarkCatalogInstalled(context.Background(), catalog)

	if !got.Packages[0].Installed {
		t.Fatal("pacote 0 deveria estar marcado como instalado")
	}
	if got.Packages[1].Installed {
		t.Fatal("pacote 1 não existe no inventário e não pode ser marcado")
	}
	if got.Packages[2].Installed {
		t.Fatal("DetectionMode Command não pode ser avaliado na listagem")
	}
	if got.Packages[3].Installed {
		t.Fatal("pacote Winget não participa da detecção custom")
	}
	if fake.calls != 1 {
		t.Fatalf("deveria haver UMA coleta de inventário por montagem, houve %d", fake.calls)
	}
}

func TestMarkCatalogInstalledSkipsCollectionWhenNotNeeded(t *testing.T) {
	fake := &fakeSoftwareInventory{}
	svc := &Service{inventory: fake}

	catalog := models.Catalog{Packages: []models.AppItem{
		{ID: "winget-1", InstallationType: "Winget"},
		{ID: "custom-1", InstallationType: string(appstore.InstallationCustom), DetectionMode: appstore.DetectionModeNone},
	}}

	_ = svc.MarkCatalogInstalled(context.Background(), catalog)
	if fake.calls != 0 {
		t.Fatalf("sem itens InventoryLink nao deveria coletar inventario, coletou %d", fake.calls)
	}
}

// KnownCustomPackageInstalled é o caminho usado pela automação: usa o Installed
// já calculado do catálogo (InventoryLink) sem reexecutar comando.
func TestKnownCustomPackageInstalledUsesCatalogInstalledFlag(t *testing.T) {
	catalog := models.Catalog{Packages: []models.AppItem{
		{
			ID:               "custom-1",
			InstallationType: string(appstore.InstallationCustom),
			DetectionMode:    appstore.DetectionModeInventoryLink,
			DetectionName:    "App Instalado",
			Installed:        true,
		},
		{
			ID:               "custom-2",
			InstallationType: string(appstore.InstallationCustom),
			DetectionMode:    appstore.DetectionModeInventoryLink,
			DetectionName:    "App Ausente",
		},
	}}

	svc := &Service{getCatalog: func(context.Context) (models.Catalog, error) { return catalog, nil }}

	known, installed := svc.KnownCustomPackageInstalled("Custom", "custom-1")
	if !known || !installed {
		t.Fatalf("custom-1 deveria estar known+installed, veio (%v,%v)", known, installed)
	}
	known, installed = svc.KnownCustomPackageInstalled("Custom", "custom-2")
	if !known || installed {
		t.Fatalf("custom-2 deveria estar known e nao instalado, veio (%v,%v)", known, installed)
	}

	// Pacote sem detecção configurada: sem evidência → não pular.
	catalog.Packages = append(catalog.Packages, models.AppItem{ID: "custom-3", InstallationType: "Custom"})
	known, installed = svc.KnownCustomPackageInstalled("Custom", "custom-3")
	if known || installed {
		t.Fatalf("sem deteccao configurada o resultado deve ser (false,false), veio (%v,%v)", known, installed)
	}

	// Catálogo indisponível: fail-safe.
	svcWithoutCatalog := &Service{}
	known, installed = svcWithoutCatalog.KnownCustomPackageInstalled("Custom", "custom-1")
	if known || installed {
		t.Fatalf("sem catalogo o resultado deve ser (false,false), veio (%v,%v)", known, installed)
	}
}
