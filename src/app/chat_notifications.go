package app

// Notificação nativa (Wails v3) de conclusão do Chat IA.
//
// Regra de negócio: SOMENTE o chat dispara este caminho. Quando o turno da LLM
// termina e o resultado não está visível para o usuário — aba de chat em
// background, janela sem foco, minimizada ou escondida no tray — o agente emite
// um toast nativo do sistema para que o usuário volte à janela do chat.
//
// Casos cobertos:
//   - resposta concluída com sucesso (chat:done);
//   - falha real do turno (chat:error); recusa por turno em andamento
//     (ai.ErrTurnBusy) e cancelamento NÃO notificam;
//   - pergunta interativa (chat:question / ask_user): o agente fica BLOQUEADO
//     esperando o usuário, então essa notificação NÃO é suprimida pela
//     deduplicação por ausência (só pelo debounce).
//
// Privacidade: por padrão o corpo mostra um trecho da resposta (180 runas).
// A preferência chat.notifyPreview=false troca por texto genérico, porque o
// corpo do toast fica visível no Action Center e na tela de bloqueio.
//
// O serviço nativo é o do próprio Wails v3
// (github.com/wailsapp/wails/v3/pkg/services/notifications), criado no main.go
// e inicializado por App.ServiceStartup; a notificação headless do serviço
// (automation/PSADT) permanece no caminho antigo (services/notifications +
// native_toast_*) e não é alterada.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wailsapp/wails/v3/pkg/application"
	wailsnotifications "github.com/wailsapp/wails/v3/pkg/services/notifications"

	"discovery/app/core/platform"
	"discovery/app/services/locale"
)

const (
	// chatNotificationID identifica a notificação de conclusão do chat. O mesmo
	// ID volta no callback de clique (OnNotificationResponse) e no payload de
	// ativação do wintoast quando o app é relançado pelo clique.
	chatNotificationID = "discovery.chat.response.ready"

	// chatNotificationPreviewLimit limita o preview em runas.
	chatNotificationPreviewLimit = 180

	// chatNotificationDebounce absorve eventos terminais duplicados/rajadas
	// muito próximas. A deduplicação principal é por AUSÊNCIA (ver
	// chatNotifyPending): enquanto o usuário não voltar a ver o chat, não
	// repetimos o aviso para cada resposta da fila.
	chatNotificationDebounce = 3 * time.Second

	// chatNotificationIconFile é o PNG (o mesmo do systray) usado como logo do
	// toast. Windows exige URI file:/// no XML do toast — caminho cru é ignorado.
	chatNotificationIconFile = "notification-icon.png"
)

// chatNotifyKind distingue sucesso de falha (textos diferentes).
type chatNotifyKind int

const (
	chatNotifyResponse chatNotifyKind = iota
	chatNotifyFailure
	// chatNotifyQuestion: o agente enviou uma pergunta (ask_user) e está
	// bloqueado esperando a resposta do usuário.
	chatNotifyQuestion
)

// chatWindowState é o estado da janela principal relevante para decidir se o
// usuário está vendo o chat.
type chatWindowState struct {
	Visible   bool
	Minimised bool
	Focused   bool
}

// shouldNotifyChatResponse decide se a conclusão de um turno deve notificar.
//
// Notifica quando a janela não está visível, está minimizada ou sem foco. Com a
// janela em primeiro plano, notifica apenas se a aba de chat NÃO é a view ativa
// (o usuário está vendo outra aba e o resultado passaria despercebido).
func shouldNotifyChatResponse(state chatWindowState, chatTabActive bool) bool {
	if !state.Visible || state.Minimised || !state.Focused {
		return true
	}
	return !chatTabActive
}

