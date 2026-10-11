package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"discovery/app/core/screenshot"
	"discovery/app/supportchat"
)

// ── Janela independente do chat de suporte ──────────────────────────────────
//
// A janela é criada sob demanda (ao aceitar o convite ou ao retomar uma sessão
// ativa no startup) e é INDEPENDENTE da interface principal do agent. Fechá-la
// encerra o chat — mas só após a confirmação do usuário (modal na própria
// janela), porque o requisito é que ela só feche quando o chat terminar.

const (
	supportChatWindowName = "support-chat"
	supportChatCallWait   = 30 * time.Second
)

// openSupportChatWindow abre (ou foca) a janela do chat.
//
// Não é exportado de propósito: a abertura da janela é decidida pelo Go
// (aceite do convite / retomada no startup) e não deve ser acionável pelo
// webview.
func (a *App) openSupportChatWindow() {
	app := a.app
	if app == nil {
		a.Logs.Append("[supportchat] aplicação Wails indisponível; janela do chat não pode ser aberta")
		return
	}

	a.supportChatMu.Lock()
	if a.supportChatWin != nil {
		win := a.supportChatWin
		a.supportChatMu.Unlock()
		win.Show()
		win.Focus()
		return
	}

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      supportChatWindowName,
		Title:     "Chat com o suporte",
		Width:     520,
		Height:    680,
		MinWidth:  380,
		MinHeight: 420,
		URL:       "/chat-window.html",
	})
	a.supportChatWin = win
	a.supportChatMu.Unlock()

	win.RegisterHook(events.Common.WindowClosing, func(ev *application.WindowEvent) {
		if a.supportChatAllowClose.Load() {
			return
		}
		// Cancelar e pedir confirmação: o chat só termina quando o usuário
		// confirmar (ou quando o suporte encerrar pelo console).
		ev.Cancel()
		a.Logs.Append("[supportchat] fechamento da janela interceptado; aguardando confirmação do usuário")
		a.EmitEvent("supportchat:confirm_close")
	})

	win.Show()
	win.Focus()
	a.Logs.Append("[supportchat] janela do chat aberta")
}

// closeSupportChatWindow destrói a janela depois do encerramento confirmado.
func (a *App) closeSupportChatWindow() {
	a.supportChatMu.Lock()
	win := a.supportChatWin
	a.supportChatWin = nil
	a.supportChatMu.Unlock()

	if win == nil {
		return
	}

	a.supportChatAllowClose.Store(true)
	win.Close()
	a.Logs.Append("[supportchat] janela do chat fechada")
}

func (a *App) supportChatContext() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// ── Bindings expostos à janela do chat ──────────────────────────────────────

// resumeSupportChatIfActive consulta o servidor no startup e reabre a janela do
// chat quando existe sessão ativa/pendente deste agent.
//
// O consumer JetStream durável já reentregaria as mensagens não confirmadas, mas
// a JANELA precisa ser reaberta: sem isso o usuário ficaria com uma conversa
// "ativa" no servidor sem nenhuma interface para responder.
func (a *App) resumeSupportChatIfActive() {
	// Atraso curto: o asset server precisa estar pronto para servir a página.
	select {
	case <-time.After(3 * time.Second):
	case <-a.supportChatContext().Done():
		return
	}

	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	state, err := a.supportChatService().Refresh(ctx)
	if err != nil {
		a.Logs.Append("[supportchat] retomada no startup: servidor indisponível — " + err.Error())
		return
	}

	if !state.HasSession {
		return
	}

	a.Logs.Append("[supportchat] sessão ativa encontrada no startup; reabrindo a janela do chat")
	a.openSupportChatWindow()
}

// SupportChatAttachmentPolicyJSON devolve accept/tamanho para o seletor de arquivos.
func (a *App) SupportChatAttachmentPolicyJSON() map[string]any {
	return a.supportChatAttachmentPolicy()
}

// SupportChatRefresh faz o snapshot REST (abertura da janela / reconciliação).
func (a *App) SupportChatRefresh() (supportchat.State, error) {
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	return a.supportChatService().Refresh(ctx)
}

// SupportChatSendMessage envia uma mensagem do usuário final.
func (a *App) SupportChatSendMessage(body string, attachmentIDs []string) error {
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	return a.supportChatService().SendMessage(ctx, body, attachmentIDs)
}

// SupportChatUploadAttachment sobe um anexo (prepare -> PUT -> complete).
func (a *App) SupportChatUploadAttachment(fileName, contentType, base64Data string) (string, error) {
	fileName = strings.TrimSpace(fileName)
	contentType = strings.TrimSpace(contentType)
	if fileName == "" || contentType == "" {
		return "", errSupportChatInvalidAttachment
	}

	data, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", errSupportChatInvalidAttachment
	}

	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	return a.supportChatService().UploadAttachment(ctx, fileName, contentType, data)
}

