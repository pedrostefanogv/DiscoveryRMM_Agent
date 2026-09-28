package automation

import (
	"context"
	"errors"
	"testing"
)

type stubPackageManager struct {
	installedOut  string
	installedErr  error
	upgradableOut string
	upgradableErr error

	installCalled int
	upgradeCalled int
}

func (m *stubPackageManager) Install(context.Context, string) (string, error) {
	m.installCalled++
	return "", nil
}
func (m *stubPackageManager) Uninstall(context.Context, string) (string, error) { return "", nil }
func (m *stubPackageManager) Upgrade(context.Context, string) (string, error) {
	m.upgradeCalled++
	return "", nil
}
func (m *stubPackageManager) UpgradeAll(context.Context) (string, error) { return "", nil }
func (m *stubPackageManager) ListInstalled(context.Context) (string, error) {
	return m.installedOut, m.installedErr
}
func (m *stubPackageManager) ListUpgradable(context.Context) (string, error) {
	return m.upgradableOut, m.upgradableErr
}

const stubWingetInstalled = "Google Chrome  Google.Chrome.EXE  153.0\nFoxit PDF Reader  Foxit.FoxitReader  2026.2\n"
const stubWingetUpgradable = "Google Chrome  Google.Chrome.EXE  153.0  154.0\n"

func resetDecisionHooks(t *testing.T) {
	t.Helper()
	SetInstalledPackageChecker(nil)
	SetWingetDecisionLogger(nil)
	t.Cleanup(func() {
		SetInstalledPackageChecker(nil)
		SetWingetDecisionLogger(nil)
	})
}

func TestDecideWingetAction_InstallInstalledSkipsWithoutDownload(t *testing.T) {
	resetDecisionHooks(t)
	mgr := &stubPackageManager{installedOut: stubWingetInstalled}

	d := decideWingetAction(context.Background(), mgr, "install", "Google.Chrome.EXE")
	if !d.Skip || !d.Benign {
		t.Fatalf("esperava skip benigno para pacote instalado, got %+v", d)
	}
	if mgr.installCalled != 0 {
		t.Fatalf("install nao deveria ter sido chamado")
	}
}

// winget sai com erro mas imprime a tabela: o output deve ser usado.
func TestDecideWingetAction_InstallUsesOutputEvenOnError(t *testing.T) {
	resetDecisionHooks(t)
	mgr := &stubPackageManager{installedOut: stubWingetInstalled, installedErr: errors.New("0x8a150042")}

	d := decideWingetAction(context.Background(), mgr, "install", "Google.Chrome.EXE")
	if !d.Skip || !d.Benign {
		t.Fatalf("esperava skip benigno usando output parcial, got %+v", d)
	}
}

func TestDecideWingetAction_InstallFallsBackToInventoryCache(t *testing.T) {
	resetDecisionHooks(t)
	SetInstalledPackageChecker(func(string) (bool, bool) { return true, true })
	mgr := &stubPackageManager{installedErr: errors.New("context canceled")}

	d := decideWingetAction(context.Background(), mgr, "install", "Google.Chrome.EXE")
	if !d.Skip || !d.Benign {
		t.Fatalf("esperava skip benigno via cache do inventario, got %+v", d)
	}
	if mgr.installCalled != 0 {
		t.Fatalf("install nao deveria ter sido chamado")
	}
}

func TestDecideWingetAction_InstallFailSafeWhenUnknown(t *testing.T) {
	resetDecisionHooks(t)
	SetInstalledPackageChecker(func(string) (bool, bool) { return false, false })
	mgr := &stubPackageManager{installedErr: errors.New("context canceled")}

	d := decideWingetAction(context.Background(), mgr, "install", "Google.Chrome.EXE")
	if d.Skip {
		t.Fatalf("sem evidencia de instalacao deve manter o fail-safe (nao skip), got %+v", d)
	}
}

