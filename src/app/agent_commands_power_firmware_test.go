package app

import (
	"context"
	"strings"
	"testing"
)

// ── Testes do verbo "restart-firmware" (comando genérico "poweraction") ──
//
// Provam o contrato JSON do reinício direto no setup do firmware (BIOS/UEFI):
//   - UEFI suportado → executa com o verbo interno restart-firmware;
//   - BIOS legado    → falha TIPADA, sem executar nada;
//   - action vazio   → falha tipada (payload inválido).
//
// O seam firmwareBootSupportFunc simula a capacidade; powerExecHook substitui o
// shutdown.exe real. Nada de SO é tocado.

func withFirmwareSupport(t *testing.T, supported bool, reason string) {
	t.Helper()
	orig := firmwareBootSupportFunc
	firmwareBootSupportFunc = func() (bool, string) { return supported, reason }
	t.Cleanup(func() { firmwareBootSupportFunc = orig })
}

// firmwarePayload monta o payload do comando genérico "poweraction".
func firmwarePayload(action string) any {
	return map[string]any{
		"action":       action,
		"delaySeconds": 0,
		"force":        true,
		"notifyUser":   false,
		"message":      "",
	}
}

func TestFirmwareRestartOnUefiExecutes(t *testing.T) {
	restorePowerHooks(t)
	withFirmwareSupport(t, true, "")
	a := powerApp(t, "")

	execCalls := 0
	powerExecHook = func(_ *App, _ context.Context, action string, _ int, _ bool, _ string) (int, string, string) {
		execCalls++
		if action != powerActionRestartFirmware {
			t.Fatalf("action=%q, want %q", action, powerActionRestartFirmware)
		}
		return 0, "", ""
	}

	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(
		context.Background(),
		"poweraction",
		firmwarePayload(powerActionRestartFirmware),
	)
	if !handled || exitCode != 0 || errText != "" {
		t.Fatalf("handled=%t exit=%d err=%q", handled, exitCode, errText)
	}
	if execCalls != 1 {
		t.Fatalf("exec=%d, want 1", execCalls)
	}
	m := parsePowerContract(t, output)
	if m["status"] != "executed" {
		t.Fatalf("status=%v, want executed (output=%s)", m["status"], output)
	}
	if m["action"] != powerActionRestartFirmware {
		t.Fatalf("action=%v, want %s", m["action"], powerActionRestartFirmware)
	}
}

func TestFirmwareRestartOnLegacyBiosFailsWithoutExecuting(t *testing.T) {
	restorePowerHooks(t)
	withFirmwareSupport(t, false, "computador em BIOS legado (Legacy/CSM); o reinicio no firmware exige boot UEFI")
	a := powerApp(t, "")

	execCalls := 0
	powerExecHook = func(*App, context.Context, string, int, bool, string) (int, string, string) {
		execCalls++
		return 0, "", ""
	}

	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(
		context.Background(),
		"poweraction",
		firmwarePayload(powerActionRestartFirmware),
	)
	if !handled {
		t.Fatal("comando deve ser tratado como energia")
	}
	if exitCode == 0 || errText == "" {
		t.Fatalf("exit=%d err=%q, want falha tipada", exitCode, errText)
	}
	if execCalls != 0 {
		t.Fatal("maquina Legacy nao pode executar shutdown /r /fw")
	}
	m := parsePowerContract(t, output)
	if m["status"] != "failed" {
		t.Fatalf("status=%v, want failed (output=%s)", m["status"], output)
	}
	if msg, _ := m["message"].(string); !strings.Contains(msg, "Legacy") {
		t.Fatalf("message=%q, deve explicar o motivo (Legacy)", msg)
	}
}

func TestPowerActionUnknownVerbFails(t *testing.T) {
	restorePowerHooks(t)
	withFirmwareSupport(t, true, "")
	a := powerApp(t, "")

	execCalls := 0
	powerExecHook = func(*App, context.Context, string, int, bool, string) (int, string, string) {
		execCalls++
		return 0, "", ""
	}

	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(
		context.Background(),
		"poweraction",
		firmwarePayload("desligar-tudo"),
	)
	if !handled || exitCode == 0 || errText == "" {
		t.Fatalf("handled=%t exit=%d err=%q, want falha tipada", handled, exitCode, errText)
	}
	if execCalls != 0 {
		t.Fatal("verbo desconhecido nao pode executar nada")
	}
	if m := parsePowerContract(t, output); m["status"] != "failed" {
		t.Fatalf("status=%v, want failed", m["status"])
	}
}
