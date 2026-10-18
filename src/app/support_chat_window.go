package app

import (
	"context"
	"encoding/base64"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	screencap "discovery/app/core/screen"
	"discovery/app/supportchat"
)

// ── Janela independente do chat de suporte ──────────────────────────────────
//
// A janela é criada sob demanda (ao aceitar o convite ou ao retomar uma sessão
// ativa no startup) e é INDEPENDENTE da interface principal do agent. Fechá-la
// encerra o chat — mas só após a confirmação do usuário (modal na própria
// janela), porque o requisito é que ela só feche quando o chat terminar.
//
// Ela é um PAINEL FIXO: as opções de criação (supportChatWindowOptions) tiram o
// botão de fechar e o maximizar e travam o tamanho. Fechamento por atalho do
// sistema (Alt+F4) continua caindo no hook WindowClosing abaixo.

const (
	supportChatWindowName = "support-chat"
	supportChatCallWait   = 30 * time.Second
	// Tamanho FIXO do painel do chat (ver supportChatWindowOptions).
	supportChatWindowWidth  = 520
	supportChatWindowHeight = 680
)

// supportChatShouldFloat informa se a janela do chat deve ficar SEMPRE NO TOPO
// (flutuando acima dos outros aplicativos).
//
// REQUISITO (definido pelo usuário): o convite do suporte precisa aparecer por
// cima de tudo. Antes a janela abria atrás do app em uso — "só ficava o ícone na
// barra de tarefas" — e o usuário não via o pedido de aceite.
//
// O flutuante vale SOMENTE enquanto o convite está pendente de resposta
// ("waiting"). Conversa ativa volta ao comportamento normal: uma janela sempre
// no topo durante todo o atendimento atrapalharia o trabalho do usuário (ela
// cobriria o app que ele está usando e que o suporte precisa ver).
func supportChatShouldFloat(status string) bool {
	return strings.TrimSpace(status) == supportchat.StatusWaiting
}

// supportChatWindowOptions monta as opções da janela do chat.
//
// A janela é um PAINEL, não uma janela comum:
//
//   - SEM botão de fechar: fechar a janela é ENCERRAR a conversa, e o único
//     caminho para isso é o botão "Encerrar" (que pede confirmação e fecha a
//     janela). Antes existiam dois caminhos — o X e o botão — e o X fechava a
//     conversa sem o usuário ver o botão equivalente;
//   - SEM maximizar e SEM redimensionar: tamanho travado em width x height, para
//     o chat não tomar a tela inteira enquanto o usuário trabalha;
//   - MaxWidth/MaxHeight iguais ao tamanho inicial: nenhum caminho (duplo clique
//     no título, atalho do sistema) pode ampliá-la além disso.
func supportChatWindowOptions(width, height int) application.WebviewWindowOptions {
	return application.WebviewWindowOptions{
		Name:   supportChatWindowName,
		Title:  "Chat com o suporte",
		Width:  width,
		Height: height,

		MinWidth:  width,
		MinHeight: height,
		MaxWidth:  width,
		MaxHeight: height,

		DisableResize:         true,
		CloseButtonState:      application.ButtonHidden,
		MaximiseButtonState:   application.ButtonDisabled,
		FullscreenButtonState: application.ButtonDisabled,

		URL: "/chat-window.html",
	}
}

// supportChatWindowSize devolve o tamanho do painel já limitado à área de
// trabalho da tela (em DIP), preservando a proporção desejada.
//
// Sem este limite a janela FIXA ficava maior que a área visível em tela pequena
// ou com escala de DPI alta (ex.: 1080p a 150% → ~1280x688 lógicos): como ela
// não pode ser redimensionada nem maximizada, o rodapé (campo de texto e
// "Enviar") ficava fora da tela e o usuário não tinha como alcançá-lo.
func supportChatWindowSize(screen *application.Screen) (int, int) {
	width, height := supportChatWindowWidth, supportChatWindowHeight
	if screen == nil {
		return width, height
	}

	work := screen.WorkArea
	if work.Width <= 0 || work.Height <= 0 {
		return width, height
	}

	if maxWidth := work.Width - 2*windowFitMargin; maxWidth > 0 && width > maxWidth {
		width = maxWidth
	}
	if maxHeight := work.Height - 2*windowFitMargin; maxHeight > 0 && height > maxHeight {
		height = maxHeight
	}
	return width, height
}