// Erro no "winget upgrade" antes retornava direto e o router baixava instalador
// para um update incerto. Agora valida o estado instalado.
func TestDecideWingetAction_UpgradeErrorUsesInstalledState(t *testing.T) {
	resetDecisionHooks(t)
	mgr := &stubPackageManager{
		installedOut:  stubWingetInstalled,
		upgradableErr: errors.New("context deadline exceeded"),
	}

	d := decideWingetAction(context.Background(), mgr, "upgrade", "Google.Chrome.EXE")
	if !d.Skip || !d.Benign {
		t.Fatalf("esperava skip benigno (instalado sem update conhecido), got %+v", d)
	}
}

func TestDecideWingetAction_UpgradeUsesOutputEvenOnError(t *testing.T) {
	resetDecisionHooks(t)
	mgr := &stubPackageManager{upgradableOut: stubWingetUpgradable, upgradableErr: errors.New("exit status")}

	d := decideWingetAction(context.Background(), mgr, "upgrade", "Google.Chrome.EXE")
	if d.Skip {
		t.Fatalf("update pendente no output nao pode ser ignorado, got %+v", d)
	}
	if d.AvailableVersion != "154.0" {
		t.Fatalf("esperava versao disponivel 154.0, got %q", d.AvailableVersion)
	}
}

func TestShouldPreloadPackage_InstalledNotPreloadedEvenWhenWingetFails(t *testing.T) {
	resetDecisionHooks(t)
	SetInstalledPackageChecker(func(string) (bool, bool) { return true, true })
	mgr := &stubPackageManager{installedErr: errors.New("context canceled")}

	if ShouldPreloadPackage(context.Background(), mgr, ActionInstallPackage, "Google.Chrome.EXE") {
		t.Fatalf("pacote instalado nao deve ser pre-carregado (baixaria instalador)")
	}
}

func TestShouldPreloadPackage_NotInstalledPreloads(t *testing.T) {
	resetDecisionHooks(t)
	mgr := &stubPackageManager{installedOut: "outro pacote  Outro.Pacote  1.0\n"}

	if !ShouldPreloadPackage(context.Background(), mgr, ActionInstallPackage, "Brave.Brave") {
		t.Fatalf("pacote ausente deve ser pre-carregado")
	}
}

// JSON do winget (--output json) também precisa alimentar a decisão.
func TestDecideWingetAction_InstallJSONOutputSkips(t *testing.T) {
	resetDecisionHooks(t)
	mgr := &stubPackageManager{installedOut: `[{"Id":"Google.Chrome.EXE","Version":"153.0"}]`}

	d := decideWingetAction(context.Background(), mgr, "install", "Google.Chrome.EXE")
	if !d.Skip || !d.Benign {
		t.Fatalf("esperava skip benigno via JSON, got %+v", d)
	}
	if d.DecidedBy != decidedByWinget {
		t.Fatalf("decidedBy=%q, want %q", d.DecidedBy, decidedByWinget)
	}
	if d.InstalledVersion != "153.0" {
		t.Fatalf("installedVersion=%q, want 153.0", d.InstalledVersion)
	}
}

func TestDecideWingetAction_CacheDecisionSource(t *testing.T) {
	resetDecisionHooks(t)
	SetInstalledPackageChecker(func(string) (bool, bool) { return true, true })
	mgr := &stubPackageManager{installedErr: errors.New("context canceled")}

	d := decideWingetAction(context.Background(), mgr, "install", "Google.Chrome.EXE")
	if d.DecidedBy != decidedByCache {
		t.Fatalf("decidedBy=%q, want %q", d.DecidedBy, decidedByCache)
	}
}

// richStubPackageManager expõe a capacidade opcional (--output json).
type richStubPackageManager struct {
	stubPackageManager
	richInstalledOut  string
	richUpgradableOut string
	richCalls         int
}

func (m *richStubPackageManager) ListInstalledRich(context.Context) (string, error) {
	m.richCalls++
	return m.richInstalledOut, nil
}

func (m *richStubPackageManager) ListUpgradableRich(context.Context) (string, error) {
	return m.richUpgradableOut, nil
}

