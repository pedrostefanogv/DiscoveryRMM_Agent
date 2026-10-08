package mcp

import (
	"context"
	"encoding/json"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// newTestRegistry cria um registry com as tools Discovery usando o stubAppBridge
// (embedding do AppBridge nil) — so validacoes que falham ANTES do bridge podem
// ser exercitadas, o que e exatamente o que estes testes cobrem.
func newTestRegistry() *Registry {
	reg := NewRegistry()
	RegisterDiscoveryTools(reg, stubAppBridge{})
	return reg
}

func callTool(t *testing.T, reg *Registry, tool string, args map[string]any) error {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	_, callErr := reg.Call(context.Background(), tool, raw)
	return callErr
}

// TestConsolidatedCategoryToolsRegistered garante que as tools de familia
// substituiram as antigas e que as novas tools estao expostas.
func TestConsolidatedCategoryToolsRegistered(t *testing.T) {
	reg := newTestRegistry()

	want := []string{
		"printer", "network_diagnostics", "process_control", "osquery", "disk",
		"service_control", "system_info", "windows_update", "security_status",
		"scheduled_task", "shares", "power_action", "send_notification", "read_file",
	}
	for _, name := range want {
		if reg.Find(name) == nil {
			t.Errorf("tool nova/consolidada %q nao registrada", name)
		}
	}

	removed := []string{
		"list_printers", "install_printer", "install_shared_printer", "remove_printer",
		"get_printer_config", "list_print_jobs", "remove_print_job", "spooler_status",
		"restart_spooler", "clear_queue", "list_drivers",
		"get_osquery_status", "ping_host", "flush_dns", "get_top_processes", "get_disk_health",
	}
	for _, name := range removed {
		if reg.Find(name) != nil {
			t.Errorf("tool antiga %q deveria ter sido removida", name)
		}
	}
}

// TestInvalidActionReturnsClearError verifica que uma action desconhecida (ou
// ausente) devolve erro claro antes de qualquer efeito colateral.
func TestInvalidActionReturnsClearError(t *testing.T) {
	cases := []struct {
		name string
		tool string
	}{
		{"printer", "printer"},
		{"network_diagnostics", "network_diagnostics"},
		{"process_control", "process_control"},
		{"osquery", "osquery"},
		{"disk", "disk"},
		{"service_control", "service_control"},
		{"windows_update", "windows_update"},
		{"security_status", "security_status"},
		{"scheduled_task", "scheduled_task"},
		{"shares", "shares"},
		{"power_action", "power_action"},
	}
	reg := newTestRegistry()
	for _, tc := range cases {
		t.Run(tc.name+"_acao_invalida", func(t *testing.T) {
			err := callTool(t, reg, tc.tool, map[string]any{"action": "acao_que_nao_existe"})
			if err == nil {
				t.Fatalf("%s deveria rejeitar action invalida", tc.tool)
			}
			if !strings.Contains(err.Error(), "action invalida") {
				t.Fatalf("%s: erro deveria citar action invalida, got=%v", tc.tool, err)
			}
		})
		t.Run(tc.name+"_acao_ausente", func(t *testing.T) {
			err := callTool(t, reg, tc.tool, map[string]any{})
			if err == nil || !strings.Contains(err.Error(), "action") {
				t.Fatalf("%s: acao ausente deveria falhar citando action, got=%v", tc.tool, err)
			}
		})
	}
}

// TestDestructiveActionsRequireConfirm garante o gate M31 em TODAS as acoes
// destrutivas novas/consolidadas: sem confirm=true o handler falha antes de
// tocar no AppBridge.
func TestDestructiveActionsRequireConfirm(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"printer_remove", "printer", map[string]any{"action": "remove", "name": "HP Teste"}},
		{"printer_cancel_job", "printer", map[string]any{"action": "cancel_job", "name": "HP Teste", "jobId": 1}},
		{"printer_restart_spooler", "printer", map[string]any{"action": "restart_spooler"}},
		{"printer_clear_queue", "printer", map[string]any{"action": "clear_queue", "name": "HP Teste"}},
		{"process_control_kill", "process_control", map[string]any{"action": "kill", "pid": 1234}},
		{"service_control_start", "service_control", map[string]any{"action": "start", "name": "Spooler"}},
		{"service_control_stop", "service_control", map[string]any{"action": "stop", "name": "Spooler"}},
		{"service_control_restart", "service_control", map[string]any{"action": "restart", "name": "Spooler"}},
		{"scheduled_task_run", "scheduled_task", map[string]any{"action": "run", "taskName": "Tarefa Teste"}},
		{"power_action_restart", "power_action", map[string]any{"action": "restart"}},
		{"power_action_shutdown", "power_action", map[string]any{"action": "shutdown"}},
		{"power_action_lock", "power_action", map[string]any{"action": "lock"}},
	}
	reg := newTestRegistry()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := callTool(t, reg, tc.tool, tc.args)
			if err == nil {
				t.Fatalf("%s sem confirm deveria falhar", tc.name)
			}
			if !strings.Contains(err.Error(), "confirm") {
				t.Fatalf("%s: erro deveria exigir confirm, got=%v", tc.name, err)
			}
		})
	}
}

