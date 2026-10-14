package app

import "testing"

// REQUISITO: relançamento automático da UI (task de logon, atalho de startup,
// restart pós-update ou watchdog) NÃO pode roubar o foco do usuário quando já
// existe uma UI viva. Antes, a segunda instância chamava ShowMainWindow() e a
// janela saltava na frente do que a pessoa estava fazendo.
func TestIsAutomaticStartupArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "sem argumentos", args: nil, want: false},
		{name: "argumento vazio", args: []string{""}, want: false},
		{name: "flag com --", args: []string{"--startup-minimized"}, want: true},
		{name: "flag com /", args: []string{"/startup-minimized"}, want: true},
		{name: "flag com -", args: []string{"-startup-minimized"}, want: true},
		{name: "flag sem prefixo", args: []string{"startup-minimized"}, want: true},
		{name: "maiúsculas", args: []string{"--STARTUP-MINIMIZED"}, want: true},
		{name: "origem watchdog", args: []string{"--startup-source=watchdog"}, want: true},
		{name: "origem task-scheduler", args: []string{"--startup-source=task-scheduler"}, want: true},
		{name: "origem autostart", args: []string{"--startup-source=autostart"}, want: true},
		{name: "origem startup-link", args: []string{"--startup-source=startup-link"}, want: true},
		{name: "origem update-restart", args: []string{"--startup-source=update-restart"}, want: true},
		{name: "origem watchdog com barra", args: []string{"/startup-source=watchdog"}, want: true},
		{name: "origem manual (clique do usuário)", args: []string{"--startup-source=manual"}, want: false},
		{name: "origem desconhecida", args: []string{"--startup-source=script-custom"}, want: false},
		{name: "apenas --debug", args: []string{"--debug"}, want: false},
		{name: "payload base64 do toast do chat", args: []string{"eyJvcHRpb25zIjp7ImlkIjoiZGlzY292ZXJ5LnN1cHBvcnRjaGF0Lmludml0ZSJ9fQ=="}, want: false},
		{name: "relançamento automático completo", args: []string{"--startup-minimized", "--startup-source=watchdog"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAutomaticStartupArgs(tt.args); got != tt.want {
				t.Errorf("IsAutomaticStartupArgs(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// A origem do watchdog precisa estar na lista de origens automáticas — é ela que
// força o start minimizado e evita pop-up no relançamento.
func TestAutomaticStartupSources_IncludesWatchdog(t *testing.T) {
	found := false
	for _, source := range automaticStartupSources {
		if source == StartupSourceWatchdog {
			found = true
		}
	}
	if !found {
		t.Fatalf("StartupSourceWatchdog (%q) deve estar em automaticStartupSources: %v",
			StartupSourceWatchdog, automaticStartupSources)
	}
}