// screenContainingPhysicalPoint devolve a tela cujo PhysicalBounds contém o
// ponto FÍSICO informado (coordenadas de GetCursorPos).
//
// Telas sem PhysicalBounds (plataformas que não preenchem) caem no Bounds, que
// no Windows continua em DIP — é um palpite razoável e nunca piora o
// comportamento anterior (que ignorava o cursor).
func screenContainingPhysicalPoint(screens []*application.Screen, pt application.Point) *application.Screen {
	for _, s := range screens {
		if s == nil {
			continue
		}
		bounds := s.PhysicalBounds
		if bounds.Width <= 0 || bounds.Height <= 0 {
			bounds = s.Bounds
		}
		if bounds.Width <= 0 || bounds.Height <= 0 {
			continue
		}
		if pt.X >= bounds.X && pt.X < bounds.X+bounds.Width &&
			pt.Y >= bounds.Y && pt.Y < bounds.Y+bounds.Height {
			return s
		}
	}
	return nil
}

// supportChatPlacementScreen decide em qual monitor a janela do chat vai nascer.
//
// Ordem de preferência:
//
//  1. monitor onde está o CURSOR — é onde o usuário está trabalhando;
//  2. monitor da janela principal (preferred), quando o cursor não resolve;
//  3. monitor primário;
//  4. a primeira tela conhecida.
//
// BUG RELATADO: com dois monitores o convite podia nascer no monitor em que o
// usuário não estava olhando e passar despercebido — junto com o "não aparece
// por cima dos outros apps", isso dava a impressão de convite perdido.
func supportChatPlacementScreen(screens []*application.Screen, cursor *application.Point, preferred *application.Screen) *application.Screen {
	if cursor != nil {
		if s := screenContainingPhysicalPoint(screens, *cursor); s != nil {
			return s
		}
	}
	if preferred != nil {
		return preferred
	}
	for _, s := range screens {
		if s != nil && s.IsPrimary {
			return s
		}
	}
	for _, s := range screens {
		if s != nil {
			return s
		}
	}
	return nil
}

// supportChatWindowOptionsOn monta as opções da janela já posicionadas no
// monitor escolhido e com o "sempre no topo" aplicado desde a criação.
//
// O AlwaysOnTop nas opções (e não só depois de exibir) evita o intervalo em que
// a janela nasce atrás do app em uso — era justamente esse piscar/pular para
// trás que confundia o usuário no convite.
func supportChatWindowOptionsOn(width, height int, screen *application.Screen, float bool) application.WebviewWindowOptions {
	opts := supportChatWindowOptions(width, height)
	opts.AlwaysOnTop = float

	if screen == nil {
		return opts
	}
	work := screen.WorkArea
	if work.Width <= 0 || work.Height <= 0 {
		return opts
	}

	// X/Y são relativos à origem da WorkArea da tela escolhida (contrato do
	// Wails v3) e centralizam o painel nela.
	x := (work.Width - width) / 2
	y := (work.Height - height) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	opts.Screen = screen
	opts.InitialPosition = application.WindowXY
	opts.X = x
	opts.Y = y
	return opts
}

// supportChatPlacementScreen resolve a tela do painel do chat (cursor → janela
// principal → primária) a partir dos dados do Wails.
func (a *App) supportChatPlacementScreen(wailsApp *application.App) *application.Screen {
	var screens []*application.Screen
	if wailsApp != nil && wailsApp.Screen != nil {
		screens = wailsApp.Screen.GetAll()
	}

	// Cursor: coordenadas FÍSICAS do desktop virtual (multi-monitor).
	var cursor *application.Point
	if info, err := screencap.GetCursorPos(); err == nil {
		cursor = &application.Point{X: int(info.X), Y: int(info.Y)}
	}

	var preferred *application.Screen
	if a.mainWindow != nil {
		if s, err := a.mainWindow.GetScreen(); err == nil && s != nil {
			preferred = s
		}
	}

	return supportChatPlacementScreen(screens, cursor, preferred)
}

// supportChatWindowFloat devolve o estado "sempre no topo" que a janela deve ter
// AGORA, lido do serviço do chat.
func (a *App) supportChatWindowFloat() bool {
	return supportChatShouldFloat(a.supportChatService().State().Status)
}

// supportChatWindowAlive informa se a janela informada ainda é a janela corrente
// do chat (usado pelas re-tentativas de trazer para a frente).
func (a *App) supportChatWindowAlive(win application.Window) bool {
	a.supportChatMu.Lock()
	defer a.supportChatMu.Unlock()
	return win != nil && a.supportChatWin == win
}

// raiseSupportChatWindow exibe a janela do chat e a traz para a frente, com
// re-tentativas (ver raiseWindowToFront/scheduleWindowRaise).
//
// O estado "sempre no topo" é decidido pelo estado do convite: pendente = true,
// conversa ativa/encerrada = false.
func (a *App) raiseSupportChatWindow(win application.Window, float bool) {
	if win == nil {
		return
	}

	raiseWindowToFront(win, float)
	scheduleWindowRaise(win, float, func() bool { return a.supportChatWindowAlive(win) }, a.supportChatContext().Done())
}