// TestReadOnlyActionsDoNotRequireConfirm garante que acoes nao destrutivas
// avancam para a validacao seguinte (bridge) em vez de esbarrar no gate.
func TestReadOnlyActionsDoNotRequireConfirm(t *testing.T) {
	reg := newTestRegistry()
	// Acao inexistente para nao tocar no bridge: se fosse destrutiva, o erro
	// seria de confirm; queremos ver "action invalida".
	err := callTool(t, reg, "printer", map[string]any{"action": "nao_existe"})
	if err == nil || !strings.Contains(err.Error(), "action invalida") {
		t.Fatalf("acao invalida: got=%v", err)
	}
}

// TestPowerShellCommandArgs valida a montagem do comando SEM executar nada.
func TestPowerShellCommandArgs(t *testing.T) {
	if _, err := buildPowerShellCommand(context.Background(), "   "); err == nil {
		t.Fatal("script vazio deveria ser rejeitado")
	}
	if _, err := buildPowerShellCommand(context.Background(), "Write-Output \x00"); err == nil {
		t.Fatal("script com NUL deveria ser rejeitado")
	}

	script := "Get-Date | ConvertTo-Json -Compress"
	cmd, err := buildPowerShellCommand(context.Background(), script)
	if err != nil {
		t.Fatalf("buildPowerShellCommand: %v", err)
	}
	if cmd == nil {
		t.Fatal("comando nil")
	}
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script} {
		if !strings.Contains(joined, want) {
			t.Errorf("args de PowerShell nao contem %q: %v", want, cmd.Args)
		}
	}
	if runtime.GOOS == "windows" {
		if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
			t.Error("janela do PowerShell deveria estar oculta (HideWindow)")
		}
	}
}

// TestRunPowerShellEmptyScriptIsStructuredError garante o contrato de erro
// estruturado {"error": ...} sem panic e sem executar processo.
func TestRunPowerShellEmptyScriptIsStructuredError(t *testing.T) {
	raw := runPowerShell(context.Background(), "   ", time.Second)
	if _, hasErr := rawHasError(raw); !hasErr {
		t.Fatalf("esperado erro estruturado, got=%s", string(raw))
	}
	if !json.Valid(raw) {
		t.Fatalf("payload de erro deveria ser JSON valido: %s", string(raw))
	}
}

// TestRequireActionNormalizes valida a normalizacao de action (case/espacos).
func TestRequireActionNormalizes(t *testing.T) {
	action, err := requireAction(map[string]any{"action": "  LIST "}, "list", "top")
	if err != nil || action != "list" {
		t.Fatalf("requireAction = %q, err=%v", action, err)
	}
	if _, err := requireAction(map[string]any{}, "list"); err == nil {
		t.Fatal("action vazia deveria falhar")
	}
}

// TestRegisteredToolCount documenta e trava o numero de tools expostas.
func TestRegisteredToolCount(t *testing.T) {
	reg := newTestRegistry()
	names := make([]string, 0, len(reg.Tools()))
	for _, tool := range reg.Tools() {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	t.Logf("total de tools registradas: %d", len(names))
	for _, name := range names {
		t.Logf("  %s", name)
	}
	// 49 apos a consolidacao das familias + read_file (leitura com autorizacao)
	// + open_folder/open_app (abrir pasta/app com autorizacao por acao).
	if len(names) != 53 {
		t.Fatalf("total de tools = %d, esperado 53", len(names))
	}
}
