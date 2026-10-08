package mcp

import (
	"strings"
	"testing"
)

// A política é por AÇÃO: a mesma tool pode exigir consentimento em uma ação
// (stop/kill/remove/run) e ser livre em outra (list/get).
func TestToolConsentForPolicy(t *testing.T) {
	cases := []struct {
		name       string
		args       map[string]any
		wantAction string
		wantNil    bool
	}{
		{"export_inventory_markdown", nil, "write_file", false},
		{"export_inventory_pdf", nil, "write_file", false},
		{"install_package", map[string]any{"id": "Google.Chrome"}, "install_package", false},
		{"uninstall_package", map[string]any{"id": "7zip.7zip"}, "uninstall_package", false},
		{"upgrade_package", map[string]any{"id": "Mozilla.Firefox"}, "upgrade_package", false},
		{"upgrade_all_packages", nil, "upgrade_all_packages", false},
		{"service_control", map[string]any{"action": "start", "name": "Spooler"}, "service_start", false},
		{"service_control", map[string]any{"action": "STOP", "name": "Spooler"}, "service_stop", false},
		{"service_control", map[string]any{"action": "restart", "name": "Spooler"}, "service_restart", false},
		{"service_control", map[string]any{"action": "list"}, "", true},
		{"scheduled_task", map[string]any{"action": "run", "taskName": "X"}, "run_scheduled_task", false},
		{"scheduled_task", map[string]any{"action": "list"}, "", true},
		{"process_control", map[string]any{"action": "kill", "pid": 4242}, "kill_process", false},
		{"process_control", map[string]any{"action": "top"}, "", true},
		{"printer", map[string]any{"action": "install", "name": "HP"}, "printer_install", false},
		{"printer", map[string]any{"action": "install_shared", "connectionPath": "\\\\srv\\hp"}, "printer_install_shared", false},
		{"printer", map[string]any{"action": "remove", "name": "HP"}, "printer_remove", false},
		{"printer", map[string]any{"action": "cancel_job", "name": "HP", "jobId": 7}, "printer_cancel_job", false},
		{"printer", map[string]any{"action": "restart_spooler"}, "printer_restart_spooler", false},
		{"printer", map[string]any{"action": "clear_queue", "name": "HP"}, "printer_clear_queue", false},
		{"printer", map[string]any{"action": "list"}, "", true},
		{"power_action", map[string]any{"action": "restart"}, "power_restart", false},
		{"power_action", map[string]any{"action": "shutdown"}, "power_shutdown", false},
		{"power_action", map[string]any{"action": "lock"}, "power_lock", false},

		// Leitura/diagnóstico: nunca pede consentimento.
		{"get_inventory", nil, "", true},
		{"read_file", map[string]any{"path": "C:/a.txt"}, "", true},
		{"shares", map[string]any{"action": "shares"}, "", true},
		{"windows_update", map[string]any{"action": "installed"}, "", true},
		{"osquery", map[string]any{"sql": "select 1"}, "", true},
		{"tool_desconhecida", nil, "", true},
	}

	for _, tc := range cases {
		req := ToolConsentFor(tc.name, tc.args)
		if tc.wantNil {
			if req != nil {
				t.Errorf("%s %v: não deveria pedir consentimento (action=%s)", tc.name, tc.args, req.Action)
			}
			continue
		}
		if req == nil {
			t.Errorf("%s %v: deveria pedir consentimento (%s)", tc.name, tc.args, tc.wantAction)
			continue
		}
		if req.Action != tc.wantAction {
			t.Errorf("%s %v: action=%q, want %q", tc.name, tc.args, req.Action, tc.wantAction)
		}
	}
}

func TestToolConsentForTargets(t *testing.T) {
	kill := ToolConsentFor("process_control", map[string]any{"action": "kill", "pid": float64(4242)})
	if kill == nil || !strings.Contains(kill.Target, "4242") {
		t.Fatalf("alvo do kill sem PID: %#v", kill)
	}

	job := ToolConsentFor("printer", map[string]any{"action": "cancel_job", "name": "HP", "jobId": float64(7)})
	if job == nil || !strings.Contains(job.Target, "HP") || !strings.Contains(job.Target, "7") {
		t.Fatalf("alvo do cancel_job incompleto: %#v", job)
	}

	power := ToolConsentFor("power_action", map[string]any{"action": "restart", "delaySeconds": float64(30), "message": "manutencao"})
	if power == nil || len(power.Extra) < 2 {
		t.Fatalf("power_action deveria trazer atraso e mensagem: %#v", power)
	}

	exp := ToolConsentFor("export_inventory_pdf", nil)
	if exp == nil || exp.Kind != "write" || len(exp.Extra) == 0 {
		t.Fatalf("export deveria ser write com destino: %#v", exp)
	}
}