// applySupportChatWindowMode ajusta a janela aberta ao estado da conversa
// (sempre no topo apenas enquanto o convite está pendente).
//
// Chamado em toda transição de estado do serviço do chat: aceite, recusa,
// encerramento pelo suporte ou expiração do convite devolvem a janela ao
// comportamento normal — antes ela poderia ficar flutuando para sempre.
func (a *App) applySupportChatWindowMode(status string) {
	if a == nil {
		return
	}

	a.supportChatMu.Lock()
	win := a.supportChatWin
	a.supportChatMu.Unlock()

	if win == nil {
		return
	}

	if supportChatShouldFloat(status) {
		win.SetAlwaysOnTop(true)
		return
	}

	win.SetAlwaysOnTop(false)
	stopWindowFlash(win)
}

// supportChatStateEventName é o nome do evento de estado do chat (o mesmo
// consumido pela janela do chat e repassado por IPC no modo serviço).
const supportChatStateEventName = "supportchat:state"

// syncSupportChatWindowFromEvent aplica o modo da janela (sempre no topo) quando
// o serviço emite uma transição de estado do chat.
//
// Roda em goroutine porque as operações de janela do Wails são InvokeSync: uma
// emissão que acontecesse na thread principal travaria a aplicação.
func (a *App) syncSupportChatWindowFromEvent(name string, data ...any) {
	if a == nil || name != supportChatStateEventName || len(data) == 0 {
		return
	}

	state, ok := data[0].(supportchat.State)
	if !ok {
		return
	}

	go a.applySupportChatWindowMode(state.Status)
}

// syncSupportChatWindowFromIPCPayload lê o status do payload IPC de
// supportchat:state (mapa JSON) e aplica o modo da janela.
//
// O payload chega em uma das duas formas documentadas em ipc_app_integration.go:
// o estado cru (quando o evento é emitido com UM argumento) ou espalhado nos
// campos do payload. As duas são aceitas porque o serviço pode evoluir e o
// contrato de campos extras é tolerante.
func (a *App) syncSupportChatWindowFromIPCPayload(payload map[string]any) {
	if a == nil || payload == nil {
		return
	}

	source := payload
	if raw, ok := payload[ipcEventRawKey].(map[string]any); ok {
		source = raw
	}

	status, _ := source["status"].(string)
	if strings.TrimSpace(status) == "" {
		return
	}

	go a.applySupportChatWindowMode(status)
}

// supportChatInviteFromIPCPayload extrai os campos do convite de um payload IPC.
//
// O payload chega das duas formas documentadas em ipc_app_integration.go: com os
// campos do evento espalhados no próprio payload ({name, sessionId, ...}) ou com
// o valor cru sob ipcEventRawKey. Devolve ok=false quando não há sessionId — é a
// única chave obrigatória do convite (conversa avulsa não tem ticketId).
func supportChatInviteFromIPCPayload(payload map[string]any) (sessionID, ticketID, ticketTitle, supportName string, ok bool) {
	if payload == nil {
		return "", "", "", "", false
	}

	source := payload
	if raw, isMap := payload[ipcEventRawKey].(map[string]any); isMap {
		source = raw
	}

	sessionID, _ = source["sessionId"].(string)
	if strings.TrimSpace(sessionID) == "" {
		return "", "", "", "", false
	}
	ticketID, _ = source["ticketId"].(string)
	ticketTitle, _ = source["ticketTitle"].(string)
	supportName, _ = source["supportName"].(string)
	return sessionID, ticketID, ticketTitle, supportName, true
}

// openSupportChatInviteFromIPC abre a janela do convite a partir do evento IPC
// recebido do serviço (modo companion).
//
// A validação do convite (sessão ainda pendente/ativa no servidor) e o
// PrepareInvite continuam em SupportChatOpenInviteWindow — aqui só existe o
// desempacotamento do payload IPC.
func (a *App) openSupportChatInviteFromIPC(payload map[string]any) {
	if a == nil || a.app == nil || payload == nil {
		return
	}

	sessionID, ticketID, ticketTitle, supportName, ok := supportChatInviteFromIPCPayload(payload)
	if !ok {
		a.Logs.Append("[supportchat] convite via IPC sem sessionId; ignorado")
		return
	}

	go func() {
		if err := a.SupportChatOpenInviteWindow(sessionID, ticketID, ticketTitle, supportName); err != nil {
			// O handler de JS da janela principal ainda pode abrir (é o caminho
			// antigo, mantido); se nem ele existir, a reconciliação de 60s cobre.
			a.Logs.Append("[supportchat] convite via IPC nao abriu a janela: " + err.Error())
			return
		}
		a.Logs.Append("[supportchat] janela do convite aberta pelo evento IPC (sessao " + sessionID + ")")
	}()
}