// SetNativeNotificationService guarda o serviço nativo do Wails v3 e registra o
// callback de clique: clicar no toast reabre a janela na aba de chat.
//
// O slot OnNotificationResponse do serviço é ÚNICO — registramos um dispatcher
// com fan-out (dispatchNativeNotification) em vez de um handler direto, para
// que outros recursos possam se registrar sem derrubar o nosso.
//
//wails:ignore
func (a *App) SetNativeNotificationService(ns *wailsnotifications.NotificationService) {
	if a == nil {
		return
	}
	a.nativeNotifications = ns
	if ns == nil {
		return
	}
	ns.OnNotificationResponse(a.dispatchNativeNotification)
	a.RegisterNativeNotificationHandler(func(result wailsnotifications.NotificationResult) {
		if result.Error != nil {
			return
		}
		if result.Response.ID != chatNotificationID {
			return
		}
		a.openChatFromNotification()
	})
}

// startNativeNotificationService inicializa o serviço nativo do Wails v3.
//
// O lifecycle é conduzido pelo App (e não registrando o serviço em
// application.Options.Services) por dois motivos:
//  1. registrar o serviço expõe seus métodos ao webview — e o webview renderiza
//     conteúdo gerado pela IA; a API de notificações não precisa ser chamável
//     de JS;
//  2. um erro em bindings.Add/ServiceStartup de um serviço registrado aborta o
//     Run() do Wails (fatal); aqui a falha apenas desabilita o toast do chat.
//
// O ServiceOptions usado é o mesmo que o Wails passaria (DefaultServiceOptions,
// vazio), então o comportamento do notifier é idêntico ao do registro normal.
func (a *App) startNativeNotificationService(ctx context.Context) {
	if a == nil || a.nativeNotifications == nil {
		return
	}
	if err := a.nativeNotifications.ServiceStartup(ctx, application.ServiceOptions{}); err != nil {
		log.Printf("[notifications] serviço nativo indisponível; notificação do chat desabilitada: %v", err)
		a.Logs.Append("[chat] notificação nativa do chat desabilitada: " + err.Error())
		return
	}
	a.nativeNotificationsStarted.Store(true)
	a.registerNotificationIcon()
}

// registerNotificationIcon materializa o PNG do logo e registra o IconUri do
// AppUserModelId (ícone da aplicação no toast/Action Center). Falha aqui não
// impede notificações: apenas não haverá logo.
func (a *App) registerNotificationIcon() {
	iconURL := a.ensureNotificationIcon()
	if iconURL == "" {
		return
	}
	appID := "Discovery"
	if a.app != nil {
		if name := strings.TrimSpace(a.app.Config().Name); name != "" {
			appID = name
		}
	}
	if err := registerNotificationAppIcon(appID, iconURL); err != nil {
		a.Logs.Append("[chat] aviso: não foi possível registrar o ícone do AppUserModelId: " + err.Error())
	}
}

// ensureNotificationIcon grava o PNG do logo no diretório de dados do agente
// (gravável em qualquer contexto de execução) e devolve o URI file:/// pronto
// para o XML do toast. Devolve "" quando não há bytes de ícone ou a gravação
// falha — o toast é enviado sem logo nesse caso.
func (a *App) ensureNotificationIcon() string {
	if a == nil || len(a.trayIcon) == 0 {
		return ""
	}
	dir := a.notificationIconDir
	if dir == "" {
		dir = platform.DataDir()
	}
	path := filepath.Join(dir, chatNotificationIconFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ""
	}
	// Reescreve quando ausente ou com tamanho diferente (update do binário
	// pode trocar o ícone).
	if data, err := os.ReadFile(path); err != nil || len(data) != len(a.trayIcon) {
		if err := os.WriteFile(path, a.trayIcon, 0o644); err != nil {
			a.Logs.Append("[chat] aviso: não foi possível gravar o ícone da notificação: " + err.Error())
			return ""
		}
	}
	// Windows aceita apenas URI no src do toast; caminho cru (C:...) é ignorado.
	return "file:///" + filepath.ToSlash(path)
}

// stopNativeNotificationService encerra o serviço nativo (idempotente).
func (a *App) stopNativeNotificationService() {
	if a == nil || a.nativeNotifications == nil || !a.nativeNotificationsStarted.Swap(false) {
		return
	}
	if err := a.nativeNotifications.ServiceShutdown(); err != nil {
		log.Printf("[notifications] falha ao encerrar o serviço nativo: %v", err)
	}
}

