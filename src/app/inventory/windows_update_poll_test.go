package inventory

// Testes da sonda barata e do scan sob demanda (sem WUA: cliente falso).

import (
	"context"
	"sync"
	"testing"
	"time"

	"discovery/app/core/windowsupdate"
	"discovery/app/debug"
)

type fakeWUClient struct {
	mu       sync.Mutex
	state    windowsupdate.WindowsUpdateState
	probes   int
	scans    int
	scanErr  error
	result   *windowsupdate.ScanResult
}

func (c *fakeWUClient) Probe(context.Context) windowsupdate.ProbeInfo {
	return windowsupdate.ProbeInfo{Supported: true}
}

func (c *fakeWUClient) ProbeState(context.Context) windowsupdate.WindowsUpdateState {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probes++
	return c.state
}

func (c *fakeWUClient) setState(state windowsupdate.WindowsUpdateState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = state
}

func (c *fakeWUClient) Scan(_ context.Context, _ windowsupdate.ScanOptions) (*windowsupdate.ScanResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scans++
	return c.result, c.scanErr
}

func (c *fakeWUClient) scanCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scans
}

func (c *fakeWUClient) Install(context.Context, []windowsupdate.InstallTarget) (*windowsupdate.InstallResult, error) {
	return &windowsupdate.InstallResult{}, nil
}

func (c *fakeWUClient) Hide(context.Context, []string, bool) (int, error) { return 0, nil }

func resetWindowsUpdatePollState() {
	wuPollMu.Lock()
	wuLastSign = ""
	wuLastSignAt = time.Time{}
	wuLastChangeScanAt = time.Time{}
	wuLastPollInfo = WindowsUpdatePollResult{}
	wuPollMu.Unlock()
}

func newWindowsUpdateTestService(client windowsupdate.Client) *Service {
	svc := NewService(Options{});
	svc.SetWindowsUpdateDeps(WindowsUpdateDeps{
		Client:  client,
		Enabled: func() bool { return true },
	});
	// Sem credenciais: o upload e ignorado (logado), mas o scan roda.
	svc.debugConfig = func() debug.Config { return debug.Config{AgentID: "agent-1"} }
	return svc
}

func TestPollWindowsUpdateChangesTriggersScanOnSignatureChange(t *testing.T) {
	resetWindowsUpdatePollState()
	client := &fakeWUClient{
		state:  windowsupdate.WindowsUpdateState{Supported: true, ServiceState: "running", CheckedAt: time.Now().UTC()},
		result: sampleScanResult(),
	}
	svc := newWindowsUpdateTestService(client)

	first := svc.PollWindowsUpdateChanges(context.Background(), 0, "change")
	if !first.Changed || !first.Scanned {
		t.Fatalf("primeira leitura deveria disparar scan: %+v", first)
	}
	if client.scanCount() != 1 {
		t.Fatalf("scans=%d want 1", client.scanCount())
	}

	// Sem mudanca: nenhum scan novo (a sonda continua sendo barata).
	second := svc.PollWindowsUpdateChanges(context.Background(), 0, "change")
	if second.Changed || second.Scanned {
		t.Fatalf("estado identico nao pode disparar scan: %+v", second)
	}
	if client.scanCount() != 1 {
		t.Fatalf("scans=%d want 1", client.scanCount())
	}

	// Download concluido + reboot pendente: assinatura muda -> scan imediato.
	client.setState(windowsupdate.WindowsUpdateState{
		Supported:     true,
		ServiceState:  "running",
		RebootPending: true,
		RebootReasons: []string{windowsupdate.RebootReasonFileRename},
		CheckedAt:     time.Now().UTC(),
	})
	third := svc.PollWindowsUpdateChanges(context.Background(), 0, "change")
	if !third.Changed || !third.Scanned {
		t.Fatalf("mudanca de reboot deveria disparar scan: %+v", third)
	}
	if client.scanCount() != 2 {
		t.Fatalf("scans=%d want 2", client.scanCount())
	}
}

func TestPollWindowsUpdateChangesRespectsMinScanGap(t *testing.T) {
	resetWindowsUpdatePollState()
	client := &fakeWUClient{
		state:  windowsupdate.WindowsUpdateState{Supported: true, ServiceState: "running", CheckedAt: time.Now().UTC()},
		result: sampleScanResult(),
	}
	svc := newWindowsUpdateTestService(client)

	svc.PollWindowsUpdateChanges(context.Background(), time.Hour, "change")
	if client.scanCount() != 1 {
		t.Fatalf("scans=%d want 1", client.scanCount())
	}

	// Nova mudanca dentro da janela minima: registra mas NAO varre de novo.
	client.setState(windowsupdate.WindowsUpdateState{Supported: true, ServiceState: "running", DownloadInProgress: true, CheckedAt: time.Now().UTC()})
	result := svc.PollWindowsUpdateChanges(context.Background(), time.Hour, "change")
	if !result.Changed {
		t.Fatalf("mudanca nao detectada: %+v", result)
	}
	if result.Scanned || client.scanCount() != 1 {
		t.Fatalf("scan em rajada nao pode rodar: %+v", result)
	}
}

func TestPollWindowsUpdateChangesSkipsWhenModuleDisabled(t *testing.T) {
	resetWindowsUpdatePollState()
	client := &fakeWUClient{state: windowsupdate.WindowsUpdateState{Supported: true}}
	svc := NewService(Options{})
	svc.SetWindowsUpdateDeps(WindowsUpdateDeps{Client: client, Enabled: func() bool { return false }})
	result := svc.PollWindowsUpdateChanges(context.Background(), 0, "change")
	if result.Checked || client.scanCount() != 0 {
		t.Fatalf("modulo desabilitado nao pode sondar/scanear: %+v", result)
	}
}

func TestSetWindowsUpdateBaselineAvoidsDuplicateScan(t *testing.T) {
	resetWindowsUpdatePollState()
	client := &fakeWUClient{
		state:  windowsupdate.WindowsUpdateState{Supported: true, ServiceState: "running", CheckedAt: time.Now().UTC()},
		result: sampleScanResult(),
	}
	svc := newWindowsUpdateTestService(client)

	signature, _ := svc.ProbeWindowsUpdateState(context.Background())
	_ = signature
	svc.SetWindowsUpdateBaseline(svc.WindowsUpdateStateSignature(context.Background()))
	result := svc.PollWindowsUpdateChanges(context.Background(), 0, "change")
	if result.Changed || client.scanCount() != 0 {
		t.Fatalf("scan periodico ja enviou este estado; nao pode duplicar: %+v", result)
	}
}
