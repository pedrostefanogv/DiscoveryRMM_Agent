package inventory

import (
	"context"
	"sync"
	"testing"
	"time"

	"discovery/app/appstore"
)

// fakeAppsSerializado é um AppsService que conta quantas execuções estavam
// CONCORRENTES (máximo de "em voo") nas operações de pacote.
type fakeAppsSerializado struct {
	mu        sync.Mutex
	inFlight  int
	maxFlight int
	calls     int
}

func (f *fakeAppsSerializado) run() {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxFlight {
		f.maxFlight = f.inFlight
	}
	f.calls++
	f.mu.Unlock()
	// Simula um winget/choco ocupado o suficiente para sobreposição se a fila
	// única não serializar.
	time.Sleep(30 * time.Millisecond)
	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()
}

func (f *fakeAppsSerializado) Install(ctx context.Context, id string) (string, error) {
	f.run()
	return "install " + id, nil
}

func (f *fakeAppsSerializado) Uninstall(ctx context.Context, id string) (string, error) {
	f.run()
	return "uninstall " + id, nil
}

func (f *fakeAppsSerializado) Upgrade(ctx context.Context, id string) (string, error) {
	f.run()
	return "upgrade " + id, nil
}

func (f *fakeAppsSerializado) UpgradeAll(ctx context.Context) (string, error) {
	f.run()
	return "upgrade-all", nil
}

func (f *fakeAppsSerializado) ListInstalled(ctx context.Context) (string, error) {
	return "", nil
}

func newQueueTestService(apps AppsService) *Service {
	return NewService(Options{
		Apps: apps,
		ResolveAllowed: func(context.Context, string) (appstore.Item, error) {
			return appstore.Item{InstallationType: string(appstore.InstallationWinget)}, nil
		},
		ResolveAllowedByType: func(context.Context, string, string) (appstore.Item, error) {
			return appstore.Item{InstallationType: string(appstore.InstallationWinget)}, nil
		},
		BeginActivity: func(string) func() { return nil },
		Logf:          func(string) {},
		Ctx:           func() context.Context { return context.Background() },
	})
}

// Dois upgrades concorrentes NÃO podem se sobrepor: a fila única (packageOpMu)
// serializa as operações de pacote — o máximo em voo no gerenciador é 1.
func TestUpgradeConcorrenteSerializado(t *testing.T) {
	apps := &fakeAppsSerializado{}
	svc := newQueueTestService(apps)
	// Sem refresh de inventário pós-operação no teste (timer não deve disparar).
	svc.postInstallInventoryRefreshDelay = 0

	const n = 4
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.Upgrade("winget::fila.pacote")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("upgrade %d falhou: %v", i, err)
		}
	}
	apps.mu.Lock()
	defer apps.mu.Unlock()
	if apps.calls != n {
		t.Fatalf("esperava %d chamadas, veio %d", n, apps.calls)
	}
	if apps.maxFlight != 1 {
		t.Fatalf("operação concorrente detectada: maxFlight=%d (esperava 1)", apps.maxFlight)
	}
}

// Instalação (UI) e upgrade (auto-update/automação) compartilham a MESMA fila:
// um Install em curso segura o Upgrade esperando.
func TestInstallEUpgradeCompartilhamFila(t *testing.T) {
	apps := &fakeAppsSerializado{}
	svc := newQueueTestService(apps)
	svc.postInstallInventoryRefreshDelay = 0

	var wg sync.WaitGroup
	results := make([]string, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		out, err := svc.Install("fila.instalar")
		if err == nil {
			results[0] = out
		}
	}()
	go func() {
		defer wg.Done()
		out, err := svc.Upgrade("winget::fila.atualizar")
		if err == nil {
			results[1] = out
		}
	}()
	wg.Wait()

	apps.mu.Lock()
	defer apps.mu.Unlock()
	if apps.calls != 2 {
		t.Fatalf("esperava 2 chamadas, veio %d", apps.calls)
	}
	if apps.maxFlight != 1 {
		t.Fatalf("operação concorrente detectada: maxFlight=%d (esperava 1)", apps.maxFlight)
	}
	if results[0] == "" || results[1] == "" {
		t.Fatalf("resultados vazios: %v", results)
	}
}