// RegisterNativeNotificationHandler adiciona um consumidor de respostas de
// notificações nativas. Use SEMPRE este caminho: o serviço do Wails aceita um
// único callback e ele já está ocupado pelo dispatcher.
//
//wails:ignore
func (a *App) RegisterNativeNotificationHandler(handler func(wailsnotifications.NotificationResult)) {
	if a == nil || handler == nil {
		return
	}
	a.nativeNotifyHandlersMu.Lock()
	a.nativeNotifyHandlers = append(a.nativeNotifyHandlers, handler)
	a.nativeNotifyHandlersMu.Unlock()
}

// dispatchNativeNotification entrega a resposta para todos os handlers
// registrados (cópia da lista fora do lock).
func (a *App) dispatchNativeNotification(result wailsnotifications.NotificationResult) {
	if a == nil {
		return
	}
	a.nativeNotifyHandlersMu.Lock()
	handlers := make([]func(wailsnotifications.NotificationResult), len(a.nativeNotifyHandlers))
	copy(handlers, a.nativeNotifyHandlers)
	a.nativeNotifyHandlersMu.Unlock()
	for _, handler := range handlers {
		handler(result)
	}
}

// SetChatTabActive informa ao backend se a aba de chat é a view ativa em tela.
// É o único estado que o frontend pode reportar; o restante (foco, visibilidade,
// minimizado) é lido direto da janela do Wails.
//
// A primeira chamada também sinaliza "frontend pronto": é o gatilho para focar
// a aba de chat quando o processo foi aberto por um clique em toast.
func (a *App) SetChatTabActive(active bool) {
	if a == nil {
		return
	}
	a.chatTabActive.Store(active)
	if active {
		// O usuário está (ou esteve) com o chat em tela: encerra a ausência.
		a.clearChatNotifyAbsence()
	}
	a.uiReady.Store(true)
	if a.pendingChatFocus.Swap(false) {
		a.openChatFromNotification()
	}
}

// clearChatNotifyAbsence zera a marca de "já avisei nesta ausência".
func (a *App) clearChatNotifyAbsence() {
	a.chatNotifyMu.Lock()
	a.chatNotifyPending = false
	a.chatNotifyLastAt = time.Time{}
	a.chatNotifyMu.Unlock()
}

// notifyChatResponseComplete é o hook chamado pelo chat.Service quando a LLM
// conclui a resposta. Roda em goroutine própria para não atrasar a liberação do
// turno (o envio do toast pode bloquear no fallback do Windows).
func (a *App) notifyChatResponseComplete(content string) {
	if a == nil || a.nativeNotifications == nil {
		return
	}
	a.safeGo(func() {
		a.sendChatNotification(chatNotifyResponse, content)
	})
}

// notifyChatResponseFailed é o hook chamado quando o turno termina em erro real
// durante o processamento (rejeições instantâneas não notificam).
func (a *App) notifyChatResponseFailed(errMsg string) {
	if a == nil || a.nativeNotifications == nil {
		return
	}
	a.safeGo(func() {
		a.sendChatNotification(chatNotifyFailure, errMsg)
	})
}

// notifyChatQuestion é chamado quando o agente envia uma pergunta interativa
// (ask_user) e fica bloqueado esperando a resposta: sem aviso, o usuário longe
// nunca descobre que o turno parou esperando por ele.
func (a *App) notifyChatQuestion(question string) {
	if a == nil || a.nativeNotifications == nil {
		return
	}
	a.safeGo(func() {
		a.sendChatNotification(chatNotifyQuestion, question)
	})
}

