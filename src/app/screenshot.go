package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"discovery/app/core/platform"
	"discovery/app/core/screen"
	"discovery/app/core/screenshot"
	applocale "discovery/app/services/locale"
)

// ── Captura de tela assistida (chat IA) ───────────────────────────────────
//
// Liga o pacote core/screenshot ao app: consentimento, bindings do overlay de
// seleção, tools MCP e trilha de auditoria.

const (
	// screenshot_consent.json é legado ("permitir sempre") e é IGNORADO: o
	// consentimento passou a ser por captura (ver core/screenshot/consent.go).
	screenshotPolicyFile = "screenshot_policy.json"
	// screenshotAuditThumbLimit limita quantas miniaturas a UI de privacidade
	// recebe de uma vez (payload do binding).
	screenshotAuditThumbLimit = 12
)

// Destinos do print capturado no overlay. O overlay roda numa janela
// DEDICADA (screenshot_overlay_window.go), então o resultado precisa voltar
// para a janela de origem por evento — o destino diz para onde.
const (
	// screenshotDestinationAIChat é o composer do chat com a IA (padrão).
	screenshotDestinationAIChat = "aiChat"
	// screenshotDestinationSupportChat é a janela do chat de suporte.
	screenshotDestinationSupportChat = "supportChat"
	// screenshotDestinationTicketComment é o composer de comentário do chamado.
	screenshotDestinationTicketComment = "ticketComment"
	// screenshotDestinationLLM é a seleção interativa pedida pela IA.
	screenshotDestinationLLM = "llm"
)

// normalizeScreenshotDestination valida o destino vindo do frontend.
func normalizeScreenshotDestination(destination string) string {
	switch strings.TrimSpace(destination) {
	case screenshotDestinationSupportChat:
		return screenshotDestinationSupportChat
	case screenshotDestinationTicketComment:
		return screenshotDestinationTicketComment
	case screenshotDestinationLLM:
		return screenshotDestinationLLM
	default:
		return screenshotDestinationAIChat
	}
}

// screenshotOverlaySession é a sessão ativa do overlay de captura.
type screenshotOverlaySession struct {
	id          string
	reason      string
	byLLM       bool
	destination string
	payload     *screenshot.OverlayPayload
	// frame é o print do desktop em resolução física cheia, usado para recortar
	// exatamente o que o usuário viu (tela congelada).
	frame   *screen.Frame
	restore func()
	// sessionAuthorized registra que a captura foi coberta pela autorização de
	// sessão (auditoria da captura interativa pedida pela IA).
	sessionAuthorized bool
	// waiter recebe o resultado quando a captura interativa foi pedida pela IA.
	waiter chan screenshotOverlayOutcome
	// once garante que release/entrega aconteçam uma única vez.
	once sync.Once
}

type screenshotOverlayOutcome struct {
	result *screenshot.CaptureResult
	err    error
}

// initScreenshotService cria (idempotente) o gerenciador de consentimento.
func (a *App) initScreenshotService() {
	a.screenshotMu.Lock()
	defer a.screenshotMu.Unlock()
	if a.screenshotConsent != nil {
		return
	}
	mgr := screenshot.NewConsentManager(
		func(ctx context.Context, question string, options []string) (string, error) {
			opts, _ := json.Marshal(options)
			return a.AskUserChatWithContext(ctx, question, string(opts), "false")
		},
		func(line string) { a.Logs.Append(line) },
		applocale.DetectPreferredLocale,
	)
	// Reconsulta da autorização de sessão SOB o promptMu do manager: duas
	// capturas concorrentes não podem fazer o usuário responder "permitir sempre
	// nesta sessão" na primeira e receber uma segunda pergunta logo depois.
	// Precisa ser registrado ANTES de publicar o manager em a.screenshotConsent.
	mgr.SetSessionGrant(a.screenshotSessionAllowed)
	a.screenshotConsent = mgr
	policy := a.loadScreenshotPolicyOrDefault()
	a.screenshotPolicy = policy
	a.screenshotLimiter = screenshot.NewCaptureLimiter(policy.LimitMax(), policy.LimitWindow())
}

// ── Política de captura (privacidade) ─────────────────────────────────────

// screenshotPolicyPathOverride permite que testes isolem o arquivo da política
// (evita escrever no %ProgramData% real da máquina). Vazio em produção.
var screenshotPolicyPathOverride string

func (a *App) screenshotPolicyPaths() []string {
	if screenshotPolicyPathOverride != "" {
		return []string{screenshotPolicyPathOverride}
	}
	return platform.ChatConfigPathCandidates(screenshotPolicyFile)
}

// loadScreenshotPolicyOrDefault lê a política do disco (vazia/inválida → padrão).
// NÃO adquirir screenshotMu: chamado com o lock já tomado por initScreenshotService.
func (a *App) loadScreenshotPolicyOrDefault() screenshot.Policy {
	for _, path := range a.screenshotPolicyPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var policy screenshot.Policy
		if err := json.Unmarshal(data, &policy); err != nil {
			continue
		}
		return policy.Normalize()
	}
	return screenshot.DefaultPolicy()
}

func (a *App) saveScreenshotPolicyFile(policy screenshot.Policy) error {
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	var lastErr error
	for _, path := range a.screenshotPolicyPaths() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			lastErr = err
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nenhum caminho valido para persistir a politica de captura")
	}
	return lastErr
}

// currentScreenshotPolicy devolve a política vigente (cópia).
func (a *App) currentScreenshotPolicy() screenshot.Policy {
	a.initScreenshotService()
	a.screenshotMu.Lock()
	defer a.screenshotMu.Unlock()
	return a.screenshotPolicy
}

// screenshotSessionAllowed informa se a IA está dispensada de pedir autorização
// a cada captura ("permitir sempre", persistido na política).
func (a *App) screenshotSessionAllowed() bool {
	a.screenshotMu.Lock()
	defer a.screenshotMu.Unlock()
	return a.screenshotPolicy.SessionCaptureAllowed()
}

// SetScreenshotSessionAllow liga/desliga a permissão "permitir sempre": com ela
// ligada a IA deixa de abrir a pergunta por captura até o usuário desativar — a
// captura continua sujeita à blocklist, às demais regras da política, à cota e à
// auditoria. O valor é PERSISTIDO no arquivo da política: sobrevive a reinício e
// atualização do agente, e só sai quando o usuário desmarca ou volta ao padrão.
func (a *App) SetScreenshotSessionAllow(enabled bool) error {
	a.initScreenshotService()
	if enabled && !a.currentScreenshotPolicy().AiCaptureAllowed() {
		// Liberar a permissão com os pedidos da IA desligados deixaria uma
		// autorização pendente que passaria a valer, sem nova confirmação,
		// quando os pedidos fossem religados — e o painel mostraria "desabilitado".
		return fmt.Errorf("habilite os pedidos de captura da IA antes de liberar a permissao permanente")
	}
	a.screenshotMu.Lock()
	policy := a.screenshotPolicy
	policy.AllowSessionCapture = &enabled
	policy = policy.Normalize()
	if err := a.saveScreenshotPolicyFile(policy); err != nil {
		a.screenshotMu.Unlock()
		return err
	}
	a.screenshotPolicy = policy
	a.screenshotMu.Unlock()
	a.Logs.Append(fmt.Sprintf("[screenshot] permissao permanente de captura (permitir sempre) = %t", enabled))
	return nil
}

