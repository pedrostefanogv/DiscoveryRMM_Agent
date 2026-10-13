package automation

import (
	"context"
	"strings"
	"testing"
)

// countingPackageManager conta quantas vezes as listagens foram realmente
// executadas (spawn do winget), para provar o efeito do cache.
type countingPackageManager struct {
	stubPackageManager
	installedCalls  int
	upgradableCalls int
}

func (m *countingPackageManager) ListInstalled(context.Context) (string, error) {
	m.installedCalls++
	return m.installedOut, m.installedErr
}

func (m *countingPackageManager) ListUpgradable(context.Context) (string, error) {
	m.upgradableCalls++
	return m.upgradableOut, m.upgradableErr
}

// ── item 3: cache de listagens do decisor ──────────────────────────────────

func TestDecisionListCache_ReusesListingWithinTTL(t *testing.T) {
	resetDecisionHooks(t)
	inner := &countingPackageManager{}
	inner.installedOut = stubWingetInstalled
	cache := newDecisionListCache(inner)

	for i := 0; i < 3; i++ {
		if _, err := cache.ListInstalled(context.Background()); err != nil {
			t.Fatalf("listagem %d falhou: %v", i, err)
		}
	}
	if inner.installedCalls != 1 {
		t.Fatalf("esperava 1 spawn de winget list para 3 consultas, got %d", inner.installedCalls)
	}
}

func TestDecisionListCache_InstallInvalidates(t *testing.T) {
	resetDecisionHooks(t)
	inner := &countingPackageManager{}
	inner.installedOut = stubWingetInstalled
	cache := newDecisionListCache(inner)

	if _, err := cache.ListInstalled(context.Background()); err != nil {
		t.Fatalf("listagem falhou: %v", err)
	}
	if _, err := cache.ListInstalled(context.Background()); err != nil {
		t.Fatalf("listagem falhou: %v", err)
	}
	if inner.installedCalls != 1 {
		t.Fatalf("cache nao esta ativo: calls=%d", inner.installedCalls)
	}

	if _, err := cache.Install(context.Background(), "Google.Chrome.EXE"); err != nil {
		t.Fatalf("install falhou: %v", err)
	}
	if _, err := cache.ListInstalled(context.Background()); err != nil {
		t.Fatalf("listagem falhou: %v", err)
	}
	if inner.installedCalls != 2 {
		t.Fatalf("install deveria invalidar o cache; calls=%d", inner.installedCalls)
	}
}

func TestDecisionListCache_DelegatesRichLister(t *testing.T) {
	resetDecisionHooks(t)
	inner := &richStubPackageManager{}
	inner.richInstalledOut = `[{"Id":"Google.Chrome.EXE","Version":"153.0"}]`
	cache := newDecisionListCache(inner)

	d := decideWingetAction(context.Background(), cache, "install", "Google.Chrome.EXE")
	if !d.Skip {
		t.Fatalf("esperava skip pela lista JSON atraves do cache, got %+v", d)
	}
	if inner.richCalls == 0 {
		t.Fatalf("ListInstalledRich deveria ser usado pelo cache")
	}
}

func TestDecisionListCache_NilManagerStaysNil(t *testing.T) {
	if newDecisionListCache(nil) != nil {
		t.Fatalf("gerenciador nil deve continuar nil: typed-nil quebraria os decisores (packages != nil)")
	}
}

// ── item 1: pré-check de estado no gatilho ─────────────────────────────────

