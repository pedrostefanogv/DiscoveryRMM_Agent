package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"discovery/app/coreagent"
	"discovery/app/logs"
)

// ── Testes do handler de power (remote_debug_commands.go) com seams ──
//
// Rodam o handler REAL (handleAgentRuntimeCommand) para o canal de comandos,
// com os hooks de pacote (powerCountdownHook/powerExecHook) substituindo o
// countdown nativo (PSADT) e a execução de SO (shutdown.exe). Nada de SO é
// tocado. A janela de manutenção é avaliada com o relógio injetável
// (maintenanceNow) — prova ponta a ponta do contrato JSON sem deferral.

// powerApp monta um App{} zerado com buffer de logs e config de janela.
func powerApp(t *testing.T, configJSON string) *App {
	t.Helper()
	a := &App{}
	if configJSON != "" {
		a.AgentConfig = mustParseAgentConfigForMaintenance(t, configJSON)
	}
	a.Logs = coreagent.LogBuffer{Buffer: logs.New()}
	return a
}

// powerPayload decodifica o payload do comando de power (mesmo formato do
// servidor: {"delaySeconds":15,"force":false,"notifyUser":true,"message":""}).
func powerPayload(t *testing.T, raw string) any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return v
}

// parsePowerContract valida o JSON do contrato e devolve o mapa completo.
func parsePowerContract(t *testing.T, output string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(output), &m); err != nil {
		t.Fatalf("output não é o contrato JSON: %v\noutput: %s", err, output)
	}
	if strings.Contains(output, "\n") {
		t.Fatalf("contrato deve ser uma única linha: %q", output)
	}
	return m
}

// restorePowerHooks restaura os seams e o relógio ao fim do teste.
func restorePowerHooks(t *testing.T) {
	t.Helper()
	origNow, origCountdown, origExec := maintenanceNow, powerCountdownHook, powerExecHook
	t.Cleanup(func() {
		maintenanceNow = origNow
		powerCountdownHook = origCountdown
		powerExecHook = origExec
	})
}

// Cenário 1 (requisito central): janela ATIVA e "agora" FORA dela + comando
// interativo notifyUser=true/force=false → o handler executa (countdown +
// exec chamados, status executed) e NUNCA emite deferred_to_window.
func TestPowerHandlerInteractiveOutsideWindowExecutes(t *testing.T) {
	restorePowerHooks(t)
	a := powerApp(t, `{"maintenanceWindow":{"enabled":true,"startUtc":"02:00","durationMinutes":60}}`)
	maintenanceNow = func() time.Time { return utcAtPower(2024, 1, 8, 5, 0) } // fora da janela

	countdownCalls, execCalls := 0, 0
	powerCountdownHook = func(_ *App, action string, delaySeconds int, _ string, force bool) string {
		countdownCalls++
		if action != "restart" || delaySeconds != 15 || force {
			t.Fatalf("countdown inesperado: action=%s delay=%d force=%t", action, delaySeconds, force)
		}
		return "proceed"
	}
	powerExecHook = func(_ *App, _ context.Context, action string, _ int, _ bool, _ string) (int, string, string) {
		execCalls++
		return 0, "", ""
	}

	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(context.Background(), "restart", powerPayload(t, `{"delaySeconds":15,"force":false,"notifyUser":true,"message":""}`))
	if !handled || exitCode != 0 || errText != "" {
		t.Fatalf("handled=%t exit=%d err=%q", handled, exitCode, errText)
	}
	if countdownCalls != 1 || execCalls != 1 {
		t.Fatalf("countdown=%d exec=%d, want 1/1", countdownCalls, execCalls)
	}
	m := parsePowerContract(t, output)
	if m["status"] != "executed" {
		t.Fatalf("status=%v, want executed (output=%s)", m["status"], output)
	}
	if m["action"] != "restart" {
		t.Fatalf("action=%v, want restart", m["action"])
	}
	if strings.Contains(output, "deferred_to_window") {
		t.Fatalf("comando interativo NUNCA pode virar deferred_to_window: %s", output)
	}
	// WARN da janela ignorada, com a próxima janela em RFC3339.
	if !containsLine(a.Logs.GetAll(), "[MAINTENANCE] janela ativa (02:00-03:00) mas comando interativo — executando agora (próxima janela: 2024-01-09T02:00:00Z)") {
		t.Fatalf("WARN da janela ignorada ausente nos logs:\n%s", strings.Join(a.Logs.GetAll(), "\n"))
	}
}

// Cenário 2: force=true fora da janela → sempre executa (action shutdown).
func TestPowerHandlerForceOutsideWindowExecutes(t *testing.T) {
	restorePowerHooks(t)
	a := powerApp(t, `{"maintenanceWindow":{"enabled":true,"startUtc":"02:00","durationMinutes":60}}`)
	maintenanceNow = func() time.Time { return utcAtPower(2024, 1, 8, 5, 0) }

	execCalls := 0
	powerCountdownHook = func(_ *App, _ string, _ int, _ string, _ bool) string { return "proceed" }
	powerExecHook = func(_ *App, _ context.Context, _ string, _ int, _ bool, _ string) (int, string, string) {
		execCalls++
		return 0, "", ""
	}
	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(context.Background(), "shutdown", powerPayload(t, `{"delaySeconds":10,"force":true,"notifyUser":true,"message":""}`))
	if !handled || exitCode != 0 || errText != "" {
		t.Fatalf("handled=%t exit=%d err=%q", handled, exitCode, errText)
	}
	if execCalls != 1 {
		t.Fatalf("exec=%d, want 1", execCalls)
	}
	if m := parsePowerContract(t, output); m["status"] != "executed" || m["action"] != "shutdown" {
		t.Fatalf("contrato=%v", m)
	}
}

