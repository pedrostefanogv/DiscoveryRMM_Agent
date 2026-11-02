package autoupdate

import (
	"context"
	"strings"
	"testing"
	"time"

	"discovery/app/appstore"
	"discovery/app/core/maintenance"
	"discovery/app/core/models"
)

func saoPauloWindow(days []int, startLocal string, durationMinutes int) maintenance.Window {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		panic(err)
	}
	return maintenance.Window{
		Enabled:         true,
		Days:            days,
		StartUTC:        "05:00",
		StartLocal:      startLocal,
		Location:        loc,
		DurationMinutes: durationMinutes,
	}
}

func saoPauloTime(t *testing.T, clock string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	when, err := time.ParseInLocation("2006-01-02 15:04", "2026-06-15 "+clock, loc)
	if err != nil {
		t.Fatalf("ParseInLocation: %v", err)
	}
	return when
}

// ── Planner ─────────────────────────────────────────────────────────────────

func TestPlanMatchWingetComTargetSourceID(t *testing.T) {
	policy := appstore.EffectivePolicy{Items: []appstore.Item{
		{InstallationType: "Winget", PackageID: "7Zip.7zip", AutoUpdateEnabled: true},
	}}
	pending := []models.UpgradeItem{
		{ID: "7zip.7zip", Source: "winget", AvailableVersion: "26.00"},
	}
	plan := Plan(policy, pending, nil, time.Now())
	if len(plan.Entries) != 1 {
		t.Fatalf("esperava 1 entrada, veio %d (skips=%+v)", len(plan.Entries), plan.Skips)
	}
	entry := plan.Entries[0]
	if entry.Target != "winget::7zip.7zip" {
		t.Fatalf("target inesperado: %q", entry.Target)
	}
	if entry.PackageID != "7Zip.7zip" || entry.InstallationType != "Winget" {
		t.Fatalf("identificação da política perdida: %+v", entry)
	}
	if entry.AvailableVersion != "26.00" {
		t.Fatalf("versão disponível perdida: %+v", entry)
	}
}

func TestPlanCaseInsensitiveEChocolatey(t *testing.T) {
	policy := appstore.EffectivePolicy{Items: []appstore.Item{
		{InstallationType: "Chocolatey", PackageID: "VLC", AutoUpdateEnabled: true},
		// Mesmo packageId, mas sem flag: NÃO deve entrar no plano.
		{InstallationType: "Winget", PackageID: "notepad++", AutoUpdateEnabled: false},
	}}
	pending := []models.UpgradeItem{
		// source "choco" normaliza para "chocolatey" (match com o item da política).
		{ID: "vlc", Source: "choco", AvailableVersion: "3.0.21"},
		{ID: "notepad++", Source: "winget", AvailableVersion: "9.0"},
	}
	plan := Plan(policy, pending, nil, time.Now())
	if len(plan.Entries) != 1 {
		t.Fatalf("esperava 1 entrada, veio %d (skips=%+v)", len(plan.Entries), plan.Skips)
	}
	if plan.Entries[0].Target != "chocolatey::vlc" {
		t.Fatalf("target inesperado: %q", plan.Entries[0].Target)
	}
	if plan.Entries[0].Source != "chocolatey" {
		t.Fatalf("source normalizado inesperado: %q", plan.Entries[0].Source)
	}
	// "notepad++" pendente sem item aprovado com flag: não pode ter virado entry
	// nem skip denied (o item EXISTE na política, só está sem flag).
	for _, skip := range plan.Skips {
		if skip.PackageID == "notepad++" {
			t.Fatalf("item sem flag não deveria gerar skip: %+v", skip)
		}
	}
}