// ResetScreenshotPolicy volta a política local aos padrões de fábrica
// (DefaultPolicy), persiste, reconfigura o limitador e revoga a permissão
// permanente ("permitir sempre"). Devolve a política resultante em JSON para a
// UI não precisar de uma segunda chamada.
func (a *App) ResetScreenshotPolicy() (string, error) {
	a.initScreenshotService()
	policy := screenshot.DefaultPolicy().Normalize()
	// Mesma seção crítica de SaveScreenshotPolicy (persistência + publicação
	// juntas): o reset gravava o arquivo FORA do lock e um "Salvar política"
	// concorrente podia regravar o disco com a política antiga depois do reset,
	// deixando memória e arquivo divergentes até o próximo restart.
	a.screenshotMu.Lock()
	if err := a.saveScreenshotPolicyFile(policy); err != nil {
		a.screenshotMu.Unlock()
		return "", err
	}
	a.screenshotPolicy = policy
	limiter := a.screenshotLimiter
	a.screenshotMu.Unlock()
	limiter.Reconfigure(policy.LimitMax(), policy.LimitWindow())
	a.Logs.Append("[screenshot] politica de captura restaurada ao padrao")
	body, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// GetScreenshotPolicy devolve a política atual (binding da UI de privacidade).
func (a *App) GetScreenshotPolicy() (string, error) {
	policy := a.currentScreenshotPolicy().Normalize()
	body, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// SaveScreenshotPolicy salva a política editada na UI e reconfigura o limitador.
func (a *App) SaveScreenshotPolicy(policyJSON string) error {
	a.initScreenshotService()
	var policy screenshot.Policy
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil {
		return fmt.Errorf("politica de captura invalida: %w", err)
	}
	// O payload da UI de política NÃO inclui allowAiCapture (checkbox próprio do
	// painel). Sem este cuidado o campo ausente virava nil e Normalize o
	// reativava para true — salvar a política reabilitava silenciosamente os
	// pedidos da IA que o usuário havia desligado.
	//
	// Seção crítica ÚNICA (leitura + persistência + publicação): o checkbox dos
	// pedidos da IA é um binding assíncrono separado e, sem isso, salvar podia
	// regravar um allowAiCapture antigo por cima de um "desligar" concorrente.
	a.screenshotMu.Lock()
	if policy.AllowAiCapture == nil {
		policy.AllowAiCapture = a.screenshotPolicy.AllowAiCapture
	}
	// A permissão "permitir sempre" também não vem no payload do formulário:
	// preservar o valor vigente evita revogá-la por omissão ao salvar. Ela só cai
	// quando o usuário desliga os pedidos da IA (abaixo) ou desmarca a permissão.
	if policy.AllowSessionCapture == nil {
		policy.AllowSessionCapture = a.screenshotPolicy.AllowSessionCapture
	}
	// Os campos da galeria também não vêm no formulário de privacidade: sem
	// preservar o valor vigente, salvar qualquer outra opção voltaria a galeria
	// para os defaults (e reativaria a galeria de quem a desligou).
	if policy.CopyOnConfirm == nil {
		policy.CopyOnConfirm = a.screenshotPolicy.CopyOnConfirm
	}
	if policy.GalleryEnabled == nil {
		policy.GalleryEnabled = a.screenshotPolicy.GalleryEnabled
	}
	if policy.GalleryMaxItems <= 0 {
		policy.GalleryMaxItems = a.screenshotPolicy.GalleryMaxItems
	}
	if !policy.AiCaptureAllowed() {
		denied := false
		policy.AllowSessionCapture = &denied
	}
	policy = policy.Normalize()
	if err := a.saveScreenshotPolicyFile(policy); err != nil {
		a.screenshotMu.Unlock()
		return err
	}
	a.screenshotPolicy = policy
	limiter := a.screenshotLimiter
	a.screenshotMu.Unlock()
	limiter.Reconfigure(policy.LimitMax(), policy.LimitWindow())
	a.Logs.Append("[screenshot] politica de captura atualizada")
	return nil
}

// enrichWindows marca janelas cujo processo está bloqueado pela política.
func enrichWindows(wins []screenshot.WindowInfo, policy screenshot.Policy) []screenshot.WindowInfo {
	for i := range wins {
		wins[i].Blocked = policy.IsProcessBlocked(wins[i].ProcessName)
	}
	return wins
}

// applyScreenshotPolicy valida/ajusta a requisição da IA conforme a política.
// mode=focused é resolvido para a janela em foco (vira mode=window).
func (a *App) applyScreenshotPolicy(req *screenshot.Request, policy screenshot.Policy) error {
	if strings.EqualFold(req.Mode, screenshot.ModeFocused) {
		handle := screenshot.ForegroundWindowHandle()
		// A janela em foco pode ser a DO PRÓPRIO AGENTE (o pedido veio do chat)
		// ou estar bloqueada — nesse caso usa a primeira elegível no z-order.
		if wins, err := screenshot.ListWindowsForPolicy(); err == nil {
			handle = resolveFocusedHandle(handle, wins, policy)
		}
		if handle == 0 {
			return fmt.Errorf("nao ha janela elegivel em foco para capturar (a janela do agente e bloqueadas sao ignoradas)")
		}
		req.Mode = screenshot.ModeWindow
		req.WindowHandle = handle
	}
	switch req.Mode {
	case screenshot.ModeWindow:
		if handle := req.WindowHandle; handle != 0 {
			if wins, err := screenshot.ListWindowsForPolicy(); err == nil {
				for _, w := range wins {
					if w.Handle == handle && policy.IsProcessBlocked(w.ProcessName) {
						return fmt.Errorf("captura bloqueada pela politica de privacidade: processo %q esta na blocklist", w.ProcessName)
					}
				}
			}
		}
	case screenshot.ModeInteractive:
		// A seleção final (área ou janela) é validada em
		// validateSelectionPolicy — aqui não dá para saber o que o usuário vai
		// escolher.
	default:
		if policy.WindowRequired() {
			return fmt.Errorf("a politica de privacidade exige capturar uma janela especifica (use list_open_windows + mode=window/focused)")
		}
		if !policy.FullScreenAllowed() {
			return fmt.Errorf("captura de tela inteira desabilitada pela politica de privacidade — capture uma janela especifica")
		}
		// A blocklist também vale para a captura AUTOMÁTICA (região/monitor/tela
		// inteira): antes só o modo janela era checado aqui, então a tool podia
		// fotografar um app bloqueado sem passar pelo overlay de seleção.
		if sel, ok := screenshotSelectionForRequest(*req); ok {
			if hit, blocked := a.blockedSelectionHit(policy, sel); blocked {
				return fmt.Errorf("captura bloqueada pela politica de privacidade: a area pedida cobre a janela de %q", hit.ProcessName)
			}
		}
	}
	return nil
}

// resolveFocusedHandle devolve o handle da janela em foco ou, quando ela é a do
// próprio agente / está bloqueada / minimizada, a primeira janela elegível no
// z-order (índice 0 = mais à frente).
func resolveFocusedHandle(foreground uint64, wins []screenshot.WindowInfo, policy screenshot.Policy) uint64 {
	eligible := func(h uint64) bool {
		for _, w := range wins {
			if w.Handle != h {
				continue
			}
			return !w.IsSelf && !w.Minimized && !policy.IsProcessBlocked(w.ProcessName)
		}
		return false
	}
	if foreground != 0 && eligible(foreground) {
		return foreground
	}
	for _, w := range wins {
		if w.IsSelf || w.Minimized || w.Width <= 0 || w.Height <= 0 {
			continue
		}
		if policy.IsProcessBlocked(w.ProcessName) {
			continue
		}
		return w.Handle
	}
	return 0
}

// screenshotImageFormat devolve a preferência de formato de imagem da política
// local ("auto" = WebP lossless quando menor que PNG; "png" = portabilidade).
func (a *App) screenshotImageFormat() string {
	return screenshot.NormalizeFormat(a.currentScreenshotPolicy().ImageFormat)
}

// validateSelectionPolicy aplica a política local à seleção feita no overlay.
// O modo interativo não passa por applyScreenshotPolicy (o alvo só existe depois
// da escolha do usuário), então a validação precisa acontecer aqui — inclusive
// para a imagem ANOTADA, que antes era aceita sem checar a blocklist.
func (a *App) validateSelectionPolicy(session *screenshotOverlaySession, sel screenshot.Selection) error {
	policy := a.currentScreenshotPolicy()
	if strings.EqualFold(sel.Kind, "window") {
		checked := false
		if session.payload != nil {
			for _, w := range session.payload.Windows {
				if w.Handle != sel.WindowHandle {
					continue
				}
				checked = true
				if w.Blocked || policy.IsProcessBlocked(w.ProcessName) {
					return fmt.Errorf("captura bloqueada pela politica de privacidade: processo %q", w.ProcessName)
				}
			}
		}
		// Handle não presente na lista do overlay (janela nova/lista vazia):
		// confere de novo na enumeração atual antes de permitir.
		if !checked && sel.WindowHandle != 0 {
			if wins, err := screenshot.ListWindowsForPolicy(); err == nil {
				for _, w := range wins {
					if w.Handle == sel.WindowHandle && policy.IsProcessBlocked(w.ProcessName) {
						return fmt.Errorf("captura bloqueada pela politica de privacidade: processo %q", w.ProcessName)
					}
				}
			}
		}
		return nil
	}
	if policy.WindowRequired() {
		return fmt.Errorf("a politica de privacidade exige capturar uma janela especifica")
	}
	// Blocklist também vale para recorte geométrico: antes só o modo janela era
	// checado, então um print por região podia fotografar um app bloqueado.
	if hit, ok := a.blockedSelectionHit(policy, sel); ok {
		return fmt.Errorf("captura bloqueada pela politica de privacidade: a area selecionada cobre a janela de %q", hit.ProcessName)
	}
	if !policy.FullScreenAllowed() && session.payload != nil {
		p := session.payload
		coversFullDesktop := sel.X <= p.VirtualX && sel.Y <= p.VirtualY &&
			sel.X+sel.Width >= p.VirtualX+p.VirtualWidth &&
			sel.Y+sel.Height >= p.VirtualY+p.VirtualHeight
		if coversFullDesktop {
			return fmt.Errorf("captura de tela inteira desabilitada pela politica de privacidade")
		}
	}
	return nil
}

// blockedSelectionHit devolve a primeira janela bloqueada cuja área é
// encobrida pela seleção geométrica. Enumera as janelas atuais (as do payload do
// overlay podem estar filtradas) e ignora a janela do próprio agente.
func (a *App) blockedSelectionHit(policy screenshot.Policy, sel screenshot.Selection) (screenshot.WindowInfo, bool) {
	wins, err := screenshot.ListWindowsForPolicy()
	if err != nil {
		return screenshot.WindowInfo{}, false
	}
	return screenshot.FirstBlockedOverlap(policy, wins, sel)
}

// screenshotSelectionForRequest traduz o pedido da IA (região, monitor ou tela
// inteira) no retângulo físico usado pela checagem geométrica da blocklist.
func screenshotSelectionForRequest(req screenshot.Request) (screenshot.Selection, bool) {
	switch strings.ToLower(strings.TrimSpace(req.Mode)) {
	case screenshot.ModeRegion:
		if req.Width <= 0 || req.Height <= 0 {
			return screenshot.Selection{}, false
		}
		return screenshot.Selection{Kind: "region", X: req.X, Y: req.Y, Width: req.Width, Height: req.Height}, true
	case screenshot.ModeMonitor:
		mons := screenshot.ListMonitors()
		if len(mons) == 0 {
			return screenshot.Selection{}, false
		}
		// Mesma regra de captureMonitorFormat: índice fora da faixa cai no 0
		// (primário). Sem espelhar isso, um índice inválido pulava a checagem da
		// blocklist e ainda assim capturava o monitor primário.
		index := req.MonitorIndex
		if index < 0 || index >= len(mons) {
			index = 0
		}
		m := mons[index]
		return screenshot.Selection{Kind: "monitor", X: m.X, Y: m.Y, Width: m.Width, Height: m.Height}, true
	case screenshot.ModeFull:
		mons := screenshot.ListMonitors()
		if len(mons) == 0 {
			return screenshot.Selection{}, false
		}
		x0, y0 := mons[0].X, mons[0].Y
		x1, y1 := mons[0].X+mons[0].Width, mons[0].Y+mons[0].Height
		for _, m := range mons[1:] {
			if m.X < x0 {
				x0 = m.X
			}
			if m.Y < y0 {
				y0 = m.Y
			}
			if m.X+m.Width > x1 {
				x1 = m.X + m.Width
			}
			if m.Y+m.Height > y1 {
				y1 = m.Y + m.Height
			}
		}
		return screenshot.Selection{Kind: "full", X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0}, true
	}
	return screenshot.Selection{}, false
}

// checkScreenshotBudget aplica o rate limit de capturas da IA.
func (a *App) checkScreenshotBudget(now time.Time) error {
	a.screenshotMu.Lock()
	limiter := a.screenshotLimiter
	a.screenshotMu.Unlock()
	ok, retryAfter := limiter.Check(now)
	if ok {
		return nil
	}
	return fmt.Errorf("limite de capturas atingido (%d por %s) — tente novamente em cerca de %s",
		limiter.Max(), limiter.Window().Round(time.Second), retryAfter.Round(time.Second))
}

func (a *App) recordScreenshotBudget(now time.Time) {
	a.screenshotMu.Lock()
	limiter := a.screenshotLimiter
	a.screenshotMu.Unlock()
	limiter.Record(now)
}

// NOTA (2026-10-01): o consentimento da captura pela IA passou a ser POR
// CAPTURA — o usuário é perguntado TODAS as vezes. Por isso não há mais
// leitura/gravação de consentimento persistido ("permitir sempre" deixou de
// existir) e o arquivo legado screenshot_consent.json é ignorado.
// ── Bindings de UI / tools MCP ────────────────────────────────────────────

// ListOpenWindowsJSON implementa AppBridge: janelas visíveis + monitores,
// enriquecidas com o flag Blocked da política local de privacidade.
func (a *App) ListOpenWindowsJSON(includeUntitled bool) (json.RawMessage, error) {
	wins, err := screenshot.ListWindowsWithOptions(includeUntitled)
	if err != nil {
		return nil, err
	}
	policy := a.currentScreenshotPolicy()
	wins = enrichWindows(wins, policy)
	capturable := 0
	for _, w := range wins {
		if !w.Blocked {
			capturable++
		}
	}
	return json.Marshal(map[string]any{
		"ok":         true,
		"count":      len(wins),
		"capturable": capturable,
		"windows":    wins,
		"monitors":   screenshot.ListMonitors(),
		"policy": map[string]any{
			"blockedProcesses": policy.BlockedProcesses,
			"allowFullScreen":  policy.FullScreenAllowed(),
			"requireWindow":    policy.WindowRequired(),
		},
	})
}

// ScreenshotConsentStatusJSON implementa AppBridge: estado da política de
// captura. A regra é perguntar em cada pedido; "perCaptureConsent" fica false
// quando o usuário ligou a permissão permanente ("permitir sempre") — opt-in,
// desligada por padrão e persistida na política (sobrevive a restart).
func (a *App) ScreenshotConsentStatusJSON() (json.RawMessage, error) {
	a.initScreenshotService()
	sessionAllow := a.screenshotSessionAllowed()
	return json.Marshal(map[string]any{
		"perCaptureConsent": !sessionAllow,
		"sessionAllow":      sessionAllow,
		"aiCaptureEnabled":  a.currentScreenshotPolicy().AiCaptureAllowed(),
		"audit":             a.screenshotConsent.Audit(),
	})
}

// CaptureScreenshotForTool implementa AppBridge: chamada da tool MCP pela IA.
func (a *App) CaptureScreenshotForTool(ctx context.Context, args map[string]any) (json.RawMessage, error) {
	a.initScreenshotService()
	req, reason, err := parseScreenshotArgs(args)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = a.ctx
	}
	// Política primeiro: se os pedidos da IA estão desabilitados, nem pergunta.
	policy := a.currentScreenshotPolicy()
	if !policy.AiCaptureAllowed() {
		return nil, fmt.Errorf("captura de tela pela IA desabilitada pela politica de privacidade")
	}
	req.Format = screenshot.NormalizeFormat(policy.ImageFormat)
	if err := a.applyScreenshotPolicy(&req, policy); err != nil {
		return nil, err
	}
	now := time.Now()
	if err := a.checkScreenshotBudget(now); err != nil {
		return nil, err
	}
	// A TENTATIVA já consome cota (mesmo negada/cancelada): sem isso o LLM podia
	// ficar disparando perguntas de consentimento sem limite (a cota contava só
	// capturas concluídas).
	a.recordScreenshotBudget(now)
	// AUTORIZAÇÃO: pergunta a CADA pedido, exceto quando o usuário já ligou a
	// permissão permanente ("permitir sempre") — opt-in, desligada por padrão e
	// persistida na política. A pergunta inclui o ALVO (janela/região/monitores)
	// para consentimento informado: o motivo escrito pela IA não basta.
	consentReason := reason
	if target := a.screenshotTargetLabel(req); target != "" {
		if strings.TrimSpace(consentReason) != "" {
			consentReason += " — alvo: " + target
		} else {
			consentReason = "alvo: " + target
		}
	}
	sessionAuthorized := a.screenshotSessionAllowed()
	if sessionAuthorized {
		a.Logs.Append("[screenshot] captura autorizada pela permissao permanente (sem nova pergunta)")
	} else {
		decision, authErr := a.screenshotConsent.Ensure(ctx, consentReason)
		if authErr != nil {
			a.recordScreenshotConsentDenied(decision, req, consentReason)
			return nil, authErr
		}
		if decision == screenshot.DecisionGrantedForSession {
			// O usuário escolheu "permitir sempre" no diálogo: liga (e persiste)
			// a permissão permanente para os próximos pedidos.
			sessionAuthorized = true
			if err := a.SetScreenshotSessionAllow(true); err != nil {
				a.Logs.Append("[screenshot] falha ao ativar autorizacao de sessao: " + err.Error())
			}
		}
	}
	return a.runScreenshotRequest(ctx, req, reason, true, sessionAuthorized)
}

// sessionAuthorized indica que esta captura foi coberta pela autorização de
// sessão (sem pergunta individual) — entra na auditoria.
func (a *App) runScreenshotRequest(ctx context.Context, req screenshot.Request, reason string, byLLM bool, sessionAuthorized bool) (json.RawMessage, error) {
	if strings.EqualFold(req.Mode, screenshot.ModeInteractive) {
		return a.captureInteractive(ctx, reason, byLLM, sessionAuthorized)
	}
	// A janela do agente é ocultada durante a captura para não sair no print.
	restoreVisibility := a.hideAgentWindowForCapture()
	defer restoreVisibility()
	started := time.Now()
	res, err := screenshot.CaptureScreen(req)
	if err != nil {
		return nil, err
	}
	elapsed := time.Since(started)
	id := a.recordScreenshot(res, reason, byLLM, elapsed, sessionAuthorized)
	// Transparência: mostra no chat o que a IA acabou de capturar (miniatura),
	// já que uma captura automática não passa pelo overlay de seleção.
	a.emitScreenshotCaptured(res, reason, byLLM, id)
	payload := res.ToolPayload(toolNote(res), toolTarget(res))
	return json.Marshal(payload)
}

func toolNote(res *screenshot.CaptureResult) string {
	if res == nil {
		return "captura indisponivel"
	}
	var alvo string
	if res.Window != nil && res.Window.Title != "" {
		alvo = fmt.Sprintf(" da janela \"%s\" (%s)", res.Window.Title, res.Window.ProcessName)
	}
	return fmt.Sprintf("Captura de tela%s (%dx%d) anexada como imagem para analise.", alvo, res.Width, res.Height)
}

func toolTarget(res *screenshot.CaptureResult) map[string]any {
	if res == nil || res.Window == nil {
		return nil
	}
	return map[string]any{
		"windowHandle": res.Window.Handle,
		"title":        res.Window.Title,
		"processName":  res.Window.ProcessName,
		"pid":          res.Window.PID,
	}
}

// screenshotTargetLabel descreve, em linguagem do usuário, o que a captura vai
// abranger. É anexado ao pedido de autorização (consentimento informado).
func (a *App) screenshotTargetLabel(req screenshot.Request) string {
	switch strings.ToLower(strings.TrimSpace(req.Mode)) {
	case screenshot.ModeFull:
		return "tela inteira (todos os monitores)"
	case screenshot.ModeMonitor:
		// 1-based para o usuário (índice 0 = primário no capturer). O índice
		// inválido cai no primário, igual à captura — o rótulo não pode mentir.
		mons := screenshot.ListMonitors()
		index := req.MonitorIndex
		if index < 0 || index >= len(mons) {
			index = 0
		}
		if len(mons) <= 1 {
			return "monitor principal"
		}
		return fmt.Sprintf("monitor %d", index+1)
	case screenshot.ModeRegion:
		return fmt.Sprintf("regiao de %dx%d pixels", req.Width, req.Height)
	case screenshot.ModeWindow, screenshot.ModeFocused:
		// Lista COMPLETA: o rótulo de consentimento não pode ficar genérico só
		// porque a janela está além do corte de z-order do payload.
		if wins, err := screenshot.ListWindowsForPolicy(); err == nil {
			for _, w := range wins {
				if w.Handle != req.WindowHandle {
					continue
				}
				if strings.TrimSpace(w.Title) != "" {
					return fmt.Sprintf("janela %q (%s)", w.Title, w.ProcessName)
				}
				return "janela " + w.ProcessName
			}
		}
		return "janela selecionada"
	case screenshot.ModeInteractive:
		return "area ou janela que VOCE escolher na tela congelada"
	}
	return ""
}

// recordScreenshotConsentDenied registra na auditoria um pedido da IA negado
// pelo usuário (rastreabilidade: a IA pediu, o usuário recusou).
func (a *App) recordScreenshotConsentDenied(decision screenshot.Decision, req screenshot.Request, reason string) {
	if a.screenshotConsent == nil {
		return
	}
	state := "negado"
	if decision == screenshot.DecisionUndecided {
		// Pergunta cancelada (ex.: botão Parar) ou diálogo indisponível.
		state = "cancelado"
	}
	detail := fmt.Sprintf("pedido da IA %s (modo %s)", state, req.Mode)
	if r := strings.TrimSpace(reason); r != "" {
		detail += " | motivo: " + r
	}
	a.screenshotConsent.RecordCapture(screenshot.AuditEntry{
		Decision: decision,
		Mode:     req.Mode,
		Detail:   detail,
		ByLLM:    true,
	})
	a.Logs.Append("[screenshot] " + detail)
}

// recordScreenshot registra a captura na auditoria, guarda a imagem cheia em
// memória (lightbox) e devolve o ID da entrada — 0 quando não registrada.
// elapsed é o tempo do pipeline de captura (acquire + encode) medido pelo
// chamador; entra no detalhe da auditoria como observabilidade barata (Fase 2
// pedia métricas de latência por modo). Zero = não medido.
func (a *App) recordScreenshot(res *screenshot.CaptureResult, reason string, byLLM bool, elapsed time.Duration, sessionAuthorized bool) int64 {
	if a.screenshotConsent == nil || res == nil {
		return 0
	}
	detail := res.Describe()
	if elapsed > 0 {
		detail += fmt.Sprintf(" | %d ms", elapsed.Milliseconds())
	}
	if sessionAuthorized {
		detail += " | autorizada pela permissao permanente"
	}
	if r := strings.TrimSpace(reason); r != "" {
		detail += " | motivo: " + r
	}
	decision := screenshot.DecisionGranted
	if sessionAuthorized {
		decision = screenshot.DecisionGrantedForSession
	}
	entry := a.screenshotConsent.RecordCapture(screenshot.AuditEntry{
		// Capturas só chegam aqui depois de autorizadas (individualmente ou
		// pela autorização de sessão).
		Decision:          decision,
		Mode:              res.Mode,
		Detail:            detail,
		ByLLM:             byLLM,
		Bytes:             len(res.Data),
		Thumbnail:         res.Thumbnail,
		ThumbnailMIME:     res.ThumbnailMIME,
		SessionAuthorized: sessionAuthorized,
	})
	a.storeScreenshotImage(entry.ID, res)
	a.Logs.Append("[screenshot] " + detail)
	return entry.ID
}

// emitScreenshotCaptured publica no chat a miniatura de uma captura concluída
// (o LLM recebe a imagem cheia no tool result; o usuário vê o que foi capturado).
func (a *App) emitScreenshotCaptured(res *screenshot.CaptureResult, reason string, byLLM bool, auditID int64) {
	if res == nil {
		return
	}
	thumb := res.ThumbnailDataURL()
	if thumb == "" {
		return
	}
	payload := map[string]any{
		"mode":    res.Mode,
		"width":   res.Width,
		"height":  res.Height,
		"dataUrl": thumb,
		"byLlm":   byLLM,
		"reason":  strings.TrimSpace(reason),
		"at":      res.CapturedAt.UTC().Format(time.RFC3339),
		"auditId": auditID,
	}
	if res.Window != nil {
		payload["window"] = map[string]any{
			"title":       res.Window.Title,
			"processName": res.Window.ProcessName,
			"pid":         res.Window.PID,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	a.EmitEvent("screenshot:captured", string(body))
	a.PublishChatEvent("screenshot:captured", string(body))
}

const (
	// agentHideBeforeCaptureDelay dá tempo ao compositor do Windows de remover
	// a janela do agente da tela antes do print.
	agentHideBeforeCaptureDelay = 220 * time.Millisecond
	// screenshotImageStoreLimit é quantas capturas cheias ficam em memória para
	// o lightbox do chat (as mais antigas são descartadas).
	screenshotImageStoreLimit = 8
	// annotatedImageMaxDim limita o lado maior da imagem anotada recebida do
	// overlay. 0 = resolução ORIGINAL (sem redução): a imagem anotada é o que o
	// usuário leu na tela e precisa manter o mesmo detalhe da captura.
	annotatedImageMaxDim = 0
	// overlayImageMaxDim é o lado maior da imagem congelada exibida no overlay
	// (fundo da seleção/anotação). 0 = resolução original da captura.
	overlayImageMaxDim = 0
	// screenshotOpenWaitMax é o teto de espera de um pedido que chegou enquanto
	// outra abertura de overlay está em andamento. Sem ele, uma captura travada
	// no driver (DXGI/PrintWindow pendurado) deixava TODOS os pedidos seguintes
	// presos no canal para sempre — e a captura de tela morta até reiniciar.
	screenshotOpenWaitMax = 30 * time.Second
)

// hideAgentWindowForCapture oculta a janela do agente (se a política permitir e
// ela estiver visível) e devolve a função de restauração. Usada para que a
// própria UI do agente não apareça no print. Idempotente.
func (a *App) hideAgentWindowForCapture() func() {
	if a.mainWindow == nil || !a.currentScreenshotPolicy().HideWindowOnCapture() {
		return func() {}
	}
	if !a.mainWindow.IsVisible() {
		return func() {}
	}
	a.mainWindow.Hide()
	time.Sleep(agentHideBeforeCaptureDelay)
	shown := false
	return func() {
		if shown {
			return
		}
		shown = true
		a.mainWindow.Show()
	}
}

// hideSupportChatForCapture esconde a janela do CHAT DE SUPORTE durante a captura
// assistida e devolve a função de restauração.
//
// Sem isso a própria janela do chat saía no print (ela está em primeiro plano
// quando o usuário clica no botão de captura). A restauração é composta no
// `restore` da sessão do overlay, então a janela volta só quando a captura
// termina — não no meio da seleção.
//
// Diferente da janela principal, isto NÃO depende de hideAgentWindow: a janela
// do chat contém a conversa com o suporte e nunca pode entrar numa captura — que
// é justamente o que o usuário vai anexar nessa conversa. A política de
// privacidade só decide se a interface principal do agent é escondida.
func (a *App) hideSupportChatForCapture() func() {
	if a == nil {
		return func() {}
	}

	a.supportChatMu.Lock()
	win := a.supportChatWin
	a.supportChatMu.Unlock()

	if win == nil || !win.IsVisible() {
		return func() {}
	}

	win.Hide()
	time.Sleep(agentHideBeforeCaptureDelay)

	shown := false
	return func() {
		if shown {
			return
		}
		shown = true
		win.Show()
	}
}

// screenshotStoredImage é a imagem cheia de uma captura mantida em memória.
type screenshotStoredImage struct {
	Data   []byte
	MIME   string
	Width  int
	Height int
}

// storeScreenshotImage guarda a imagem cheia da captura (limitado às últimas
// N) para o lightbox do chat — sem persistir nada em disco.
func (a *App) storeScreenshotImage(id int64, res *screenshot.CaptureResult) {
	if id <= 0 || res == nil || len(res.Data) == 0 {
		return
	}
	a.screenshotImagesMu.Lock()
	defer a.screenshotImagesMu.Unlock()
	if a.screenshotImages == nil {
		a.screenshotImages = map[int64]screenshotStoredImage{}
	}
	a.screenshotImages[id] = screenshotStoredImage{Data: res.Data, MIME: res.MIME, Width: res.Width, Height: res.Height}
	a.screenshotImageOrder = append(a.screenshotImageOrder, id)
	for len(a.screenshotImageOrder) > screenshotImageStoreLimit {
		oldest := a.screenshotImageOrder[0]
		a.screenshotImageOrder = a.screenshotImageOrder[1:]
		delete(a.screenshotImages, oldest)
	}
}

// CopyImageDataURLToClipboard copia uma imagem (data URL) para a área de
// transferência do Windows como bitmap (CF_DIB). Usado pelo lightbox.
func (a *App) CopyImageDataURLToClipboard(dataURL string) error {
	img, err := screenshot.DecodeDataURLToImage(dataURL)
	if err != nil {
		return fmt.Errorf("imagem invalida para copiar: %w", err)
	}
	if err := screen.SetClipboardImage(img); err != nil {
		return fmt.Errorf("falha ao copiar para a area de transferencia: %w", err)
	}
	a.Logs.Append("[screenshot] imagem copiada para a area de transferencia")
	return nil
}

// GetScreenshotImage devolve a imagem cheia de uma captura da auditoria
// (lightbox do chat). IDs fora do buffer (expirados) retornam erro.
func (a *App) GetScreenshotImage(id int64) (string, error) {
	a.screenshotImagesMu.Lock()
	stored, ok := a.screenshotImages[id]
	a.screenshotImagesMu.Unlock()
	if !ok {
		return "", fmt.Errorf("imagem da captura %d nao esta mais em memoria", id)
	}
	body, err := json.Marshal(map[string]any{
		"id":      id,
		"mime":    stored.MIME,
		"width":   stored.Width,
		"height":  stored.Height,
		"dataUrl": "data:" + stored.MIME + ";base64," + base64.StdEncoding.EncodeToString(stored.Data),
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// parseScreenshotArgs normaliza os argumentos JSON da tool capture_screenshot.
func parseScreenshotArgs(args map[string]any) (screenshot.Request, string, error) {
	req := screenshot.Request{Mode: strings.ToLower(screenshotStringArg(args, "mode"))}
	if req.Mode == "" {
		req.Mode = screenshot.ModeInteractive
	}
	switch req.Mode {
	case screenshot.ModeFull, screenshot.ModeInteractive, screenshot.ModeFocused:
	case screenshot.ModeMonitor:
		req.MonitorIndex = screenshotIntArg(args, "monitor")
	case screenshot.ModeWindow:
		req.WindowHandle = uint64(screenshotIntArg(args, "windowHandle"))
		if req.WindowHandle == 0 {
			return req, "", fmt.Errorf("windowHandle obrigatorio no modo window (use list_open_windows para obter o handle)")
		}
	case screenshot.ModeRegion:
		req.X = screenshotIntArg(args, "x")
		req.Y = screenshotIntArg(args, "y")
		req.Width = screenshotIntArg(args, "width")
		req.Height = screenshotIntArg(args, "height")
		if req.Width <= 0 || req.Height <= 0 {
			return req, "", fmt.Errorf("x, y, width e height sao obrigatorios no modo region")
		}
	default:
		return req, "", fmt.Errorf("modo de captura invalido: %q (use full, window, focused, monitor, region ou interactive)", req.Mode)
	}
	req.Quality = screenshotIntArg(args, "quality")
	// maxDimension opcional: só REDUZ (o padrão é a resolução original da
	// captura). 0 = sem redução.
	if maxDim := screenshotIntArg(args, "maxDimension"); maxDim > 0 {
		if maxDim < 200 {
			maxDim = 200
		}
		if maxDim > 3840 {
			maxDim = 3840
		}
		req.MaxDimension = maxDim
	}
	return req, screenshotStringArg(args, "reason"), nil
}

func screenshotStringArg(args map[string]any, name string) string {
	v, _ := args[name].(string)
	return strings.TrimSpace(v)
}

func screenshotIntArg(args map[string]any, name string) int {
	switch v := args[name].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

// authorized indica que a captura da sessão está coberta pela autorização de
// sessão do usuário.
func (s *screenshotOverlaySession) authorized() bool {
	return s != nil && s.sessionAuthorized
}

// ── Overlay de seleção (tela congelada) ───────────────────────────────────

// PrepareScreenshotOverlay congela o desktop e entrega a seleção à JANELA
// DEDICADA do overlay (screenshot_overlay_window.go), que a desenha.
//
// O chamador (janela principal) recebe apenas um ack: o payload da tela
// congelada vai para a janela do overlay pelo canal de entrega. Isto é o que
// torna a captura independente da janela principal — inclusive quando ela está
// no tray. "destination" diz para onde o print volta ao terminar.
func (a *App) PrepareScreenshotOverlay(reason, destination string) (string, error) {
	a.initScreenshotService()
	session, err := a.openScreenshotOverlay(strings.TrimSpace(reason), false, false, normalizeScreenshotDestination(destination))
	if err != nil {
		return "", err
	}
	ack, err := json.Marshal(map[string]any{"ok": true, "session": session.id})
	if err != nil {
		return "", err
	}
	return string(ack), nil
}

// FinishScreenshotOverlay materializa a seleção do usuário e restaura a janela.
func (a *App) FinishScreenshotOverlay(sessionID, selectionJSON string) (string, error) {
	res, err := a.finishScreenshotOverlay(sessionID, selectionJSON)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{
		"ok":         true,
		"session":    sessionID,
		"mode":       res.Mode,
		"mime":       res.MIME,
		"width":      res.Width,
		"height":     res.Height,
		"bytes":      len(res.Data),
		"dataUrl":    res.DataURL(),
		"window":     res.Window,
		"capturedAt": res.CapturedAt,
	})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

// CancelScreenshotOverlay cancela a seleção e restaura a janela.
func (a *App) CancelScreenshotOverlay(sessionID string) error {
	a.releaseScreenshotOverlay(sessionID, nil, fmt.Errorf("captura cancelada pelo usuario"))
	// Cancelar um id que não corresponde a nenhuma sessão ativa é inofensivo e
	// não pode deixar a janela presa no modo overlay (mesma defesa do Finish).
	a.restoreOrphanScreenshotOverlay()
	return nil
}

// GetScreenshotPermission devolve o estado de autorização + auditoria.
func (a *App) GetScreenshotPermission() (string, error) {
	a.initScreenshotService()
	payload, err := a.ScreenshotConsentStatusJSON()
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

// SetScreenshotAiCaptureEnabled liga/desliga os PEDIDOS de captura da IA.
// Não é uma autorização: com o recurso ligado a IA continua perguntando a cada
// captura; desligado, a tool recusa sem sequer abrir o diálogo.
func (a *App) SetScreenshotAiCaptureEnabled(enabled bool) error {
	a.initScreenshotService()
	// Mesma seção crítica de SaveScreenshotPolicy: ler, persistir e publicar com
	// o lock preso evita lost update entre os dois bindings (usuário desmarca o
	// checkbox e clica "Salvar política" em seguida).
	a.screenshotMu.Lock()
	policy := a.screenshotPolicy
	policy.AllowAiCapture = &enabled
	if !enabled {
		// Revoga (e persiste) a permissão "permitir sempre": desligar os pedidos
		// da IA não pode deixar uma autorização pendente para quando forem
		// religados.
		denied := false
		policy.AllowSessionCapture = &denied
	}
	policy = policy.Normalize()
	if err := a.saveScreenshotPolicyFile(policy); err != nil {
		a.screenshotMu.Unlock()
		return err
	}
	a.screenshotPolicy = policy
	limiter := a.screenshotLimiter
	a.screenshotMu.Unlock()
	// Mantém o limitador coerente com a política persistida (mesma regra de
	// SaveScreenshotPolicy), já que Normalize pode reajustar a cota.
	limiter.Reconfigure(policy.LimitMax(), policy.LimitWindow())
	a.Logs.Append(fmt.Sprintf("[screenshot] pedidos de captura da IA habilitados=%t", enabled))
	return nil
}

// screenshotAuditView é um item da auditoria exibido na UI de privacidade.
type screenshotAuditView struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Decision string    `json:"decision"`
	Mode     string    `json:"mode"`
	Detail   string    `json:"detail"`
	ByLLM    bool      `json:"byLlm"`
	Bytes    int       `json:"bytes"`
	// SessionAuthorized: a captura foi coberta pela autorização de sessão.
	SessionAuthorized bool   `json:"sessionAuthorized,omitempty"`
	Thumbnail         string `json:"thumbnail,omitempty"`
}

// GetScreenshotAuditPanel devolve consentimento + política + uso do limite +
// auditoria (com miniaturas das últimas capturas) para o painel de privacidade.
func (a *App) GetScreenshotAuditPanel() (string, error) {
	a.initScreenshotService()
	policy := a.currentScreenshotPolicy()
	sessionAllow := a.screenshotSessionAllowed()
	a.screenshotMu.Lock()
	limiter := a.screenshotLimiter
	a.screenshotMu.Unlock()

	entries := a.screenshotConsent.Audit()
	views := make([]screenshotAuditView, 0, screenshotAuditThumbLimit)
	for i := len(entries) - 1; i >= 0 && len(views) < screenshotAuditThumbLimit; i-- {
		entry := entries[i]
		view := screenshotAuditView{
			ID: entry.ID, At: entry.At, Decision: string(entry.Decision),
			Mode: entry.Mode, Detail: entry.Detail, ByLLM: entry.ByLLM, Bytes: entry.Bytes,
			SessionAuthorized: entry.SessionAuthorized,
		}
		if len(entry.Thumbnail) > 0 {
			// MIME vem do encoder (WebP quando cgo disponível); o default jpeg
			// cobre entradas antigas/legadas sem o campo.
			mime := strings.TrimSpace(entry.ThumbnailMIME)
			if mime == "" {
				mime = "image/jpeg"
			}
			view.Thumbnail = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(entry.Thumbnail)
		}
		views = append(views, view)
	}

	now := time.Now()
	body, err := json.Marshal(map[string]any{
		"perCaptureConsent": !sessionAllow,
		"sessionAllow":      sessionAllow,
		"aiCaptureEnabled":  policy.AiCaptureAllowed(),
		"locale":            applocale.DetectPreferredLocale(),
		"policy":            policy.Normalize(),
		"usage": map[string]any{
			"used":          limiter.Count(now),
			"max":           limiter.Max(),
			"windowMinutes": int(limiter.Window().Minutes()),
		},
		"audit": views,
	})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// abortScreenshotOverlay encerra a sessão ativa (hook de fechar/ocultar janela).
func (a *App) abortScreenshotOverlay(reason string) {
	a.screenshotMu.Lock()
	session := a.screenshotOverlay
	if session == nil {
		// Abertura em andamento: a sessão ainda não existe, mas a janela já
		// foi/está sendo capturada. Marca para o build liberar tudo ao terminar
		// em vez de publicar uma sessão viva numa janela fechada/ocultada (a
		// próxima captura devolveria aquele frame congelado antigo).
		if a.screenshotOpening {
			a.screenshotOpenAbort = true
			a.screenshotMu.Unlock()
			a.Logs.Append("[screenshot] abertura abortada durante a captura: " + reason)
			return
		}
		a.screenshotMu.Unlock()
		return
	}
	a.screenshotMu.Unlock()
	a.releaseScreenshotOverlay(session.id, nil, fmt.Errorf("captura cancelada: %s", reason))
}

// GetOpenWindows devolve as janelas visíveis (binding de UI).
func (a *App) GetOpenWindows() (string, error) {
	b, err := a.ListOpenWindowsJSON(false)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// openScreenshotOverlay abre a sessão do overlay, SERIALIZANDO as chamadas
// concorrentes: enquanto uma captura está sendo preparada (esconder janelas +
// congelar tela + encode, ~1 s), os pedidos que chegam esperam a MESMA sessão
// em vez de dispararem outra captura e outra sessão.
//
// BUG CORRIGIDO (relato "a tela do PC fica toda preta" + "Falha na captura de
// tela: RuntimeError: sessao de captura expirada ou invalida"): o binding é
// atendido pelo Wails em goroutine própria e a sessão só era publicada no FIM
// da captura. Com N pedidos em poucos segundos (16 em ~3 s no log do usuário),
// todos passavam pelo check inicial, cada um criava a SUA sessão e só a última
// ficava em a.screenshotOverlay — os restore das perdedoras (bounds da janela,
// janela do chat, hooks) eram descartados. O front desenhava um payload cujo id
// já não existia no backend: o Finish falhava com "sessao de captura expirada ou
// invalida" e a janela ficava frameless/always-on-top cobrindo o desktop.
func (a *App) openScreenshotOverlay(reason string, byLLM bool, sessionAuthorized bool, destination string) (*screenshotOverlaySession, error) {
	for {
		a.screenshotMu.Lock()
		if active := a.screenshotOverlay; active != nil {
			a.screenshotMu.Unlock()
			if active.byLLM == byLLM {
				return active, nil
			}
			return nil, fmt.Errorf("ja existe uma captura de tela em andamento")
		}
		if a.screenshotOpening {
			done := a.screenshotOpenDone
			a.screenshotMu.Unlock()
			// Teto de espera: uma captura pendurada no driver não pode deixar
			// TODOS os pedidos seguintes presos neste canal para sempre (a
			// guarda de abertura nunca seria liberada).
			select {
			case <-done:
			case <-time.After(screenshotOpenWaitMax):
				a.Logs.Append("[screenshot] abertura anterior nao concluiu em " + screenshotOpenWaitMax.String() + "; devolvendo erro ao pedido")
				return nil, fmt.Errorf("ja existe uma captura de tela em andamento (sem resposta); tente novamente")
			}
			// A abertura terminou: devolve a sessão criada ou o MESMO erro —
			// recapturar aqui multiplicaria a captura por clique.
			a.screenshotMu.Lock()
			active := a.screenshotOverlay
			err := a.screenshotOpenErr
			a.screenshotMu.Unlock()
			if active != nil {
				if active.byLLM == byLLM {
					return active, nil
				}
				return nil, fmt.Errorf("ja existe uma captura de tela em andamento")
			}
			if err == nil {
				err = fmt.Errorf("captura de tela indisponivel no momento")
			}
			return nil, err
		}
		a.screenshotOpening = true
		a.screenshotOpenDone = make(chan struct{})
		// NÃO zerar screenshotOpenErr aqui: um pedido que acorda de uma abertura
		// ANTERIOR leria nil enquanto esta abertura ainda corre e receberia um
		// erro genérico. O erro é sobrescrito no fim da abertura (defer) — o
		// valor lido por quem esperou é sempre um erro real, nunca nil.
		a.screenshotOpenAbort = false
		a.screenshotMu.Unlock()
		break
	}
	return a.buildScreenshotOverlay(reason, byLLM, sessionAuthorized, destination)
}

// buildScreenshotOverlay executa a captura do overlay. Chamado apenas por
// openScreenshotOverlay, que garante no máximo UMA abertura em andamento e
// publica o resultado (sessão ou erro) para os pedidos que esperaram na guarda.
func (a *App) buildScreenshotOverlay(reason string, byLLM bool, sessionAuthorized bool, destination string) (session *screenshotOverlaySession, err error) {
	// Publica o fim da abertura SEMPRE (inclusive em erro): sem isto um erro
	// deixaria os pedidos concorrentes presos no canal para sempre.
	defer func() {
		a.screenshotMu.Lock()
		a.screenshotOpening = false
		a.screenshotOpenErr = err
		done := a.screenshotOpenDone
		a.screenshotOpenDone = nil
		a.screenshotMu.Unlock()
		if done != nil {
			close(done)
		}
	}()

	// Com a criação preguiçosa da janela principal (autostart no tray) o agente
	// pode legitimamente não ter janela: não há nada a esconder no print, então o
	// overlay segue normalmente. Antes este caso era recusado com erro — e
	// hideAgentWindowForCapture() já trata janela ausente como no-op.
	// Em modo serviço a.app é nil e ensureScreenshotOverlayWindow devolve erro
	// próprio (mesmo desfecho de antes, com mensagem mais específica).
	// A janela DEDICADA do overlay é criada (uma única vez) ANTES da captura: a
	// página carrega enquanto o desktop é congelado, então o primeiro pedido não
	// paga o custo da janela no caminho crítico. Ela é independente da janela
	// principal — funciona com o agent no tray.
	overlayWin, winErr := a.ensureScreenshotOverlayWindow()
	if winErr != nil {
		return nil, winErr
	}
	if readyErr := a.waitScreenshotOverlayReady(overlayWin); readyErr != nil {
		return nil, readyErr
	}
	vx, vy, vw, vh, ok := screenshot.VirtualBounds()
	if !ok {
		return nil, fmt.Errorf("nao foi possivel determinar o desktop virtual")
	}
	// As janelas do agent (principal e chat de suporte) ficam FORA da tela
	// congelada: a principal porque pode estar em primeiro plano, o chat porque é
	// de onde o pedido normalmente vem. As duas voltam ao estado original no fim
	// da sessão (a principal pode voltar para o tray).
	restoreVisibility := a.hideAgentWindowForCapture()
	// A janela do chat de suporte também fica fora do print (é ela que está em
	// primeiro plano quando o usuário pede a captura).
	restoreSupportChat := a.hideSupportChatForCapture()
	frame, err := a.captureOverlayFrame(vx, vy, vw, vh)
	if err != nil {
		// Nenhum caminho devolveu uma tela utilizável (inclusive o BitBlt): a
		// janela precisa voltar ao estado anterior e o usuário receber o erro —
		// antes um frame PRETO da Desktop Duplication passava por "sucesso" e o
		// overlay aparecia como uma tela toda preta.
		restoreVisibility()
		restoreSupportChat()
		return nil, err
	}
	// Qualidade alta: a tela congelada é o que o usuário lê para escolher a
	// área e o fundo da imagem anotada enviada ao LLM.
	overlayBytes, mime, err := screenshot.EncodeOverlayFrame(frame, overlayImageMaxDim)
	if err != nil {
		a.Logs.Append("[screenshot] falha ao codificar a tela congelada do overlay: " + err.Error())
		// Sem devolver a visibilidade aqui, uma falha de encode deixava a janela
		// do agent e a do chat de suporte OCULTAS (elas só voltavam no restore
		// da sessão, que nunca chega a existir).
		restoreVisibility()
		restoreSupportChat()
		return nil, err
	}
	// O canvas/anotações do frontend trabalham em pixels da IMAGEM reduzida:
	// as dimensões do payload precisam ser as do encode, não as do frame cru.
	overlayW, overlayH := screenshot.ScaledDimensions(frame.Width, frame.Height, overlayImageMaxDim)
	wins, _ := screenshot.ListWindows()
	policy := a.currentScreenshotPolicy()
	wins = enrichWindows(wins, policy)
	id := fmt.Sprintf("shot-%d", time.Now().UnixNano())
	payload := &screenshot.OverlayPayload{
		Session:        id,
		At:             time.Now().UTC(),
		Reason:         reason,
		VirtualX:       vx,
		VirtualY:       vy,
		VirtualWidth:   vw,
		VirtualHeight:  vh,
		ImageWidth:     overlayW,
		ImageHeight:    overlayH,
		ImageDataURL:   "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(overlayBytes),
		ImageMime:      mime,
		Monitors:       screenshot.ListMonitors(),
		Windows:        wins,
		RequestedByLLM: byLLM,
		// Destino do print anotado: a janela do overlay reencaminha o resultado
		// para a janela que o pediu (chat de suporte, comentário, chat da IA).
		Destination: destination,
		// A UI usa isso para não oferecer "Tela inteira" quando a política proíbe.
		PolicyFullScreenAllowed: policy.FullScreenAllowed(),
		PolicyWindowRequired:    policy.WindowRequired(),
	}
	restore, err := a.enterOverlayBounds(overlayWin, vx, vy, vw, vh)
	if err != nil {
		restoreVisibility()
		restoreSupportChat()
		return nil, err
	}
	// A janela foi fechada/ocultada ENQUANTO a captura acontecia: não publicar
	// a sessão (ela ficaria viva com a janela já fora do estado anterior).
	a.screenshotMu.Lock()
	openAborted := a.screenshotOpenAbort
	a.screenshotOpenAbort = false
	a.screenshotMu.Unlock()
	if openAborted {
		restore()
		restoreVisibility()
		restoreSupportChat()
		return nil, fmt.Errorf("captura cancelada: janela do overlay fechada ou ocultada")
	}
	// Entrega a tela congelada à janela do overlay (pull por
	// ScreenshotOverlayNext) ANTES de aparecer: o frontend decodifica a imagem
	// enquanto a janela é exibida, sem o retângulo escuro intermediário.
	payloadJSON, _ := json.Marshal(payload)
	a.deliverScreenshotOverlayPayload(string(payloadJSON))
	session = &screenshotOverlaySession{
		id:                id,
		reason:            reason,
		byLLM:             byLLM,
		destination:       destination,
		sessionAuthorized: sessionAuthorized,
		payload:           payload,
		frame:             frame,
		restore: func() {
			// Ordem: esconde o overlay, descarta payload órfão e devolve as
			// janelas do agent (principal no estado anterior, chat de volta).
			restore()
			a.drainScreenshotOverlayDeliver()
			restoreVisibility()
			restoreSupportChat()
		},
		waiter: make(chan screenshotOverlayOutcome, 1),
	}
	a.screenshotMu.Lock()
	a.screenshotOverlay = session
	// Guarda o restore da sessão viva: é o que devolve a janela aos bounds
	// normais quando o Finish chega com um id que não existe mais (ver
	// restoreOrphanScreenshotOverlay) — sem isso a janela ficava cobrindo o
	// desktop virtual em modo overlay até fechar/reabrir o agent.
	a.screenshotRestore = session.restore
	a.screenshotMu.Unlock()

	// Nenhum evento com o payload é necessário: a janela do overlay o puxa por
	// ScreenshotOverlayNext. Emitir aqui deixava um evento órfão no broker,
	// entregue no próximo poll e capaz de reabrir o overlay de uma sessão já
	// encerrada.
	return session, nil
}

// captureDesktopFrame congela o retângulo físico do desktop para o overlay.
// Delegado ao pacote screenshot: além de evitar a duplicação do copy do frame,
// passa pelo mesmo caminho HDR-aware (monitor Advanced Color capturado por DXGI
// com tone mapping, em vez do BitBlt lavado). OriginX/OriginY vêm preservados —
// o recorte da seleção depende deles em multi-monitor.
func (a *App) captureDesktopFrame(x, y, w, h int) (*screen.Frame, error) {
	frame, err := screenshot.CaptureDesktopFrame(x, y, w, h)
	if err != nil {
		return nil, fmt.Errorf("captura do desktop para o overlay: %w", err)
	}
	return frame, nil
}

// captureOverlayFrame congela o desktop para o overlay com detecção de frame
// PRETO.
//
// BUG CORRIGIDO (relato "quando manda anexar print da tela, a tela toda fica
// preta"): em monitores Advanced Color o overlay usa o capturer DXGI/HDR, e a
// Desktop Duplication devolve um frame PRETO em vários cenários (primeiro frame
// da sessão, driver, tela segura) SEM erro nenhum. O frame preto passava por
// sucesso, virava a "tela congelada" e o overlay — que é uma janela preta
// cobrindo o desktop — aparecia todo preto, sem nenhuma mensagem.
//
// A defesa é: repetir a captura (o primeiro frame da duplication costuma vir
// vazio), depois cair para o BitBlt (caminho confiável usado pelo acesso
// remoto) e, se nem ele render, devolver erro — a janela avisa o usuário em vez
// de mostrar uma tela preta.
func (a *App) captureOverlayFrame(x, y, w, h int) (*screen.Frame, error) {
	const attempts = 3

	var last *screen.Frame
	var lastErr error

	for attempt := 1; attempt <= attempts; attempt++ {
		frame, err := a.captureDesktopFrame(x, y, w, h)
		if err == nil && frame != nil {
			if !screenshot.FrameIsBlank(frame, 0) {
				return frame, nil
			}
			last = frame
			a.Logs.Append(fmt.Sprintf("[screenshot] tela congelada veio PRETA (tentativa %d/%d); repetindo a captura", attempt, attempts))
		} else {
			lastErr = err
			a.Logs.Append(fmt.Sprintf("[screenshot] captura do desktop falhou (tentativa %d/%d): %v", attempt, attempts, err))
		}
		if attempt < attempts {
			time.Sleep(120 * time.Millisecond)
		}
	}

	gdiFrame, gdiErr := screenshot.CaptureDesktopFrameGDI(x, y, w, h)
	if gdiErr == nil && gdiFrame != nil && !screenshot.FrameIsBlank(gdiFrame, 0) {
		a.Logs.Append("[screenshot] tela congelada obtida pelo caminho GDI (BitBlt) apos frames pretos do DXGI")
		return gdiFrame, nil
	}
	if gdiErr != nil {
		a.Logs.Append("[screenshot] fallback GDI (BitBlt) falhou: " + gdiErr.Error())
	} else if gdiFrame != nil {
		a.Logs.Append("[screenshot] fallback GDI (BitBlt) tambem retornou uma imagem preta")
	}

	if last != nil {
		return nil, fmt.Errorf("a captura do desktop retornou uma imagem preta (DXGI e BitBlt) — tente novamente")
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("nao foi possivel congelar a tela para a captura")
}

func (a *App) captureInteractive(ctx context.Context, reason string, byLLM bool, sessionAuthorized bool) (json.RawMessage, error) {
	session, err := a.openScreenshotOverlay(reason, byLLM, sessionAuthorized, screenshotDestinationLLM)
	if err != nil {
		return nil, err
	}
	if !byLLM {
		// Fluxo manual: o frontend conduz a seleção e chama Finish.
		payload, _ := json.Marshal(session.payload)
		return payload, nil
	}
	select {
	case out := <-session.waiter:
		if out.err != nil {
			return nil, out.err
		}
		out.result.Interactive = true
		// Captura interativa: o tempo é o da escolha do usuário, não do pipeline.
		a.recordScreenshot(out.result, reason, true, 0, session.authorized())
		payload := out.result.ToolPayload(toolNote(out.result), toolTarget(out.result))
		return json.Marshal(payload)
	case <-ctx.Done():
		a.releaseScreenshotOverlay(session.id, nil, ctx.Err())
		return nil, ctx.Err()
	}
}

func (a *App) finishScreenshotOverlay(sessionID, selectionJSON string) (*screenshot.CaptureResult, error) {
	a.screenshotMu.Lock()
	session := a.screenshotOverlay
	a.screenshotMu.Unlock()
	if session == nil || session.id != sessionID {
		// BUG CORRIGIDO: o front pode concluir a seleção de uma sessão que já
		// não existe (abortada ao fechar/ocultar a janela, ou de uma captura
		// concorrente). Antes o erro era devolvido SEM restaurar a janela: ela
		// continuava frameless/always-on-top nos bounds do overlay, cobrindo o
		// desktop inteiro — a "tela toda preta" que só voltava fechando e
		// reabrindo o agent pelo tray.
		if session == nil {
			a.restoreOrphanScreenshotOverlay()
		}
		return nil, fmt.Errorf("sessao de captura expirada ou invalida")
	}
	var sel screenshot.Selection
	if err := json.Unmarshal([]byte(selectionJSON), &sel); err != nil {
		a.releaseScreenshotOverlay(sessionID, nil, fmt.Errorf("selecao invalida: %w", err))
		return nil, fmt.Errorf("selecao de captura invalida: %w", err)
	}
	started := time.Now()
	res, err := a.captureFromSelection(session, sel)
	elapsed := time.Since(started)
	a.releaseScreenshotOverlay(sessionID, res, err)
	if err != nil {
		return nil, err
	}
	// Sessões pedidas pela IA registram a auditoria em captureInteractive (que
	// também recebe o resultado pelo waiter) — evita entrada duplicada.
	if !session.byLLM {
		a.recordScreenshot(res, session.reason, session.byLLM, elapsed, session.authorized())
	}
	return res, nil
}

func (a *App) captureFromSelection(session *screenshotOverlaySession, sel screenshot.Selection) (*screenshot.CaptureResult, error) {
	// Política primeiro: vale para qualquer caminho (anotado, janela ou recorte).
	if err := a.validateSelectionPolicy(session, sel); err != nil {
		return nil, err
	}
	// Imagem anotada (setas/caixas/texto desenhados no overlay) tem prioridade:
	// é exatamente o que o usuário viu e quer compartilhar.
	if strings.TrimSpace(sel.AnnotatedDataURL) != "" {
		// Qualidade de fallback 95 (lossy só quando o lossless não cabe): a imagem
		// anotada é o que o usuário leu na tela — não pode perder texto no reencode.
		data, mime, w, h, err := screenshot.DecodeDataURLImage(sel.AnnotatedDataURL, annotatedImageMaxDim, 95, a.screenshotImageFormat())
		if err != nil {
			return nil, fmt.Errorf("imagem anotada invalida: %w", err)
		}
		mode := screenshot.ModeRegion
		if strings.EqualFold(sel.Kind, "window") {
			mode = screenshot.ModeWindow
		}
		res := &screenshot.CaptureResult{
			Mode: mode, MIME: mime, Data: data,
			Width: w, Height: h,
			OriginX: sel.X, OriginY: sel.Y,
			Interactive: true, CapturedAt: time.Now(),
		}
		// Miniatura da auditoria/chat a partir do recorte da tela congelada: sem
		// isso toda captura manual com anotação ficava sem prévia no painel de
		// privacidade (o caminho anotado só existia como bytes PNG). O recorte é
		// barato; decodificar o PNG anotado custaria ~100 ms.
		if crop, cerr := screenshot.CropFrame(session.frame, sel.X-session.frame.OriginX, sel.Y-session.frame.OriginY, sel.Width, sel.Height); cerr == nil {
			screenshot.AttachThumbnailFrame(res, crop)
		}
		return res, nil
	}
	if strings.EqualFold(sel.Kind, "window") && sel.WindowHandle != 0 {
		// Blocklist já validada em validateSelectionPolicy. Formato vem da
		// política (png/webp/auto), igual aos demais caminhos — antes esta
		// captura ignorava a preferência da UI.
		if res, err := screenshot.CaptureScreen(screenshot.Request{
			Mode:         screenshot.ModeWindow,
			WindowHandle: sel.WindowHandle,
			Quality:      95,
			Format:       a.screenshotImageFormat(),
		}); err == nil {
			res.Interactive = true
			return res, nil
		}
		// Fallback: recorta a janela da tela congelada.
	}
	localX := sel.X - session.frame.OriginX
	localY := sel.Y - session.frame.OriginY
	cropped, err := screenshot.CropFrame(session.frame, localX, localY, sel.Width, sel.Height)
	if err != nil {
		return nil, err
	}
	data, mime, err := screenshot.EncodeFrameFormat(cropped, 95, 0, a.screenshotImageFormat())
	if err != nil {
		return nil, err
	}
	mode := screenshot.ModeRegion
	if strings.EqualFold(sel.Kind, "window") {
		mode = screenshot.ModeWindow
	}
	res := &screenshot.CaptureResult{
		Mode: mode, MIME: mime, Data: data,
		Width: cropped.Width, Height: cropped.Height,
		OriginX: sel.X, OriginY: sel.Y,
		Interactive: true, CapturedAt: time.Now(),
	}
	// Miniatura do recorte (auditoria/chat) — o caminho de região montava o
	// resultado à mão e ficava sem prévia no painel de privacidade.
	screenshot.AttachThumbnailFrame(res, cropped)
	return res, nil
}

// restoreOrphanScreenshotOverlay devolve a janela principal ao estado normal
// quando NÃO existe sessão ativa mas o overlay continua registrado (o front
// chamou Finish/Cancel com um id que o backend já não tem).
//
// É a defesa contra a "tela toda preta": a sessão morre sem release, a janela
// fica frameless/always-on-top nos bounds do overlay cobrindo o desktop virtual
// e o usuário só recupera fechando e reabrindo o agent pelo tray (o hook de
// WindowClosing abortava a sessão e restaurava). Aqui a restauração acontece no
// próprio erro devolvido ao front.
func (a *App) restoreOrphanScreenshotOverlay() {
	a.screenshotMu.Lock()
	if a.screenshotOverlay != nil {
		// Há sessão viva: os bounds do overlay são legítimos (o id que não bate
		// é que era velho). Não mexer.
		a.screenshotMu.Unlock()
		return
	}
	restore := a.screenshotRestore
	a.screenshotRestore = nil
	a.screenshotMu.Unlock()
	if restore == nil {
		return
	}
	// Fora do lock: o restore faz chamadas de janela (Wails/Win32).
	restore()
	a.Logs.Append("[screenshot] sessao inexistente no Finish/Cancel: bounds da janela restaurados")
}

// releaseScreenshotOverlay restaura a janela, limpa a sessão e entrega o
// resultado ao waiter (fluxo da IA). Idempotente por sessão.
func (a *App) releaseScreenshotOverlay(sessionID string, res *screenshot.CaptureResult, resErr error) {
	a.screenshotMu.Lock()
	session := a.screenshotOverlay
	if session == nil || session.id != sessionID {
		a.screenshotMu.Unlock()
		return
	}
	a.screenshotMu.Unlock()

	session.once.Do(func() {
		if session.restore != nil {
			session.restore()
		}
		a.screenshotMu.Lock()
		if a.screenshotOverlay == session {
			a.screenshotOverlay = nil
			a.screenshotRestore = nil
		}
		a.screenshotMu.Unlock()
		closeJSON, _ := json.Marshal(map[string]any{"session": session.id, "ok": resErr == nil})
		a.EmitEvent("screenshot:overlay_close", string(closeJSON))
		a.PublishChatEvent("screenshot:overlay_close", string(closeJSON))
		select {
		case session.waiter <- screenshotOverlayOutcome{result: res, err: resErr}:
		default:
		}
	})
}
