package app

import (
	"context"
	"encoding/base64"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

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
	// Portão por janela: o hook consulta ESTA janela e não um estado global que
	// sobrevive ao fechamento anterior.
	allowClose := &atomic.Bool{}
	a.supportChatWin = win
	a.supportChatAllowClose = allowClose
	a.supportChatMu.Unlock()

	win.RegisterHook(events.Common.WindowClosing, func(ev *application.WindowEvent) {
		if allowClose.Load() {
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
	allowClose := a.supportChatAllowClose
	a.supportChatWin = nil
	a.supportChatAllowClose = nil
	a.supportChatMu.Unlock()

	if win == nil {
		return
	}

	if allowClose != nil {
		allowClose.Store(true)
	}
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

// supportChatWindowOpen informa se a janela do chat já existe.
func (a *App) supportChatWindowOpen() bool {
	a.supportChatMu.Lock()
	defer a.supportChatMu.Unlock()
	return a.supportChatWin != nil
}

// resumeSupportChatIfActive consulta o servidor e abre a janela do chat quando
// existe sessão ativa/pendente deste agent E ainda não há janela aberta.
//
// Chamado no startup E a cada (re)conexão do NATS. Motivo: o convite trafega no
// canal unicast (`...agent.{id}.command`, core NATS — SEM replay). Num flap de
// conexão a mensagem se perde e a sessão fica `Pending` no servidor; o servidor
// só reentrega depois de ~2 min. Descobrir a sessão pendente pelo REST fecha
// essa janela de tempo — era o caso em que "o chat só aparecia reiniciando o
// agent".
//
// Nunca rouba foco: se a janela já existe (mesmo minimizada), não faz nada — o
// convite de uma conversa NOVA chega pelo evento `supportchat:invite`.
func (a *App) resumeSupportChatIfActive(delay time.Duration) {
	// Atraso curto: no startup o asset server precisa estar pronto para servir a
	// página; nas reconciliações seguintes o atraso é pequeno (ou zero).
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-a.supportChatContext().Done():
			return
		}
	}

	svc := a.supportChatService()

	// Estado local ANTES do snapshot: é ele que diz se a janela aberta apenas
	// aguardava um convite (waiting). Conversa ATIVA nunca é fechada por aqui.
	previous := svc.State()

	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	state, err := svc.Refresh(ctx)
	if err != nil {
		a.Logs.Append("[supportchat] retomada no startup: servidor indisponível — " + err.Error())
		return
	}

	if !state.HasSession {
		// Convite que morreu no servidor (expirou/foi encerrado) sem evento de
		// fim: a janela ficaria aberta e vazia ("SEM CONVERSA"). Ela é fechada
		// porque a conversa nunca começou — a ATIVA é preservada.
		if previous.Status == supportchat.StatusWaiting && a.supportChatWindowOpen() {
			a.Logs.Append("[supportchat] convite sem sessão no servidor; fechando a janela do chat")
			svc.ClearState()
			a.closeSupportChatWindow()
		}
		return
	}

	if a.supportChatWindowOpen() {
		return
	}

	a.Logs.Append("[supportchat] sessão ativa/pendente encontrada sem janela aberta; abrindo o chat")
	a.openSupportChatWindow()
}

// runSupportChatReconcileLoop reconcilia o estado do chat periodicamente.
//
// Rede de segurança contra o convite perdido no canal unicast (core NATS, sem
// replay) SEM depender de reconexão: o servidor só reentrega comando pendente
// depois de ~2 min, e o usuário não pode ficar esperando. Também cobre a
// retomada após suspensão da máquina.
func (a *App) runSupportChatReconcileLoop() {
	// Piso de 30s: é uma consulta pequena, mas não precisa ser frequente — o
	// caminho normal (evento supportchat:invite) continua sendo imediato.
	const interval = 60 * time.Second

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-a.supportChatContext().Done():
			return
		case <-ticker.C:
			a.resumeSupportChatIfActive(0)
		}
	}
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

// SupportChatRequestScreenshot inicia a captura ASSISTIDA — o MESMO overlay do
// chat com a IA: seleção de janela/aplicativo ou região da tela e ferramentas
// de anotação (setas, marcações, passos).
//
// A captura é conduzida AQUI, em goroutine, pela JANELA DEDICADA do overlay
// (screenshot_overlay_window.go) — não passa mais pela janela principal, que
// normalmente está no tray enquanto o usuário conversa com o suporte. O
// resultado volta para esta conversa como anexo pelo evento
// "supportchat:screenshot" (ou "supportchat:screenshot_failed" no erro).
func (a *App) SupportChatRequestScreenshot() error {
	if a.app == nil {
		return errSupportChatNoUI
	}

	go func() {
		if _, err := a.PrepareScreenshotOverlay("captura manual do usuario", screenshotDestinationSupportChat); err != nil {
			a.Logs.Append("[supportchat] captura assistida falhou: " + err.Error())
			// Libera o botão de print da janela do chat em vez de deixá-lo preso
			// até o watchdog de 60 s.
			a.EmitEvent("supportchat:screenshot_failed", map[string]any{"error": err.Error()})
			return
		}
		a.Logs.Append("[supportchat] captura assistida solicitada pelo usuário (janela dedicada de overlay)")
	}()

	return nil
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

	// Um convite só abre a janela depois de o SERVIDOR confirmar que esta sessão
	// ainda existe (pendente/ativa). A reentrega de comandos do servidor pode
	// entregar o convite minutos depois: sem esta checagem, o eco de uma sessão
	// já encerrada abria a janela vazia ("SEM CONVERSA") sozinho.
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	state, err := svc.Refresh(ctx)
	if err != nil {
		a.Logs.Append("[supportchat] convite ignorado: falha ao confirmar a sessão no servidor — " + err.Error())
		return err
	}
	if !state.HasSession || state.SessionID == "" {
		a.Logs.Append("[supportchat] convite ignorado: sessão " + sessionID + " não está mais pendente/ativa no servidor")
		return errSupportChatInviteStale
	}
	if state.SessionID != sessionID {
		a.Logs.Append("[supportchat] convite ignorado: o servidor tem outra sessão ativa (" + state.SessionID + ")")
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