// sendChatNotification lê o estado real (serviço, janela, preferência) e delega
// a decisão/envio para deliverChatNotification.
func (a *App) sendChatNotification(kind chatNotifyKind, content string) {
	if a == nil || a.nativeNotifications == nil {
		return
	}
	// Serviço nativo não inicializou (registro do toast falhou): não tenta
	// enviar por um notifier meio-inicializado.
	if !a.nativeNotificationsStarted.Load() {
		return
	}
	// Modo serviço não tem UI local de chat.
	if a.RuntimeFlags.ServiceMode {
		return
	}
	// Shutdown em andamento: não faz sentido notificar.
	if a.QuitRequested.Load() {
		return
	}
	if a.mainWindow == nil {
		// Sem janela não há como saber o que está em tela; não notifica.
		return
	}
	state := chatWindowState{
		Visible:   a.mainWindow.IsVisible(),
		Minimised: a.mainWindow.IsMinimised(),
		Focused:   a.mainWindow.IsFocused(),
	}
	// Falha nunca carrega o texto bruto do erro: o detalhe fica no chat. A
	// pergunta respeita a mesma preferência de privacidade da resposta.
	includePreview := (kind == chatNotifyResponse || kind == chatNotifyQuestion) && a.chatPreviewEnabled()
	a.deliverChatNotification(kind, content, state, a.chatTabActive.Load(), includePreview, a.nativeNotifications.SendNotification)
}

// chatPreviewEnabled lê a preferência de privacidade (default true).
func (a *App) chatPreviewEnabled() bool {
	if a == nil || a.chatSvc == nil {
		return true
	}
	return a.chatSvc.NotifyPreviewEnabled()
}

// deliverChatNotification aplica decisão + deduplicação + envio e devolve true
// quando o toast foi entregue.
//
// O sender é injetado para permitir teste com fake: application.Window (estado
// real) não é mockável, então o caminho completo é exercitado por aqui.
func (a *App) deliverChatNotification(
	kind chatNotifyKind,
	content string,
	state chatWindowState,
	chatTabActive bool,
	includePreview bool,
	send func(wailsnotifications.NotificationOptions) error,
) bool {
	if a == nil || send == nil {
		return false
	}
	if !shouldNotifyChatResponse(state, chatTabActive) {
		// Usuário com o chat em tela: a ausência terminou e a próxima poderá
		// notificar de imediato.
		a.clearChatNotifyAbsence()
		return false
	}

	a.chatNotifyMu.Lock()
	// Pergunta é um evento acionável (o agente está travado esperando o
	// usuário): não é suprimida pela ausência já avisada — só pelo debounce.
	if kind != chatNotifyQuestion && a.chatNotifyPending {
		// Já avisamos nesta ausência (ex.: fila de mensagens despachada em
		// sequência) — um aviso basta para o usuário voltar ao chat.
		a.chatNotifyMu.Unlock()
		return false
	}
	if !a.chatNotifyLastAt.IsZero() && time.Since(a.chatNotifyLastAt) < chatNotificationDebounce {
		a.chatNotifyMu.Unlock()
		return false
	}
	// A pergunta não consome a ausência: a resposta final ainda deve avisar.
	if kind != chatNotifyQuestion {
		a.chatNotifyPending = true
	}
	a.chatNotifyLastAt = time.Now()
	a.chatNotifyMu.Unlock()

	title, body := chatNotificationText(kind, content, locale.DetectPreferredLocale(), includePreview)
	dataKind := "chat.response"
	switch kind {
	case chatNotifyFailure:
		dataKind = "chat.failure"
	case chatNotifyQuestion:
		dataKind = "chat.question"
	}
	opts := wailsnotifications.NotificationOptions{
		ID:    chatNotificationID,
		Title: title,
		Body:  body,
		Data:  map[string]interface{}{"kind": dataKind},
		// ThreadID NÃO é usado de propósito: no backend Windows do Wails ele
		// vira <header title="<ThreadID>">, que o Windows renderiza como um
		// caption visível ("discovery.chat") — e o header gerado não preenche o
		// atributo obrigatório `arguments`, o que pode invalidar o toast.
	}
	if iconURL := a.ensureNotificationIcon(); iconURL != "" {
		// Logo da aplicação no próprio toast (não depende do AppUserModelId nem
		// do caminho COM/PowerShell do envio).
		opts.Attachments = []wailsnotifications.NotificationAttachment{
			{Path: iconURL, Type: "appLogoOverride"},
		}
	}
	if err := send(opts); err != nil {
		// Desfaz a marca para permitir nova tentativa depois do debounce.
		a.chatNotifyMu.Lock()
		a.chatNotifyPending = false
		a.chatNotifyMu.Unlock()
		// O wintoast devolve erro do caminho COM mesmo quando o fallback do
		// PowerShell exibiu o toast (errors.Join(comErr, nil)): nesse caminho o
		// toast sai sem logo e sem callback de clique.
		a.Logs.Append("[chat] aviso: envio nativo retornou erro (toast pode ter saído pelo fallback do PowerShell): " + err.Error())
		return false
	}
	return true
}