func TestPlanSkipsComMotivo(t *testing.T) {
	now := time.Now()
	policy := appstore.EffectivePolicy{Items: []appstore.Item{
		{InstallationType: "Winget", PackageID: "ok.pacote", AutoUpdateEnabled: true},
		{InstallationType: "Winget", PackageID: "sem.update", AutoUpdateEnabled: true},
		{InstallationType: "Winget", PackageID: "auto.pacote", AutoUpdateEnabled: true},
		{InstallationType: "Winget", PackageID: "fora.pacote", AutoUpdateEnabled: true},
	}}
	pending := []models.UpgradeItem{
		{ID: "ok.pacote", Source: "winget"},
		{ID: "auto.pacote", Source: "winget"},
		{ID: "fora.pacote", Source: "winget"},
		// Órfão: não corresponde a nenhum item da política.
		{ID: "orc.pendente", Source: "winget"},
	}
	isAutomated := func(installationType, packageID string) bool {
		return packageID == "auto.pacote"
	}
	plan := Plan(policy, pending, isAutomated, now)

	// Janela desligada no Plan puro: ok.pacote E fora.pacote entram.
	if len(plan.Entries) != 2 || plan.Entries[0].Target != "winget::ok.pacote" || plan.Entries[1].Target != "winget::fora.pacote" {
		t.Fatalf("entradas inesperadas: %+v", plan.Entries)
	}
	reasons := map[string]string{}
	for _, skip := range plan.Skips {
		reasons[skip.PackageID] = skip.Reason
	}
	if reasons["sem.update"] != SkipReasonNoUpdate {
		t.Fatalf("sem.update: esperava %s, veio %q", SkipReasonNoUpdate, reasons["sem.update"])
	}
	if reasons["auto.pacote"] != SkipReasonAutomatedByTask {
		t.Fatalf("auto.pacote: esperava %s, veio %q", SkipReasonAutomatedByTask, reasons["auto.pacote"])
	}
	if reasons["orc.pendente"] != SkipReasonDenied {
		t.Fatalf("orc.pendente: esperava %s, veio %q", SkipReasonDenied, reasons["orc.pendente"])
	}
}

func TestPlanOutsideWindowComJanelaAtiva(t *testing.T) {
	// Janela 02:00–03:00 America/Sao_Paulo; now = 12:30 SP → fora.
	win := saoPauloWindow(nil, "02:00", 60)
	now := saoPauloTime(t, "12:30")
	policy := appstore.EffectivePolicy{Items: []appstore.Item{
		{InstallationType: "Winget", PackageID: "a.pacote", AutoUpdateEnabled: true},
		{InstallationType: "Chocolatey", PackageID: "b.pacote", AutoUpdateEnabled: true},
	}}
	pending := []models.UpgradeItem{
		{ID: "a.pacote", Source: "winget"},
		{ID: "b.pacote", Source: "chocolatey"},
	}
	plan := planWithLimit(policy, pending, nil, now, win, MaxPerCycle)
	if !plan.WindowBlocked {
		t.Fatal("esperava WindowBlocked=true")
	}
	if len(plan.Entries) != 0 {
		t.Fatalf("nenhuma entrada esperada fora da janela: %+v", plan.Entries)
	}
	for _, skip := range plan.Skips {
		if skip.Reason != SkipReasonOutsideWindow {
			t.Fatalf("motivo inesperado para %s: %q", skip.PackageID, skip.Reason)
		}
	}
}

func TestPlanMaxPerCycle(t *testing.T) {
	now := time.Now()
	var policyItems []appstore.Item
	var pending []models.UpgradeItem
	for i := 0; i < 5; i++ {
		id := "pacote." + strings.Repeat("x", i+1)
		policyItems = append(policyItems, appstore.Item{InstallationType: "Winget", PackageID: id, AutoUpdateEnabled: true})
		pending = append(pending, models.UpgradeItem{ID: id, Source: "winget"})
	}
	plan := Plan(appstore.EffectivePolicy{Items: policyItems}, pending, nil, now)
	if len(plan.Entries) != MaxPerCycle {
		t.Fatalf("esperava %d entradas, veio %d", MaxPerCycle, len(plan.Entries))
	}
	// Sem skip sintético para itens acima do limite (reavaliados no próximo
	// ciclo) e nenhum órfão (todos os pendentes batem com a política).
	if len(plan.Skips) != 0 {
		t.Fatalf("skips inesperados: %+v", plan.Skips)
	}
}

// ── WindowGate ──────────────────────────────────────────────────────────────

