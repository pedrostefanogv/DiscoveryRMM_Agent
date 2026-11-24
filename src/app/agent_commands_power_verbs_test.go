package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ── Testes dos verbos diretos do comando genérico "poweraction" ──
//
// Cobrem os verbos que NÃO passam pelo contador/adiamento do PSADT:
//   - restart-advanced → shutdown.exe /r /o (Opções Avançadas / WinRE);
//   - cancel           → cancela timers do agent + shutdown.exe /a;
//   - lock             → bloqueio de sessão (LockWorkStation).
//
// Os seams (powerExecHook/powerCancelHook/powerLockFunc) substituem o SO: nada
// é executado de verdade.

func TestResolvePowerShutdownInvocationAdvancedBoot(t *testing.T) {
	inv, ok := resolvePowerShutdownInvocation(powerActionRestartAdvanced, 0, true, "")
	if !ok {
		t.Fatal("restart-advanced deve ser um verbo suportado pelo shutdown.exe")
	}
	if inv.Label != "restart-advanced" {
		t.Fatalf("label=%q, want restart-advanced", inv.Label)
	}
	want := "/r /o /t 0 /f"
	if got := strings.Join(inv.Args, " "); got != want {
		t.Fatalf("args=%q, want %q", got, want)
	}
}

func TestResolvePowerShutdownInvocationFirmwareKeepsFwDirective(t *testing.T) {
	inv, ok := resolvePowerShutdownInvocation(powerActionRestartFirmware, 30, false, "aviso")
	if !ok {
		t.Fatal("restart-firmware deve ser um verbo suportado")
	}
	want := "/r /fw /t 30 /c aviso"
	if got := strings.Join(inv.Args, " "); got != want {
		t.Fatalf("args=%q, want %q", got, want)
	}
}

func TestResolvePowerShutdownInvocationRejectsUnknownAction(t *testing.T) {
	// Fail-safe: um verbo desconhecido NUNCA pode virar desligamento (/s).
	if _, ok := resolvePowerShutdownInvocation("hibernate", 0, false, ""); ok {
		t.Fatal("verbo desconhecido nao pode ser aceito como desligamento/reinicio")
	}
}

func TestRestartAdvancedExecutesAdvancedBootVerb(t *testing.T) {
	restorePowerHooks(t)
	a := powerApp(t, "")

	execAction := ""
	powerExecHook = func(_ *App, _ context.Context, action string, _ int, _ bool, _ string) (int, string, string) {
		execAction = action
		return 0, "", ""
	}

	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(
		context.Background(), "poweraction", firmwarePayload(powerActionRestartAdvanced))
	if !handled || exitCode != 0 || errText != "" {
		t.Fatalf("handled=%t exit=%d err=%q", handled, exitCode, errText)
	}
	if execAction != powerActionRestartAdvanced {
		t.Fatalf("action=%q, want %q", execAction, powerActionRestartAdvanced)
	}
	m := parsePowerContract(t, output)
	if m["status"] != "executed" {
		t.Fatalf("status=%v, want executed (output=%s)", m["status"], output)
	}
	if m["action"] != powerActionRestartAdvanced {
		t.Fatalf("action=%v, want %s", m["action"], powerActionRestartAdvanced)
	}
}

func TestRestartAdvancedWorksOnLegacyBios(t *testing.T) {
	// Opções Avançadas (/o) NÃO exigem UEFI: em BIOS legado o Windows continua
	// oferecendo o menu do WinRE. Só o /fw é que depende de UEFI.
	restorePowerHooks(t)
	withFirmwareSupport(t, false, "computador em BIOS legado (Legacy/CSM)")
	a := powerApp(t, "")

	execCalls := 0
	powerExecHook = func(*App, context.Context, string, int, bool, string) (int, string, string) {
		execCalls++
		return 0, "", ""
	}

	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(
		context.Background(), "poweraction", firmwarePayload(powerActionRestartAdvanced))
	if !handled || exitCode != 0 || errText != "" {
		t.Fatalf("handled=%t exit=%d err=%q", handled, exitCode, errText)
	}
	if execCalls != 1 {
		t.Fatalf("exec=%d, want 1 (Legacy nao pode bloquear /o)", execCalls)
	}
	if m := parsePowerContract(t, output); m["status"] != "executed" {
		t.Fatalf("status=%v, want executed", m["status"])
	}
}

