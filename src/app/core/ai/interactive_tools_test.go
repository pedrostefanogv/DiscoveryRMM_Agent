package ai

import "testing"

// Regressão: as tools que pedem autorização ao usuário no chat não podem ficar
// sujeitas ao timeout de 60s do loop, senão a pergunta morre antes do clique.
func TestInteractiveToolRequiresUser(t *testing.T) {
	cases := []struct {
		name     string
		args     string
		expected bool
	}{
		{"ask_user", `{"question":"x"}`, true},
		{"read_file", `{"path":"C:/a.txt"}`, true},
		{"capture_screenshot", "", true},

		// Consentimento (mcp.ToolConsentFor) — por AÇÃO.
		{"export_inventory_pdf", "", true},
		{"install_package", `{"id":"Google.Chrome"}`, true},
		{"uninstall_package", `{"id":"7zip.7zip"}`, true},
		{"upgrade_all_packages", "", true},
		{"power_action", `{"action":"restart"}`, true},

		// Mesma tool, ação de leitura: NÃO é interativa.
		{"service_control", `{"action":"list"}`, false},
		{"service_control", `{"action":"stop","name":"Spooler"}`, true},
		{"process_control", `{"action":"list"}`, false},
		{"process_control", `{"action":"kill","pid":4242}`, true},
		{"scheduled_task", `{"action":"list"}`, false},
		{"scheduled_task", `{"action":"run","taskName":"X"}`, true},
		{"printer", `{"action":"list"}`, false},
		{"printer", `{"action":"remove","name":"HP"}`, true},

		// Diagnóstico puro nunca é interativo.
		{"get_inventory", "", false},
		{"search_packages", `{"query":"firefox"}`, false},
		{"get_logs", "", false},
		{"shares", `{"action":"shares"}`, false},
		{"osquery", `{"sql":"select 1"}`, false},
	}

	for _, tc := range cases {
		if got := interactiveToolRequiresUser(tc.name, tc.args); got != tc.expected {
			t.Errorf("interactiveToolRequiresUser(%q, %s) = %v, want %v", tc.name, tc.args, got, tc.expected)
		}
	}
}