// openChatFromNotification reabre a janela principal e foca a aba de chat.
// O backend não conhece a view ativa, então executa o helper exposto pelo
// frontend (window.__discoveryFocusChatView em app-chat.js).
func (a *App) openChatFromNotification() {
	if a == nil {
		return
	}
	a.ShowMainWindow()
	if a.mainWindow != nil {
		a.mainWindow.ExecJS("window.__discoveryFocusChatView && window.__discoveryFocusChatView();")
	}
}

// chatActivationPayload espelha o payload do wintoast (NotificationPayload do
// pacote de notificações): o campo Go é Options, mas a chave JSON é "payload".
type chatActivationPayload struct {
	Action  string `json:"action"`
	Options struct {
		ID string `json:"id"`
	} `json:"payload"`
}

// HandleActivationArgs inspeciona os argumentos de linha de comando.
//
// Clique num toast com o app FECHADO relança o executável com o payload base64
// do toast (ActivationExe). Nesse caso a janela deve abrir direto na aba de
// chat — antes ela abria na última aba usada.
//
//wails:ignore
func (a *App) HandleActivationArgs(args []string) {
	if a == nil || !chatActivationArgsMatch(args) {
		return
	}
	if a.uiReady.Load() {
		// App já estava rodando (segunda instância): foca imediatamente.
		a.openChatFromNotification()
		return
	}
	// Boot ainda em andamento: o primeiro SetChatTabActive (frontend pronto)
	// dispara o foco.
	a.pendingChatFocus.Store(true)
}

// chatActivationArgsMatch procura, entre os argumentos, o payload base64 do
// toast de conclusão do chat. Argumentos que não decodificam são ignorados.
func chatActivationArgsMatch(args []string) bool {
	for _, raw := range args {
		arg := strings.TrimSpace(raw)
		if len(arg) < 8 {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(arg)
		if err != nil {
			continue
		}
		var payload chatActivationPayload
		if err := json.Unmarshal(decoded, &payload); err != nil {
			continue
		}
		if payload.Options.ID == chatNotificationID {
			return true
		}
	}
	return false
}

// chatNotificationText monta título/corpo localizados do toast. Em falha o
// corpo é sempre genérico (sem o texto do erro); em sucesso e em pergunta o
// preview depende da preferência de privacidade.
func chatNotificationText(kind chatNotifyKind, content, lang string, includePreview bool) (string, string) {
	english := strings.EqualFold(strings.TrimSpace(lang), "en-US")
	if kind == chatNotifyFailure {
		if english {
			return "AI Chat error", "The AI could not finish the answer. Open the chat to see the details."
		}
		return "Falha no Chat IA", "A IA não conseguiu concluir a resposta. Abra o chat para ver os detalhes."
	}
	if kind == chatNotifyQuestion {
		preview := ""
		if includePreview {
			preview = chatResponsePreview(content)
		}
		if english {
			if preview == "" {
				preview = "The AI Chat asked you a question and is waiting for your answer."
			}
			return "The agent is waiting for your answer", preview
		}
		if preview == "" {
			preview = "O Chat IA fez uma pergunta e está aguardando sua resposta."
		}
		return "O agente aguarda sua resposta", preview
	}

	preview := ""
	if includePreview {
		preview = chatResponsePreview(content)
	}
	if english {
		if preview == "" {
			preview = "The AI finished answering. Open the chat to see the response."
		}
		return "New reply in AI Chat", preview
	}
	if preview == "" {
		preview = "A IA concluiu a resposta. Abra o chat para ver o resultado."
	}
	return "Nova resposta no Chat IA", preview
}

// Expressões de limpeza do preview. O corpo do toast é texto plano — o Windows
// não renderiza markdown — então marcadores como ** apareciam literalmente.
var (
	chatPreviewImageRe    = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	chatPreviewLinkRe     = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	chatPreviewCodeRe     = regexp.MustCompile("`([^`]*)`")
	chatPreviewHTMLRe     = regexp.MustCompile(`<[^>]*>`)
	chatPreviewStrongRe   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	chatPreviewUnderRe    = regexp.MustCompile(`__([^_]+)__`)
	chatPreviewStrikeRe   = regexp.MustCompile(`~~([^~]+)~~`)
	chatPreviewEmphRe     = regexp.MustCompile(`\*([^*\s][^*]*)\*`)
	chatPreviewEmphLowRe  = regexp.MustCompile(`(^|\s)_([^_\s][^_]*)_(\s|$|[.,;:!?)])`)
	chatPreviewOrderedRe  = regexp.MustCompile(`^\d{1,3}[.)]\s+`)
	chatPreviewFenceRe    = regexp.MustCompile("^\\s*(```|~~~)")
	chatPreviewRuleRe     = regexp.MustCompile(`^\s*([-*_=]\s*){3,}$`)
	chatPreviewTableSepRe = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	chatPreviewTableRowRe = regexp.MustCompile(`^\s*\|`)
)

// chatResponsePreview extrai um resumo de uma linha do texto final, já em texto
// plano: ignora blocos de código e separadores de tabela/regra, remove
// marcadores de bloco e inline e normaliza espaços.
func chatResponsePreview(content string) string {
	text := strings.ReplaceAll(content, "\r\n", "\n")
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		if chatPreviewFenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence ||
			chatPreviewRuleRe.MatchString(line) ||
			chatPreviewTableSepRe.MatchString(line) ||
			chatPreviewTableRowRe.MatchString(line) {
			continue
		}
		clean := chatPreviewPlainLine(line)
		if clean == "" {
			continue
		}
		return truncateRunes(clean, chatNotificationPreviewLimit)
	}
	return ""
}

