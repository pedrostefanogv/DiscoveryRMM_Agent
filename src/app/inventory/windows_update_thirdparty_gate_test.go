package inventory

// D22: origem "third_party" da política de patches — quando DESATIVADA (valor
// resolvido que o servidor envia em /me/configuration), o agente NEM coleta
// apps de terceiros (choco/winget) nem os reporta; a sonda de mudança também
// não executa os providers.

import (
	"context"
	"errors"
	"testing"

	"discovery/app/core/thirdparty"
)

type fakeThirdPartyProvider struct {
	source string
	calls  int
	apps   []thirdparty.AppUpdate
}

func (p *fakeThirdPartyProvider) Source() string { return p.source }

func (p *fakeThirdPartyProvider) Inventory(context.Context) ([]thirdparty.AppUpdate, error) {
	p.calls++
	return p.apps, nil
}

func (p *fakeThirdPartyProvider) Upgrade(context.Context, string) (string, error) {
	return "", errors.New("não usado neste teste")
}

func newThirdPartyGateService(gate func() bool) *Service {
	svc := NewService(Options{})
	svc.SetWindowsUpdateDeps(WindowsUpdateDeps{
		Client:            &fakeWUClient{result: sampleScanResult()},
		Enabled:           func() bool { return true },
		ThirdPartyEnabled: gate,
	})
	return svc
}

func TestThirdPartyEnabledDefaultNilKeepsCollecting(t *testing.T) {
	// Servidor antigo não envia o campo: nil = mantém a coleta (compat).
	svc := newThirdPartyGateService(nil)
	if !svc.ThirdPartyEnabled() {
		t.Fatal("nil deps deveria manter a coleta de terceiros (compat)")
	}
}

func TestThirdPartyDisabledSkipsCollectionOnScan(t *testing.T) {
	provider := &fakeThirdPartyProvider{
		source: "choco",
		apps:   []thirdparty.AppUpdate{{Source: "choco", ID: "brave", Name: "Brave"}},
	}
	svc := newThirdPartyGateService(func() bool { return false })
	svc.SetThirdPartyProviders([]thirdparty.Provider{provider})

	result, err := svc.ScanWindowsUpdates(context.Background())
	if err != nil {
		t.Fatalf("scan falhou: %v", err)
	}
	for _, update := range result.Updates {
		if thirdparty.IsThirdPartySource(update.Source) {
			t.Fatalf("origem desativada não deveria coletar %q no inventário", update.UpdateKey)
		}
	}
	if provider.calls != 0 {
		t.Fatalf("provider chamado %d vez(es) com a origem desativada; want 0", provider.calls)
	}
}

func TestThirdPartyEnabledCollectsOnScan(t *testing.T) {
	provider := &fakeThirdPartyProvider{
		source: "choco",
		apps:   []thirdparty.AppUpdate{{Source: "choco", ID: "brave", Name: "Brave"}},
	}
	svc := newThirdPartyGateService(func() bool { return true })
	svc.SetThirdPartyProviders([]thirdparty.Provider{provider})

	result, err := svc.ScanWindowsUpdates(context.Background())
	if err != nil {
		t.Fatalf("scan falhou: %v", err)
	}
	found := false
	for _, update := range result.Updates {
		if thirdparty.IsThirdPartySource(update.Source) {
			found = true
		}
	}
	if !found {
		t.Fatal("origem habilitada deveria incluir apps de terceiros no inventário")
	}
	if provider.calls == 0 {
		t.Fatal("provider não foi consultado com a origem habilitada")
	}
}

func TestThirdPartyDisabledPollNeverRunsProviders(t *testing.T) {
	provider := &fakeThirdPartyProvider{source: "winget"}
	svc := newThirdPartyGateService(func() bool { return false })
	svc.SetThirdPartyProviders([]thirdparty.Provider{provider})

	changed, err := svc.ThirdPartyInventoryChanged(context.Background())
	if err != nil {
		t.Fatalf("sonda falhou: %v", err)
	}
	if changed {
		t.Fatal("origem desativada não deveria reportar mudança")
	}
	if provider.calls != 0 {
		t.Fatalf("sonda executou provider %d vez(es) com a origem desativada; want 0", provider.calls)
	}
}

func TestThirdPartyGateSwapsDynamically(t *testing.T) {
	// A política pode mudar entre scans: a closure lê a configuração fresca.
	enabled := false
	provider := &fakeThirdPartyProvider{
		source: "choco",
		apps:   []thirdparty.AppUpdate{{Source: "choco", ID: "brave", Name: "Brave"}},
	}
	svc := newThirdPartyGateService(func() bool { return enabled })
	svc.SetThirdPartyProviders([]thirdparty.Provider{provider})

	if _, err := svc.ScanWindowsUpdates(context.Background()); err != nil {
		t.Fatalf("scan falhou: %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider consultado com origem desativada; want 0, got %d", provider.calls)
	}

	enabled = true
	if _, err := svc.ScanWindowsUpdates(context.Background()); err != nil {
		t.Fatalf("scan falhou: %v", err)
	}
	if provider.calls == 0 {
		t.Fatal("provider não consultado após habilitar a origem")
	}
}


