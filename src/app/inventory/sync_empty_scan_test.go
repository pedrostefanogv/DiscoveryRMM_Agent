package inventory

import (
	"context"
	"testing"
	"time"

	"discovery/app/core/models"
)

// ── Rede de segurança: scan vazio transitório NÃO pode limpar o servidor ──

// TestLoadPendingUpdates_KeepsLastGoodListOnSuspiciousEmptyScan reproduz o
// cenário que apagou os updates em produção: o "winget upgrade" devolve tabela
// vazia (timeout/fonte indisponível) logo após um scan bom. Enviar esse vazio
// zerava updateAvailable de todos os apps no servidor.
func TestLoadPendingUpdates_KeepsLastGoodListOnSuspiciousEmptyScan(t *testing.T) {
	calls := 0
	svc := &Service{
		logf: func(string) {},
		pendingUpdates: func(context.Context) ([]models.UpgradeItem, error) {
			calls++
			if calls == 1 {
				return []models.UpgradeItem{{Name: "Kudu 3.3.0", ID: "AdventDevelopmentInc.Kudu", AvailableVersion: "3.4.0"}}, nil
			}
			return nil, nil // scan vazio transitório
		},
	}

	first := svc.loadPendingUpdates(context.Background())
	if len(first) != 1 {
		t.Fatalf("primeiro scan deveria trazer 1 update, veio %d", len(first))
	}

	// Invalida o cache (como faz o refresh manual) e força novo scan vazio.
	svc.InvalidatePendingUpdatesCache()

	second := svc.loadPendingUpdates(context.Background())
	if len(second) != 1 {
		t.Fatalf("scan vazio suspeito NAO pode limpar a lista (veio %d)", len(second))
	}
	if second[0].ID != "AdventDevelopmentInc.Kudu" {
		t.Fatalf("lista preservada incorreta: %+v", second)
	}
}

// TestLoadPendingUpdates_AcceptsEmptyScanAfterGrace garante que o vazio é
// aceito como verdade depois da janela de graça (senão um update removido de
// fato nunca sairia do dashboard).
func TestLoadPendingUpdates_AcceptsEmptyScanAfterGrace(t *testing.T) {
	calls := 0
	svc := &Service{
		logf: func(string) {},
		pendingUpdates: func(context.Context) ([]models.UpgradeItem, error) {
			calls++
			if calls == 1 {
				return []models.UpgradeItem{{Name: "antigo", ID: "antigo.pkg"}}, nil
			}
			return nil, nil
		},
	}

	if got := svc.loadPendingUpdates(context.Background()); len(got) != 1 {
		t.Fatalf("primeiro scan deveria trazer 1 update, veio %d", len(got))
	}

	// Envelhece o carimbo do último scan bom além da janela de graça.
	svc.pendingUpdatesMu.Lock()
	svc.pendingUpdatesLastAt = time.Now().Add(-(emptyScanGraceTTL + time.Minute))
	svc.pendingUpdatesGoodAt = svc.pendingUpdatesLastAt
	svc.pendingUpdatesMu.Unlock()

	if got := svc.loadPendingUpdates(context.Background()); len(got) != 0 {
		t.Fatalf("apos a janela de graca o vazio deve ser aceito, veio %d", len(got))
	}
}

// TestLoadInstalledPackages_KeepsLastGoodListOnSuspiciousEmptyScan garante a
// mesma proteção para o "winget list" (fonte dos updatePackageId).
func TestLoadInstalledPackages_KeepsLastGoodListOnSuspiciousEmptyScan(t *testing.T) {
	calls := 0
	svc := &Service{
		logf: func(string) {},
		installedPackages: func(context.Context) ([]models.InstalledPackage, error) {
			calls++
			if calls == 1 {
				return []models.InstalledPackage{{Name: "Kudu 3.3.0", ID: "AdventDevelopmentInc.Kudu", Version: "3.3.0"}}, nil
			}
			return nil, nil
		},
	}

	if got := svc.loadInstalledPackages(context.Background()); len(got) != 1 {
		t.Fatalf("primeira listagem deveria trazer 1 pacote, veio %d", len(got))
	}

	svc.InvalidatePendingUpdatesCache()

	if got := svc.loadInstalledPackages(context.Background()); len(got) != 1 {
		t.Fatalf("winget list vazio suspeito NAO pode limpar a lista (veio %d)", len(got))
	}
}
