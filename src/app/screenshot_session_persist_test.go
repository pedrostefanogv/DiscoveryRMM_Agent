package app

import (
	"path/filepath"
	"testing"
	"time"

	"discovery/app/logs"
)

// newScreenshotTestApp isola o arquivo da política num diretório temporário
// (o teste nunca toca no %ProgramData% real da máquina).
func newScreenshotTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	prev := screenshotPolicyPathOverride
	screenshotPolicyPathOverride = filepath.Join(dir, "screenshot_policy.json")
	t.Cleanup(func() { screenshotPolicyPathOverride = prev })

	app := &App{}
	app.Logs.Buffer = logs.New()
	return app
}

// restartApp simula um reinício do agente: instância nova lendo o mesmo arquivo.
func restartApp(t *testing.T) *App {
	t.Helper()
	app := &App{}
	app.Logs.Buffer = logs.New()
	app.initScreenshotService()
	return app
}

// A permissão "permitir sempre" precisa sobreviver a um restart do agente —
// era exatamente isso que fazia o diálogo reaparecer depois de cada reinício.
func TestScreenshotPermanentPermissionPersistsAcrossRestart(t *testing.T) {
	app := newScreenshotTestApp(t)
	if err := app.SetScreenshotSessionAllow(true); err != nil {
		t.Fatalf("SetScreenshotSessionAllow(true): %v", err)
	}
	if !app.screenshotSessionAllowed() {
		t.Fatal("permissao deveria estar ligada na instancia atual")
	}
	if !restartApp(t).screenshotSessionAllowed() {
		t.Fatal("permissao deveria sobreviver ao restart do agente")
	}

	// Desmarcar revoga e persiste a revogação.
	if err := app.SetScreenshotSessionAllow(false); err != nil {
		t.Fatalf("SetScreenshotSessionAllow(false): %v", err)
	}
	if restartApp(t).screenshotSessionAllowed() {
		t.Fatal("revogacao deveria persistir")
	}
}

// Desligar os pedidos da IA revoga a permissão (e persiste); e não dá para
// liberar a permissão com os pedidos desligados.
func TestScreenshotAiCaptureOffRevokesPermanentPermission(t *testing.T) {
	app := newScreenshotTestApp(t)
	if err := app.SetScreenshotSessionAllow(true); err != nil {
		t.Fatalf("SetScreenshotSessionAllow(true): %v", err)
	}
	if err := app.SetScreenshotAiCaptureEnabled(false); err != nil {
		t.Fatalf("SetScreenshotAiCaptureEnabled(false): %v", err)
	}
	if restartApp(t).screenshotSessionAllowed() {
		t.Fatal("desligar os pedidos da IA deveria revogar a permissao permanente")
	}
	if err := app.SetScreenshotSessionAllow(true); err == nil {
		t.Fatal("liberar a permissao com os pedidos da IA desligados deveria falhar")
	}
}

// Salvar a política (payload do formulário, sem os campos de autorização) não
// pode revogar a permissão por omissão.
func TestScreenshotSavePolicyKeepsPermanentPermission(t *testing.T) {
	app := newScreenshotTestApp(t)
	if err := app.SetScreenshotSessionAllow(true); err != nil {
		t.Fatalf("SetScreenshotSessionAllow(true): %v", err)
	}
	payload := `{"blockedProcesses":["keepass"],"allowFullScreen":true,"requireWindow":false,"maxCapturesPerWindow":10,"windowMinutes":1,"hideAgentWindow":true,"imageFormat":"auto"}`
	if err := app.SaveScreenshotPolicy(payload); err != nil {
		t.Fatalf("SaveScreenshotPolicy: %v", err)
	}
	if !restartApp(t).screenshotSessionAllowed() {
		t.Fatal("salvar a politica nao pode revogar a permissao permanente")
	}
}

// "Voltar ao padrão" revoga a permissão e grava os defaults.
func TestScreenshotResetRevokesPermanentPermission(t *testing.T) {
	app := newScreenshotTestApp(t)
	if err := app.SetScreenshotSessionAllow(true); err != nil {
		t.Fatalf("SetScreenshotSessionAllow(true): %v", err)
	}
	if _, err := app.ResetScreenshotPolicy(); err != nil {
		t.Fatalf("ResetScreenshotPolicy: %v", err)
	}
	restarted := restartApp(t)
	if restarted.screenshotSessionAllowed() {
		t.Fatal("voltar ao padrao deveria revogar a permissao permanente")
	}
	p := restarted.currentScreenshotPolicy()
	if p.LimitMax() != 10 || p.LimitWindow() != time.Minute || !p.AiCaptureAllowed() ||
		!p.FullScreenAllowed() || p.WindowRequired() || !p.HideWindowOnCapture() {
		t.Fatalf("defaults apos reset = %+v", p)
	}
}