// openSupportChatWindow abre (ou foca) a janela do chat.
//
// Não é exportado de propósito: a abertura da janela é decidida pelo Go
// (aceite do convite / retomada no startup) e não deve ser acionável pelo
// webview.
//
// BUG RELATADO: o convite "não abria por cima dos outros aplicativos — só
// ficava o ícone na barra de tarefas". Show()+Focus() não vencem o foreground
// lock do Windows; agora a janela nasce no monitor em que o usuário está
// (cursor), já com "sempre no topo" quando o convite está pendente e é forçada
// ao primeiro plano (ver window_foreground*.go).
func (a *App) openSupportChatWindow() {
	app := a.app
	if app == nil {
		a.Logs.Append("[supportchat] aplicação Wails indisponível; janela do chat não pode ser aberta")
		return
	}

	float := a.supportChatWindowFloat()

	a.supportChatMu.Lock()
	if a.supportChatWin != nil {
		win := a.supportChatWin
		a.supportChatMu.Unlock()
		a.raiseSupportChatWindow(win, float)
		return
	}

	// Tela onde a janela vai nascer: a do cursor (onde o usuário está
	// trabalhando), depois a da janela principal e, por fim, a primária.
	screen := a.supportChatPlacementScreen(app)
	width, height := supportChatWindowSize(screen)

	win := app.Window.NewWithOptions(supportChatWindowOptionsOn(width, height, screen, float))
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

	a.raiseSupportChatWindow(win, float)
	a.Logs.Append("[supportchat] janela do chat aberta (sempre no topo=" + boolLabel(float) + ")")
}

// boolLabel formata um booleano para o log do agent.
func boolLabel(value bool) string {
	if value {
		return "sim"
	}
	return "nao"
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
	stopWindowFlash(win)
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
		// Janela já aberta: o snapshot REST também serve para corrigir o
		// "sempre no topo" (convite pendente = flutua; ativo = normal). Sem isso
		// um estado perdido no NATS deixava a janela flutuando durante toda a
		// conversa — ou um convite pendente sem a proteção do topo.
		a.applySupportChatWindowMode(state.Status)
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
			a.reconcileSupportChat()
		}
	}
}

// supportChatActiveReconcileEvery é de quantos em quantos ticks a reconciliação
// consulta o REST quando há conversa ATIVA com a janela aberta (60s * 5 = 5 min).
const supportChatActiveReconcileEvery = 5

// reconcileSupportChat é o tick periódico (ver runSupportChatReconcileLoop).
//
// Custo x benefício: o snapshot REST é 2 requisições por tick, por agent — e com
// conversa ATIVA e janela aberta o NATS entrega em tempo real, a reconexão força
// um refresh completo (OnNATSConnected → resumeSupportChatIfActive) e a própria
// janela faz refresh nas ações do usuário. Então ali a consulta periódica só
// precisa detectar EXPIRAÇÃO SILENCIOSA no servidor (sessão que morreu sem
// evento): 1 tick a cada 5 mantém essa rede de segurança sem pagar o custo todo.
//
// Fora desse caso (convite pendente, janela fechada, retomada) a consulta
// continua a cada tick — é o cenário em que o convite se perde no canal unicast.
func (a *App) reconcileSupportChat() {
	windowOpen := a.supportChatWindowOpen()
	status := a.supportChatService().State().Status
	tick := a.supportChatReconcileTicks.Add(1)

	if !supportChatReconcileNeeded(windowOpen, status, tick) {
		return
	}

	a.resumeSupportChatIfActive(0)
}

// supportChatReconcileNeeded decide se o tick periódico deve consultar o REST.
//
// Puro para poder ser testado sem App/janela (ver support_chat_window_test.go).
func supportChatReconcileNeeded(windowOpen bool, status string, tick int64) bool {
	if !windowOpen {
		return true
	}
	if status != supportchat.StatusActive {
		return true
	}
	return tick%supportChatActiveReconcileEvery == 0
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

	// A janela do convite SEMPRE nasce flutuando e em primeiro plano: é o pedido
	// de aceite do usuário, não pode passar despercebido atrás de outro app.
	a.openSupportChatWindow()
	a.Logs.Append("[supportchat] janela do convite aberta em primeiro plano (sessao " + sessionID + ")")
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

	// O convite foi respondido: a janela deixa de ser "sempre no topo". No aceite
	// o estado do serviço já volta para active (applySupportChatWindowMode é
	// chamado na emissão do estado), mas a recusa FECHA a janela em seguida e o
	// flutuante/pisca precisa ser desligado antes.
	a.applySupportChatWindowMode(supportchat.StatusNone)

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