// Gate de EXECUÇÃO (novo contrato): o auto-update só roda DENTRO da janela
// ativa; janela desligada/ausente (Window zero) = não executa.
func TestAutoUpdateAllowedNowContrato(t *testing.T) {
	win := saoPauloWindow(nil, "02:00", 60)
	// Fora da janela (12:30 SP) → false.
	if AutoUpdateAllowedNow(win, saoPauloTime(t, "12:30")) {
		t.Fatal("esperava false fora da janela")
	}
	// Dentro (02:30 SP) → true.
	if !AutoUpdateAllowedNow(win, saoPauloTime(t, "02:30")) {
		t.Fatal("esperava true dentro da janela")
	}
	// Janela desligada → false (auto-update desativado).
	off := win
	off.Enabled = false
	if AutoUpdateAllowedNow(off, saoPauloTime(t, "02:30")) {
		t.Fatal("esperava false com janela desligada")
	}
	// Window zero/nil (config não carregada) → false.
	if AutoUpdateAllowedNow(maintenance.Window{}, saoPauloTime(t, "02:30")) {
		t.Fatal("esperava false com Window zero (config não carregada)")
	}
}

// ShouldRunNow é helper interno do plano (skip outside_window só quando a
// janela está ativa): janela desligada → true (o GATE já bloqueou o ciclo).
func TestShouldRunNowHelperDoPlano(t *testing.T) {
	win := saoPauloWindow(nil, "02:00", 60)
	if ShouldRunNow(win, saoPauloTime(t, "12:30")) {
		t.Fatal("esperava false fora da janela")
	}
	if !ShouldRunNow(win, saoPauloTime(t, "02:30")) {
		t.Fatal("esperava true dentro da janela")
	}
	off := win
	off.Enabled = false
	if !ShouldRunNow(off, saoPauloTime(t, "12:30")) {
		t.Fatal("helper do plano: esperava true com janela desligada")
	}
}

func TestShouldRunNowJanelaCruzaMeiaNoite(t *testing.T) {
	// 23:00 por 120min cruza a meia-noite: 23:30 e 00:30 estão dentro, 10:00 fora.
	win := saoPauloWindow(nil, "23:00", 120)
	if !ShouldRunNow(win, saoPauloTime(t, "23:30")) {
		t.Fatal("esperava true às 23:30")
	}
	if !ShouldRunNow(win, saoPauloTime(t, "00:30")) {
		t.Fatal("esperava true às 00:30 (janela cruzando meia-noite)")
	}
	if ShouldRunNow(win, saoPauloTime(t, "10:00")) {
		t.Fatal("esperava false às 10:00")
	}
}

// ── RunOnce (fakes) ─────────────────────────────────────────────────────────

func TestRunOnceExecutaUpgradesComFila(t *testing.T) {
	policy := appstore.EffectivePolicy{Items: []appstore.Item{
		{InstallationType: "Winget", PackageID: "a.pacote", AutoUpdateEnabled: true},
		{InstallationType: "Chocolatey", PackageID: "b.pacote", AutoUpdateEnabled: true},
		{InstallationType: "Winget", PackageID: "c.pacote", AutoUpdateEnabled: true},
	}}
	pending := []models.UpgradeItem{
		{ID: "a.pacote", Source: "winget", AvailableVersion: "2.0"},
		{ID: "b.pacote", Source: "choco", AvailableVersion: "3.0"},
		{ID: "c.pacote", Source: "winget", AvailableVersion: "4.0"},
		{ID: "d.pacote", Source: "winget", AvailableVersion: "5.0"},
	}
	var targets []string
	deps := Deps{
		LoadPolicy:     func(context.Context) (appstore.EffectivePolicy, error) { return policy, nil },
		PendingUpdates: func() ([]models.UpgradeItem, error) { return pending, nil },
		Upgrade: func(_ context.Context, target string) (string, error) {
			targets = append(targets, target)
			return "saida " + target, nil
		},
		IsAutomated: func(installationType, packageID string) bool { return packageID == "c.pacote" },
		// Janela ativa cobrindo o dia inteiro (contrato: só executa dentro).
		Window: func() maintenance.Window { return saoPauloWindow(nil, "00:00", 1440) },
		Logf:   func(string) {},
		// -1 desativa o espaçamento no teste.
		MinSpacing:  -1,
		MaxPerCycle: MaxPerCycle,
	}
	var eventCounts []int
	deps.EmitAutoUpdated = func(count int) { eventCounts = append(eventCounts, count) }

	svc := New(deps)
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	// a e b executam; c é suprimido pela automação (precedência); d excede MaxPerCycle.
	if len(targets) != 2 {
		t.Fatalf("esperava 2 upgrades, veio %v", targets)
	}
	if targets[0] != "winget::a.pacote" || targets[1] != "chocolatey::b.pacote" {
		t.Fatalf("targets inesperados: %v", targets)
	}
	if len(eventCounts) != 1 || eventCounts[0] != 2 {
		t.Fatalf("evento store:auto-updated inesperado: %v", eventCounts)
	}
}