// chatPreviewPlainLine converte uma linha markdown em texto plano para o toast:
// "**não há erro**" vira "não há erro" — era exatamente o caso visto em
// produção, com os asteriscos aparecendo no corpo da notificação.
func chatPreviewPlainLine(line string) string {
	clean := strings.TrimSpace(line)
	// Marcadores de bloco: títulos (#), citações (>), numeração e listas.
	clean = strings.TrimSpace(strings.TrimLeft(clean, "#>"))
	clean = chatPreviewOrderedRe.ReplaceAllString(clean, "")
	// Bullet apenas quando o marcador é seguido de espaço: "*itálico*" no
	// início da linha não é item de lista.
	if rest := strings.TrimLeft(clean, "-*+•"); rest != clean && (strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t")) {
		clean = strings.TrimSpace(rest)
	}
	// Elementos inline.
	clean = chatPreviewImageRe.ReplaceAllString(clean, "$1")
	clean = chatPreviewLinkRe.ReplaceAllString(clean, "$1")
	clean = chatPreviewCodeRe.ReplaceAllString(clean, "$1")
	clean = chatPreviewHTMLRe.ReplaceAllString(clean, "")
	clean = chatPreviewStrongRe.ReplaceAllString(clean, "$1")
	clean = chatPreviewUnderRe.ReplaceAllString(clean, "$1")
	clean = chatPreviewStrikeRe.ReplaceAllString(clean, "$1")
	clean = chatPreviewEmphRe.ReplaceAllString(clean, "$1")
	clean = chatPreviewEmphLowRe.ReplaceAllString(clean, "${1}${2}${3}")
	// Marcadores órfãos (ex.: negrito não fechado no fim do texto).
	clean = strings.ReplaceAll(clean, "**", "")
	clean = strings.Join(strings.Fields(clean), " ")
	return strings.TrimSpace(clean)
}

// truncateRunes corta uma string em max runas (sem quebrar UTF-8).
func truncateRunes(value string, max int) string {
	if max <= 0 || utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:max])) + "…"
}