func TestDecideWingetAction_PrefersRichLister(t *testing.T) {
	resetDecisionHooks(t)
	mgr := &richStubPackageManager{}
	mgr.installedOut = "tabela sem o pacote\n"
	mgr.richInstalledOut = `[{"Id":"Google.Chrome.EXE","Version":"153.0"}]`

	d := decideWingetAction(context.Background(), mgr, "install", "Google.Chrome.EXE")
	if !d.Skip || d.DecidedBy != decidedByWinget {
		t.Fatalf("esperava skip pela lista JSON (rich), got %+v", d)
	}
	if mgr.richCalls == 0 {
		t.Fatalf("ListInstalledRich deveria ter sido usado")
	}
}

// O prompt (Welcome PSADT / toast) só deve aparecer quando há algo a executar.
func TestShouldSkipPackageActionBeforePrompt(t *testing.T) {
	resetDecisionHooks(t)
	cases := []struct {
		name   string
		mgr    *stubPackageManager
		action AutomationTaskActionType
		want   bool
	}{
		{
			name:   "install ja instalado: pula prompt",
			mgr:    &stubPackageManager{installedOut: "Brave  brave.brave  1.66\n"},
			action: ActionInstallPackage, want: true,
		},
		{
			name:   "install ausente: pede confirmacao",
			mgr:    &stubPackageManager{installedOut: "Outro  Outro.Pacote  1.0\n"},
			action: ActionInstallPackage, want: false,
		},
		{
			name:   "update sem pendencia: pula prompt",
			mgr:    &stubPackageManager{installedOut: "Brave  brave.brave  1.66\n", upgradableOut: "Outro  Outro.Pacote  1.0  2.0\n"},
			action: ActionUpdatePackage, want: true,
		},
		{
			name:   "update com pendencia: pede confirmacao",
			mgr:    &stubPackageManager{installedOut: "Brave  brave.brave  1.66\n", upgradableOut: "Brave  brave.brave  1.66  1.67\n"},
			action: ActionUpdatePackage, want: false,
		},
		{
			name:   "update-or-install instalado e atualizado: pula prompt",
			mgr:    &stubPackageManager{installedOut: "Brave  brave.brave  1.66\n", upgradableOut: "Outro  Outro.Pacote  1.0  2.0\n"},
			action: ActionUpdateOrInstallPackage, want: true,
		},
		{
			name:   "update-or-install com update pendente: pede confirmacao",
			mgr:    &stubPackageManager{installedOut: "Brave  brave.brave  1.66\n", upgradableOut: "Brave  brave.brave  1.66  1.67\n"},
			action: ActionUpdateOrInstallPackage, want: false,
		},
		{
			name:   "update-or-install ausente: pede confirmacao",
			mgr:    &stubPackageManager{installedOut: "Outro  Outro.Pacote  1.0\n"},
			action: ActionUpdateOrInstallPackage, want: false,
		},
		{
			name:   "remove package nunca pula prompt",
			mgr:    &stubPackageManager{installedOut: "Brave  brave.brave  1.66\n"},
			action: ActionRemovePackage, want: false,
		},
		{
			name:   "run script nunca pula prompt",
			mgr:    &stubPackageManager{installedOut: "Brave  brave.brave  1.66\n"},
			action: ActionRunScript, want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := AutomationTask{TaskID: "t-1", PackageID: "brave.brave", ActionType: tc.action}
			got := ShouldSkipPackageActionBeforePrompt(context.Background(), tc.mgr, task)
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldSkipPackageActionBeforePrompt_NoPackageOrManager(t *testing.T) {
	resetDecisionHooks(t)
	mgr := &stubPackageManager{installedOut: "Brave  brave.brave  1.66\n"}
	if ShouldSkipPackageActionBeforePrompt(context.Background(), mgr, AutomationTask{ActionType: ActionInstallPackage}) {
		t.Fatal("sem packageId não há decisão")
	}
	if ShouldSkipPackageActionBeforePrompt(context.Background(), nil, AutomationTask{PackageID: "brave.brave", ActionType: ActionInstallPackage}) {
		t.Fatal("sem package manager não há decisão")
	}
}