func TestRunOnceFalhaDePolicyNaoExecutaUpgrade(t *testing.T) {
	upgrades := 0
	deps := Deps{
		LoadPolicy: func(context.Context) (appstore.EffectivePolicy, error) {
			return appstore.EffectivePolicy{}, context.Canceled
		},
		PendingUpdates: func() ([]models.UpgradeItem, error) { return nil, nil },
		Upgrade:        func(context.Context, string) (string, error) { upgrades++; return "", nil },
		Window:         func() maintenance.Window { return saoPauloWindow(nil, "00:00", 1440) },
		Logf:           func(string) {},
		MinSpacing:     -1,
	}
	svc := New(deps)
	if err := svc.RunOnce(context.Background()); err == nil {
		t.Fatal("esperava erro de política")
	}
	if upgrades != 0 {
		t.Fatalf("nenhum upgrade deveria rodar, rodaram %d", upgrades)
	}
}

// Janela desligada/ausente (Window zero — config não carregada): o ciclo não
// varre, não executa e não falha (auto-update desativado).
func TestRunOnceJanelaDesligadaNaoVarre(t *testing.T) {
	varre := false
	var mensagens []string
	deps := Deps{
		LoadPolicy:     func(context.Context) (appstore.EffectivePolicy, error) { return appstore.EffectivePolicy{}, nil },
		PendingUpdates: func() ([]models.UpgradeItem, error) { varre = true; return nil, nil },
		Upgrade:        func(context.Context, string) (string, error) { return "", nil },
		Window:         func() maintenance.Window { return maintenance.Window{} },
		Logf:           func(line string) { mensagens = append(mensagens, line) },
		MinSpacing:     -1,
	}
	svc := New(deps)
	if err := svc.RunOnce(nil); err != nil {
		t.Fatalf("janela desligada não deveria falhar (e nil ctx não pode panica): %v", err)
	}
	if varre {
		t.Fatal("janela desligada o ciclo não deveria varrer updates")
	}
	achou := false
	for _, m := range mensagens {
		if strings.Contains(m, "janela de manutenção desligada — auto-update desativado") {
			achou = true
		}
	}
	if !achou {
		t.Fatalf("esperava log 'janela desligada', veio: %v", mensagens)
	}
}

func TestRunOnceForaDaJanelaNaoVarre(t *testing.T) {
	win := saoPauloWindow(nil, "02:00", 60)
	now := saoPauloTime(t, "12:30")
	varre := false
	deps := Deps{
		LoadPolicy:     func(context.Context) (appstore.EffectivePolicy, error) { return appstore.EffectivePolicy{}, nil },
		PendingUpdates: func() ([]models.UpgradeItem, error) { varre = true; return nil, nil },
		Upgrade:        func(context.Context, string) (string, error) { return "", nil },
		Window:         func() maintenance.Window { return win },
		Logf:           func(string) {},
		Now:            func() time.Time { return now },
		MinSpacing:     -1,
	}
	svc := New(deps)
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if varre {
		t.Fatal("fora da janela o ciclo não deveria varrer updates")
	}
}