// SupportChatCaptureScreen captura a tela do monitor principal para o usuário
// anexar ao chat — o mesmo recurso que o chat com a IA tem.
//
// A captura é iniciada pelo PRÓPRIO usuário (clique no botão), então não passa
// pelo fluxo de consentimento por captura usado quando quem pede é a IA: o
// clique é o consentimento. A imagem é redimensionada para um teto de 1920px a
// fim de caber na allowlist/tamanho de anexo do servidor.
func (a *App) SupportChatCaptureScreen() (map[string]string, error) {
	a.initScreenshotService()

	monitorIndex := 0
	for _, monitor := range screenshot.ListMonitors() {
		if monitor.Primary {
			monitorIndex = monitor.Index
			break
		}
	}

	result, err := screenshot.CaptureScreen(screenshot.Request{
		Mode:         screenshot.ModeMonitor,
		MonitorIndex: monitorIndex,
		MaxDimension: 1920,
		Format:       "png",
	})
	if err != nil {
		a.Logs.Append("[supportchat] captura de tela falhou: " + err.Error())
		return nil, err
	}

	contentType := strings.TrimSpace(result.MIME)
	extension := "png"
	switch {
	case strings.Contains(contentType, "webp"):
		extension = "webp"
	case strings.Contains(contentType, "jpeg"), strings.Contains(contentType, "jpg"):
		extension = "jpg"
	}
	if contentType == "" {
		contentType = "image/png"
	}

	fileName := "print-" + time.Now().Format("20060102-150405") + "." + extension
	a.Logs.Append(fmt.Sprintf("[supportchat] captura de tela anexada pelo usuário (%s, monitor %d, %dx%d)",
		fileName, result.Monitor, result.Width, result.Height))

	return map[string]string{
		"contentType": contentType,
		"base64":      result.Base64(),
		"fileName":    fileName,
	}, nil
}

// SupportChatDownloadAttachment devolve o anexo em base64 + content type para
// a janela exibir/baixar (o webview não carrega o token do agent).
func (a *App) SupportChatDownloadAttachment(attachmentID string) (map[string]string, error) {
	attachmentID = strings.TrimSpace(attachmentID)
	if attachmentID == "" {
		return nil, errSupportChatInvalidAttachment
	}

	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	data, contentType, err := a.supportChatService().DownloadAttachment(ctx, attachmentID)
	if err != nil {
		return nil, err
	}

	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return map[string]string{
		"contentType": contentType,
		"base64":      base64.StdEncoding.EncodeToString(data),
	}, nil
}

// SupportChatOpenInviteWindow abre a janela do chat já com o convite pendente.
//
// Chamado pelo processo de UI quando recebe `supportchat:invite` (o serviço, que
// recebe o comando do servidor, não tem Wails para abrir janela).
func (a *App) SupportChatOpenInviteWindow(sessionID, ticketID, ticketTitle, supportName string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errSupportChatInvalidInvite
	}

	svc := a.supportChatService()

	// Defesa extra: um convite não pode derrubar uma conversa ATIVA de outro
	// chamado (a recusa por busy é resolvida no serviço).
	if state := svc.State(); state.Status == supportchat.StatusActive && state.SessionID != sessionID {
		return errSupportChatBusy
	}

	svc.PrepareInvite(supportchat.Invite{
		SessionID:   sessionID,
		TicketID:    ticketID,
		TicketTitle: ticketTitle,
		SupportName: supportName,
	})

	a.openSupportChatWindow()
	return nil
}

// SupportChatRespondInvite responde ao convite DENTRO da janela do chat.
//
//   - aceite: reporta ao servidor, transita o estado e carrega a conversa;
//   - recusa: reporta, descarta o estado e fecha a janela.
func (a *App) SupportChatRespondInvite(accepted bool, reason string) error {
	svc := a.supportChatService()

	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	if err := svc.RespondInvite(ctx, accepted, reason); err != nil {
		return err
	}

	if accepted {
		_, err := svc.Refresh(ctx)
		return err
	}

	svc.ClearState()
	a.closeSupportChatWindow()
	return nil
}

// SupportChatEnd encerra a sessão pelo usuário.
func (a *App) SupportChatEnd(reason string) error {
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	return a.supportChatService().End(ctx, reason)
}

// SupportChatConfirmClose encerra o chat e fecha a janela (chamado após o
// usuário confirmar no modal). O estado local é descartado — o agent não
// mantém histórico de conversas.
func (a *App) SupportChatConfirmClose() error {
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	svc := a.supportChatService()
	err := svc.End(ctx, "user_closed")
	svc.ClearState()
	a.closeSupportChatWindow()

	return err
}