func TestPowerActionCancelCancelsWithoutExecutingPower(t *testing.T) {
	restorePowerHooks(t)
	a := powerApp(t, "")

	cancelled := 0
	powerCancelHook = func(_ *App, _ context.Context) (string, string) {
		cancelled++
		return "", ""
	}
	powerExecHook = func(*App, context.Context, string, int, bool, string) (int, string, string) {
		t.Fatal("cancel nao pode executar energia")
		return 0, "", ""
	}

	payload := map[string]any{
		"action": powerActionCancel, "delaySeconds": 60,
		"force": false, "notifyUser": false, "message": "",
	}
	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(context.Background(), "poweraction", payload)
	if !handled || exitCode != 0 || errText != "" {
		t.Fatalf("handled=%t exit=%d err=%q", handled, exitCode, errText)
	}
	if cancelled != 1 {
		t.Fatalf("cancel chamado %d vezes, want 1 (delay deve ser ignorado)", cancelled)
	}
	m := parsePowerContract(t, output)
	if m["status"] != "executed" {
		t.Fatalf("status=%v, want executed (output=%s)", m["status"], output)
	}
	if m["action"] != powerActionCancel {
		t.Fatalf("action=%v, want %s", m["action"], powerActionCancel)
	}
}

func TestPowerActionCancelSurfacesFailure(t *testing.T) {
	restorePowerHooks(t)
	a := powerApp(t, "")

	powerCancelHook = func(_ *App, _ context.Context) (string, string) {
		return "", "falha ao executar shutdown.exe /a: acesso negado"
	}

	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(
		context.Background(), "poweraction", firmwarePayload(powerActionCancel))
	if !handled || exitCode == 0 || errText == "" {
		t.Fatalf("handled=%t exit=%d err=%q, want falha tipada", handled, exitCode, errText)
	}
	if m := parsePowerContract(t, output); m["status"] != "failed" {
		t.Fatalf("status=%v, want failed", m["status"])
	}
}

func TestPowerActionLockLocksSessionWithoutPowerCommand(t *testing.T) {
	restorePowerHooks(t)
	a := powerApp(t, "")

	locked := 0
	powerLockFunc = func(context.Context) error {
		locked++
		return nil
	}
	powerExecHook = func(*App, context.Context, string, int, bool, string) (int, string, string) {
		t.Fatal("lock nao pode executar shutdown.exe")
		return 0, "", ""
	}

	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(
		context.Background(), "poweraction", firmwarePayload(powerActionLock))
	if !handled || exitCode != 0 || errText != "" {
		t.Fatalf("handled=%t exit=%d err=%q", handled, exitCode, errText)
	}
	if locked != 1 {
		t.Fatalf("lock chamado %d vezes, want 1", locked)
	}
	m := parsePowerContract(t, output)
	if m["status"] != "executed" {
		t.Fatalf("status=%v, want executed (output=%s)", m["status"], output)
	}
	if m["action"] != powerActionLock {
		t.Fatalf("action=%v, want %s", m["action"], powerActionLock)
	}
}

func TestPowerActionLockFailureIsTypedFailure(t *testing.T) {
	restorePowerHooks(t)
	a := powerApp(t, "")

	powerLockFunc = func(context.Context) error {
		return errors.New("falha ao bloquear a sessao: sessao interativa ausente")
	}

	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(
		context.Background(), "poweraction", firmwarePayload(powerActionLock))
	if !handled || exitCode == 0 || errText == "" {
		t.Fatalf("handled=%t exit=%d err=%q, want falha tipada", handled, exitCode, errText)
	}
	m := parsePowerContract(t, output)
	if m["status"] != "failed" {
		t.Fatalf("status=%v, want failed", m["status"])
	}
	if msg, _ := m["message"].(string); !strings.Contains(msg, "sessao interativa") {
		t.Fatalf("message=%q deve explicar a falha", msg)
	}
}

func TestPowerVerbsCoverAllWireVerbs(t *testing.T) {
	// Guarda de drift: todo verbo exposto pelo agent genérico precisa ser
	// reconhecido em agentcommands E ter spec no app.
	verbs := []string{
		powerActionRestartFirmware,
		powerActionRestartAdvanced,
		powerActionCancel,
		powerActionLock,
	}
	for _, verb := range verbs {
		if !isPowerActionVerb(verb) {
			t.Fatalf("%q nao reconhecido por agentcommands.IsPowerActionVerb", verb)
		}
		if _, ok := resolvePowerVerbSpec(verb); !ok {
			t.Fatalf("%q sem spec em resolvePowerVerbSpec", verb)
		}
	}
	if isPowerActionVerb("hibernate") {
		t.Fatal("hibernate ainda nao e um verbo exposto pelo agent")
	}
}