// Cenário 3: notifyUser=false → timer interno do agente (scheduled) e
// NENHUMA execução imediata.
func TestPowerHandlerSilentSchedulesNotExecutes(t *testing.T) {
	restorePowerHooks(t)
	a := powerApp(t, `{"maintenanceWindow":{"enabled":true,"startUtc":"02:00","durationMinutes":60,"rebootGraceSeconds":0}}`)
	maintenanceNow = func() time.Time { return utcAtPower(2024, 1, 8, 5, 0) }

	execCalls := 0
	powerExecHook = func(_ *App, _ context.Context, _ string, _ int, _ bool, _ string) (int, string, string) {
		execCalls++
		return 0, "", ""
	}
	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(context.Background(), "restart", powerPayload(t, `{"delaySeconds":300,"force":false,"notifyUser":false,"message":""}`))
	if !handled || exitCode != 0 || errText != "" {
		t.Fatalf("handled=%t exit=%d err=%q", handled, exitCode, errText)
	}
	if execCalls != 0 {
		t.Fatal("notifyUser=false não pode executar imediatamente")
	}
	m := parsePowerContract(t, output)
	if m["status"] != "scheduled" {
		t.Fatalf("status=%v, want scheduled (output=%s)", m["status"], output)
	}
	if dv, _ := m["delaySeconds"].(float64); int(dv) != 300 {
		t.Fatalf("delaySeconds=%v, want 300", m["delaySeconds"])
	}
	// Texto legado preservado na message para o fallback dos agentes antigos.
	if msg, _ := m["message"].(string); !strings.Contains(msg, "agendado silenciosamente") {
		t.Fatalf("message=%q, deve conter \"agendado silenciosamente\"", msg)
	}
}

// Cenário 4: usuário adia no countdown → deferred_by_user com defer 1/3/60
// (defer=current/max/min); nada executa agora.
func TestPowerHandlerUserDeferIsDeferredByUser(t *testing.T) {
	restorePowerHooks(t)
	a := powerApp(t, "")
	execCalls := 0
	powerCountdownHook = func(*App, string, int, string, bool) string { return "defer" }
	powerExecHook = func(*App, context.Context, string, int, bool, string) (int, string, string) {
		execCalls++
		return 0, "", ""
	}
	handled, exitCode, output, _ := a.handleAgentRuntimeCommand(context.Background(), "restart", powerPayload(t, `{"delaySeconds":15,"force":false,"notifyUser":true,"message":""}`))
	if !handled || exitCode != 0 {
		t.Fatalf("handled=%t exit=%d", handled, exitCode)
	}
	if execCalls != 0 {
		t.Fatal("defer do usuário não pode executar a ação")
	}
	m := parsePowerContract(t, output)
	if m["status"] != "deferred_by_user" {
		t.Fatalf("status=%v, want deferred_by_user (output=%s)", m["status"], output)
	}
	if msg, _ := m["message"].(string); !strings.Contains(msg, "adiado pelo usuário") {
		t.Fatalf("message=%q, deve conter \"adiado pelo usuário\"", msg)
	}
	deferBlock, _ := m["defer"].(map[string]any)
	if deferBlock == nil {
		t.Fatalf("bloco defer ausente: %s", output)
	}
	if int(deferBlock["current"].(float64)) != 1 || int(deferBlock["max"].(float64)) != 3 || int(deferBlock["minutes"].(float64)) != 60 {
		t.Fatalf("defer=%v, want current=1 max=3 minutes=60", deferBlock)
	}
}

// Cenário 5: execução falha (hook devolve exit != 0) → status=failed com
// exit 1 e a mensagem de erro real no campo "message" e no errText.
func TestPowerHandlerExecutionFailureIsFailed(t *testing.T) {
	restorePowerHooks(t)
	a := powerApp(t, "")
	powerCountdownHook = func(*App, string, int, string, bool) string { return "proceed" }
	powerExecHook = func(*App, context.Context, string, int, bool, string) (int, string, string) {
		return 1, "", "shutdown.exe falhou: acesso negado"
	}
	handled, exitCode, output, errText := a.handleAgentRuntimeCommand(context.Background(), "restart", powerPayload(t, `{"delaySeconds":15,"force":false,"notifyUser":true,"message":""}`))
	if !handled || exitCode != 1 {
		t.Fatalf("handled=%t exit=%d, want exit=1", handled, exitCode)
	}
	m := parsePowerContract(t, output)
	if m["status"] != "failed" {
		t.Fatalf("status=%v, want failed (output=%s)", m["status"], output)
	}
	if msg, _ := m["message"].(string); !strings.Contains(msg, "acesso negado") {
		t.Fatalf("message=%q, deve preservar o erro real", msg)
	}
	if errText != "shutdown.exe falhou: acesso negado" {
		t.Fatalf("errText=%q, deve preservar o erro real", errText)
	}
}

// containsLine verifica se alguma linha de log contém a substring.
func containsLine(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
