package app

import (
	"errors"
	"strings"
	"testing"

	wailsnotifications "github.com/wailsapp/wails/v3/pkg/services/notifications"

	"discovery/app/core/automation"
)

// Toast nativo de automation task: mesmas opcoes do Chat IA (ID/titulo/corpo +
// logo) e Data marcando a origem.
func TestSendNativeNotificationOptions_BuildsTaskToast(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	sender := &fakeSender{}

	if !a.sendNativeNotificationOptions("automation-exec-1-install_start", " Tarefa iniciada ", " Tarefa ColetarAnydesk iniciada. ", sender.send) {
		t.Fatal("envio nativo deveria ser aceito com sender valido")
	}
	if len(sender.sent) != 1 {
		t.Fatalf("esperado 1 envio, got %d", len(sender.sent))
	}
	opts := sender.sent[0]
	if opts.ID != "automation-exec-1-install_start" {
		t.Fatalf("ID inesperado: %q", opts.ID)
	}
	if opts.Title != "Tarefa iniciada" || opts.Body != "Tarefa ColetarAnydesk iniciada." {
		t.Fatalf("titulo/corpo deveriam estar sem espacos nas pontas: %q / %q", opts.Title, opts.Body)
	}
	if kind, _ := opts.Data["kind"].(string); kind != "automation.task" {
		t.Fatalf("kind inesperado: %+v", opts.Data)
	}
}

func TestSendNativeNotificationOptions_EmptyIDFallsBackAndErrorsAreReported(t *testing.T) {
	a := NewApp(AppStartupOptions{})

	var sent []wailsnotifications.NotificationOptions
	capture := func(opts wailsnotifications.NotificationOptions) error {
		sent = append(sent, opts)
		return nil
	}
	if !a.sendNativeNotificationOptions("   ", "Titulo", "Corpo", capture) {
		t.Fatal("envio com sender valido deveria ser aceito")
	}
	if sent[0].ID == "" {
		t.Fatal("ID vazio deveria cair no identificador padrao do toast")
	}

	failing := func(wailsnotifications.NotificationOptions) error { return errors.New("boom") }
	if a.sendNativeNotificationOptions("automation-1", "Titulo", "Corpo", failing) {
		t.Fatal("erro do sender deve devolver false (caller cai no toast do agent)")
	}
	if a.sendNativeNotificationOptions("automation-1", "Titulo", "Corpo", nil) {
		t.Fatal("sender nil deve devolver false")
	}
}

// Texto do toast com origem externa (nome da tarefa/pacote) precisa sair
// neutralizado: no fallback do wintoast o XML e embutido num here-string do
// PowerShell que interpolaria $var/$(comando)/crase.
func TestSendNativeNotificationOptions_NeutralizesPowerShellInterpolation(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	sender := &fakeSender{}

	title := "Tarefa $(calc.exe) iniciada"
	body := "Pacote `whoami` com crase"
	if !a.sendNativeNotificationOptions("automation-1", title, body, sender.send) {
		t.Fatal("envio nativo deveria ser aceito")
	}
	opts := sender.sent[0]
	if strings.Contains(opts.Title, "$(") {
		t.Fatalf("$( nao pode aparecer literal no titulo do toast: %q", opts.Title)
	}
	if !strings.Contains(opts.Title, chatNotificationZWSP) {
		t.Fatalf("titulo deveria conter o zero-width space de neutralizacao: %q", opts.Title)
	}
	if !strings.Contains(opts.Body, chatNotificationZWSP) {
		t.Fatalf("corpo com crase deveria conter o zero-width space: %q", opts.Body)
	}
}

// Politica que rebaixa o evento para silent (aplicada pelo servico antes de
// emitir) nao pode gerar toast nativo nenhum.
func TestMaybeSendCompanionNativeToast_IgnoresSilentPolicy(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	metadata := map[string]any{automation.NotificationSurfaceMetadataKey: automation.NotificationSurfaceNative}
	eventData := []any{map[string]any{
		"id":       "automation-exec-1-install_start",
		"mode":     "silent",
		"title":    "Tarefa iniciada",
		"message":  "Tarefa X iniciada.",
		"metadata": metadata,
	}}

	a.maybeSendCompanionNativeToast(eventData)

	if _, still := metadata[automation.NotificationSurfaceMetadataKey]; still {
		t.Fatal("superficie nativa deveria sair quando a politica rebaixa o evento para silent")
	}
}

// Evento que a politica tornou "require_confirmation" tem UI propria (modal/
// Welcome): o toast nativo nao pode ser enviado nem a superficie nativa mantida.
func TestMaybeSendCompanionNativeToast_IgnoresRequireConfirmation(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	metadata := map[string]any{automation.NotificationSurfaceMetadataKey: automation.NotificationSurfaceNative}
	eventData := []any{map[string]any{
		"id":       "automation-exec-1-install_start",
		"mode":     "require_confirmation",
		"title":    "Tarefa iniciada",
		"message":  "Tarefa X iniciada.",
		"metadata": metadata,
	}}

	a.maybeSendCompanionNativeToast(eventData)

	if _, still := metadata[automation.NotificationSurfaceMetadataKey]; still {
		t.Fatal("superficie nativa deveria sair quando o evento exige confirmacao")
	}
}

// Sem servico nativo (comum no servico/session 0), a superficie nativa precisa
// ser removida para o frontend cair no toast da interface do agent.
func TestMaybeSendCompanionNativeToast_WithoutNativeService_ClearsSurface(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	metadata := map[string]any{automation.NotificationSurfaceMetadataKey: automation.NotificationSurfaceNative}
	eventData := []any{map[string]any{
		"id":       "automation-exec-1-install_start",
		"mode":     "notify_only",
		"title":    "Tarefa iniciada",
		"message":  "Tarefa X iniciada.",
		"metadata": metadata,
	}}

	a.maybeSendCompanionNativeToast(eventData)

	// Continua nativo: nada foi enviado e nenhum aviso apareceria.
	if _, still := metadata[automation.NotificationSurfaceMetadataKey]; still {
		t.Fatal("superficie nativa deveria ser removida quando o envio nativo nao esta disponivel")
	}
}

func TestMaybeSendCompanionNativeToast_IgnoresAgentSurface(t *testing.T) {
	a := NewApp(AppStartupOptions{})
	metadata := map[string]any{automation.NotificationSurfaceMetadataKey: automation.NotificationSurfaceAgent}
	eventData := []any{map[string]any{"id": "automation-1", "metadata": metadata}}

	a.maybeSendCompanionNativeToast(eventData)

	if got := metadata[automation.NotificationSurfaceMetadataKey]; got != automation.NotificationSurfaceAgent {
		t.Fatalf("superficie do agent nao pode ser alterada: %v", got)
	}
}
