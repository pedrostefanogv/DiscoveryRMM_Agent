package main

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	appkg "discovery/app"
)

// activationArgForTest monta o argumento base64 que o wintoast injeta na linha
// de comando quando o usuário clica na notificação (mesmo formato do pacote de
// notificações: {action, payload:{id,...}}).
func activationArgForTest(t *testing.T, id string) string {
	t.Helper()
	payload := map[string]any{
		"action": "DEFAULT_ACTION",
		"payload": map[string]any{
			"id":    id,
			"title": "Nova resposta no Chat IA",
			"body":  "texto",
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload do toast: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// Guarda de regressão: o clique num toast de chat NÃO pode ser tratado como
// relançamento automático. O payload de ativação pode vir acompanhado das flags
// do autostart (--startup-minimized) — suprimir a janela ali faria o clique no
// toast parecer que não funcionou.
func TestShouldShowMainWindowOnSecondInstance(t *testing.T) {
	// Espelha chatNotificationID de app/chat_notifications.go (o const é
	// interno): se ele mudar, este teste falha e denuncia o desalinhamento.
	toastPayload := activationArgForTest(t, "discovery.chat.response.ready")

	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "abertura manual", args: []string{"--startup-source=manual"}, want: true},
		{name: "sem argumentos", args: nil, want: true},
		{name: "autostart", args: []string{"--startup-minimized", "--startup-source=task-scheduler"}, want: false},
		{name: "watchdog", args: []string{"--startup-minimized", "--startup-source=watchdog"}, want: false},
		{name: "toast sozinho", args: []string{toastPayload}, want: true},
		{
			name: "toast com flags do autostart",
			args: []string{"--startup-minimized", toastPayload},
			want: true,
		},
		{
			name: "toast de outro recurso com flags do autostart",
			args: []string{"--startup-minimized", activationArgForTest(t, "outro.recurso")},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldShowMainWindowOnSecondInstance(tt.args); got != tt.want {
				t.Errorf("shouldShowMainWindowOnSecondInstance(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// Guarda de consistência: TODA origem automática (a mesma lista usada pelo
// single-instance para não roubar foco) precisa forçar o start minimizado.
// Sem isso, um relançamento automático abriria a janela cheia na cara do
// usuário — exatamente o que o watchdog passa a fazer em intervalos regulares.
func TestEnforceStartupMinimized_CoversAutomaticSources(t *testing.T) {
	sources := []string{
		"autostart",
		"task-scheduler",
		"startup-link",
		"update-restart",
		appkg.StartupSourceWatchdog,
	}
	for _, source := range sources {
		if _, minimized := enforceStartupMinimized(source, false); !minimized {
			t.Errorf("origem automática %q não forçou start minimizado", source)
		}
		if !appkg.IsAutomaticStartupArgs([]string{"--startup-source=" + source}) {
			t.Errorf("origem %q não é reconhecida por IsAutomaticStartupArgs", source)
		}
	}
}

func TestEnforceStartupMinimized(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		minimized     bool
		wantSource    string
		wantMinimized bool
	}{
		{
			name:          "update-restart sem flag força minimized",
			source:        "update-restart",
			minimized:     false,
			wantSource:    "update-restart",
			wantMinimized: true,
		},
		{
			name:          "update-restart com flag mantém minimized",
			source:        "update-restart",
			minimized:     true,
			wantSource:    "update-restart",
			wantMinimized: true,
		},
		{
			name:          "autostart sem flag força minimized",
			source:        "autostart",
			minimized:     false,
			wantSource:    "autostart",
			wantMinimized: true,
		},
		{
			name:          "task-scheduler sem flag força minimized",
			source:        "task-scheduler",
			minimized:     false,
			wantSource:    "task-scheduler",
			wantMinimized: true,
		},
		{
			name:          "startup-link sem flag força minimized",
			source:        "startup-link",
			minimized:     false,
			wantSource:    "startup-link",
			wantMinimized: true,
		},
		{
			name:          "watchdog sem flag força minimized",
			source:        "watchdog",
			minimized:     false,
			wantSource:    "watchdog",
			wantMinimized: true,
		},
		{
			name:          "manual não é alterado",
			source:        "manual",
			minimized:     false,
			wantSource:    "manual",
			wantMinimized: false,
		},
		{
			name:          "origem desconhecida não é alterada",
			source:        "custom-source",
			minimized:     false,
			wantSource:    "custom-source",
			wantMinimized: false,
		},
		{
			name:          "casing variado é tratado (normalização feita antes)",
			source:        "Update-Restart",
			minimized:     false,
			wantSource:    "Update-Restart",
			wantMinimized: false, // função pura não normaliza; main.go normaliza antes
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSource, gotMinimized := enforceStartupMinimized(tt.source, tt.minimized)
			if gotSource != tt.wantSource {
				t.Errorf("source = %q, want %q", gotSource, tt.wantSource)
			}
			if gotMinimized != tt.wantMinimized {
				t.Errorf("minimized = %v, want %v", gotMinimized, tt.wantMinimized)
			}
		})
	}
}
