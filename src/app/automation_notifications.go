package app

// Toast nativo do sistema para as automation tasks ("Toast nativo do app").
//
// E o MESMO caminho do Chat IA (chat_notifications.go): o servico nativo de
// notificacoes do Wails v3 (Action Center do Windows), com o logo do app. A
// diferenca e o gatilho: aqui a origem e o modo de notificacao escolhido na
// tarefa (NotificationMode = NativeToast), nao a conclusao de um turno do chat.
//
// Quem envia:
//   - UI standalone (modo normal): o proprio App, no dispatcher da automation
//     (app.go), antes do evento ir para o frontend;
//   - UI companion (servico + UI conectada): esta UI, ao receber o evento
//     notification:new marcado com notificationSurface=native via IPC;
//   - servico sem UI: o fallback headless (native_toast_windows.go) continua
//     valendo, porque nao existe app Wails para enviar a notificacao nativa.
//
// Se o envio nativo falhar (servico nao inicializado/registro do toast
// indisponivel), o caller remove a marca de superficie e o toast volta a ser
// desenhado pela interface do agent — o usuario nunca fica sem aviso.

import (
	"strings"

	wailsnotifications "github.com/wailsapp/wails/v3/pkg/services/notifications"

	"discovery/app/core/automation"
)

// sendNativeTaskNotification envia o toast nativo do app para uma notificacao de
// automation task. Devolve false quando o servico nativo nao esta disponivel.
func (a *App) sendNativeTaskNotification(notificationID, title, message string) bool {
	if a == nil || a.nativeNotifications == nil || !a.nativeNotificationsStarted.Load() {
		return false
	}
	// Encerramento em andamento: nao faz sentido notificar (mesmo criterio do chat).
	if a.QuitRequested.Load() {
		return false
	}
	return a.sendNativeNotificationOptions(notificationID, title, message, a.nativeNotifications.SendNotification)
}

// sendNativeNotificationOptions monta as opcoes do toast (logo do app incluido)
// e delega ao sender injetado — permite testar a montagem/decisao sem o servico
// nativo real (o caminho do Chat IA segue o mesmo padrao).
func (a *App) sendNativeNotificationOptions(
	notificationID, title, message string,
	send func(wailsnotifications.NotificationOptions) error,
) bool {
	if a == nil || send == nil {
		return false
	}
	id := strings.TrimSpace(notificationID)
	if id == "" {
		id = "automation.notification"
	}
	// Mesmo tratamento do Chat IA: quando o push COM do wintoast falha, o XML do
	// toast e embutido num here-string do PowerShell que interpola $var/$(comando)
	// — o nome da tarefa/pacote e texto de origem externa e NAO pode virar
	// comando. O zero-width space depois de $ e de crase quebra a interpolacao e
	// nao aparece na tela.
	opts := wailsnotifications.NotificationOptions{
		ID:    id,
		Title: neutralizeToastInterpolation(strings.TrimSpace(title)),
		Body:  neutralizeToastInterpolation(strings.TrimSpace(message)),
		Data:  map[string]interface{}{"kind": "automation.task"},
	}
	if iconURL := a.ensureNotificationIcon(); iconURL != "" {
		// Logo da aplicacao no proprio toast (mesmo tratamento do Chat IA).
		opts.Attachments = []wailsnotifications.NotificationAttachment{
			{Path: iconURL, Type: "appLogoOverride"},
		}
	}
	if err := send(opts); err != nil {
		a.Logs.Append("[automation] aviso: envio nativo retornou erro (toast pode ter saido pelo fallback do PowerShell): " + err.Error())
		return false
	}
	return true
}

// maybeSendCompanionNativeToast atende o evento notification:new recebido do
// servico pela UI companion: quando a notificacao esta marcada como superficie
// nativa, envia o toast do sistema aqui (esta UI tem o servico de notificacoes
// do Wails) e, se falhar, remove a marca do payload para o frontend cair no toast
// da interface do agent.
func (a *App) maybeSendCompanionNativeToast(eventData []any) {
	if a == nil || len(eventData) == 0 {
		return
	}
	data, ok := eventData[0].(map[string]any)
	if !ok {
		return
	}
	metadata, ok := data["metadata"].(map[string]any)
	if !ok || !automation.IsNativeNotificationSurface(metadata) {
		return
	}
	// So o aviso informativo usa a via nativa. A politica por eventType
	// (aplicada pelo servico antes de emitir) pode ter tornado o evento "silent"
	// (nada a exibir) ou "require_confirmation" (UI propria): nesses casos a
	// superficie volta para a interface do agent, que sabe tratar cada modo.
	if mode, _ := data["mode"].(string); !strings.EqualFold(strings.TrimSpace(mode), "notify_only") {
		delete(metadata, automation.NotificationSurfaceMetadataKey)
		return
	}
	id, _ := data["id"].(string)
	title, _ := data["title"].(string)
	message, _ := data["message"].(string)
	if a.sendNativeTaskNotification(id, title, message) {
		return
	}
	// Sem servico nativo: o toast da interface do agent volta a ser a superficie.
	delete(metadata, automation.NotificationSurfaceMetadataKey)
}