func TestShouldSkipPackageActionBeforeExecution(t *testing.T) {
	resetDecisionHooks(t)
	const semPendencia = "Outro.App  Outro.App  1.0\n"
	cases := []struct {
		name       string
		action     AutomationTaskActionType
		packageID  string
		installed  string
		upgradable string
		wantSkip   bool
	}{
		{"instalar ja instalado", ActionInstallPackage, "Google.Chrome.EXE", stubWingetInstalled, "", true},
		{"instalar ausente", ActionInstallPackage, "Ausente.App", stubWingetInstalled, "", false},
		{"update ja atualizado", ActionUpdatePackage, "Google.Chrome.EXE", stubWingetInstalled, semPendencia, true},
		{"update com pendencia", ActionUpdatePackage, "Google.Chrome.EXE", stubWingetInstalled, stubWingetUpgradable, false},
		{"updateorinstall instalado e atualizado", ActionUpdateOrInstallPackage, "Google.Chrome.EXE", stubWingetInstalled, semPendencia, true},
		{"updateorinstall ausente", ActionUpdateOrInstallPackage, "Ausente.App", stubWingetInstalled, semPendencia, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &stubPackageManager{installedOut: tc.installed, upgradableOut: tc.upgradable}
			task := AutomationTask{TaskID: "task-" + tc.name, ActionType: tc.action, PackageID: tc.packageID}
			dec := ShouldSkipPackageActionBeforeExecution(context.Background(), mgr, task)
			if dec.Skip != tc.wantSkip {
				t.Fatalf("Skip=%v, esperado %v (motivo=%q)", dec.Skip, tc.wantSkip, dec.Reason)
			}
			if tc.wantSkip && strings.TrimSpace(dec.Reason) == "" {
				t.Fatalf("skip deveria trazer motivo para log/relatorio")
			}
			if tc.wantSkip && mgr.installCalled+mgr.upgradeCalled != 0 {
				t.Fatalf("nenhuma operacao deveria ter sido executada no pre-check")
			}
		})
	}
}

func TestServiceShouldSkipTriggeredPackageTask(t *testing.T) {
	resetDecisionHooks(t)
	svc := &Service{}
	mgr := &stubPackageManager{installedOut: stubWingetInstalled}
	svc.SetPackageManager(mgr)

	dec, skip := svc.shouldSkipTriggeredPackageTask(context.Background(), AutomationTask{
		TaskID:     "task-anydesk",
		ActionType: ActionInstallPackage,
		PackageID:  "Google.Chrome.EXE",
	})
	if !skip {
		t.Fatalf("pacote instalado deveria ser dispensado no gatilho")
	}
	if strings.TrimSpace(dec.Reason) == "" {
		t.Fatalf("motivo do skip deveria estar preenchido")
	}
	if mgr.installCalled != 0 {
		t.Fatalf("install nao deveria rodar no gatilho")
	}

	// Script nunca e dispensado no gatilho: quem decide e o proprio script.
	if _, skip := svc.shouldSkipTriggeredPackageTask(context.Background(), AutomationTask{
		TaskID:     "task-script",
		ActionType: ActionRunScript,
	}); skip {
		t.Fatalf("RunScript nao deve ser dispensado no gatilho")
	}
}

// ── item 2 (scripts): sinal MDZ_SKIP ───────────────────────────────────────

func TestParseSkipSignal(t *testing.T) {
	out := "TeamViewer ID coletado: 123\nMDZ_SKIP: teamviewer ja configurado\nfim\n"
	reason, cleaned := parseSkipSignal(out)
	if reason != "teamviewer ja configurado" {
		t.Fatalf("motivo inesperado: %q", reason)
	}
	if strings.Contains(cleaned, "MDZ_SKIP") {
		t.Fatalf("linha do sinal nao deveria sobrar no output limpo: %q", cleaned)
	}
	if !strings.Contains(cleaned, "fim") {
		t.Fatalf("resto do output deveria ser preservado: %q", cleaned)
	}

	if r, c := parseSkipSignal("apenas saida\n"); r != "" || c != "apenas saida\n" {
		t.Fatalf("sem sinal o output deve voltar intacto: reason=%q cleaned=%q", r, c)
	}

	if r, _ := parseSkipSignal("MDZ_SKIP:\n"); r == "" {
		t.Fatalf("sinal sem motivo textual ainda e um skip declarado")
	}
}
