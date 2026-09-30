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
	screenshotConsentFile = "screenshot_consent.json"
	screenshotPolicyFile  = "screenshot_policy.json"
	// screenshotAuditThumbLimit limita quantas miniaturas a UI de privacidade
	// recebe de uma vez (payload do binding).
	screenshotAuditThumbLimit = 12
)

// screenshotOverlaySession é a sessão ativa do overlay de captura.
type screenshotOverlaySession struct {
	id      string
	reason  string
	byLLM   bool
	payload *screenshot.OverlayPayload
	// frame é o print do desktop em resolução física cheia, usado para recortar
	// exatamente o que o usuário viu (tela congelada).
	frame   *screen.Frame
	restore func()
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
	a.screenshotConsent = screenshot.NewConsentManager(
		func(ctx context.Context, question string, options []string) (string, error) {
			opts, _ := json.Marshal(options)
			return a.AskUserChatWithContext(ctx, question, string(opts), "false")
		},
		screenshot.Persist{Load: a.loadScreenshotConsent, Save: a.saveScreenshotConsent},
		func(line string) { a.Logs.Append(line) },
		applocale.DetectPreferredLocale,
	)
	policy := a.loadScreenshotPolicyOrDefault()
	a.screenshotPolicy = policy
	a.screenshotLimiter = screenshot.NewCaptureLimiter(policy.LimitMax(), policy.LimitWindow())
}

// ── Política de captura (privacidade) ─────────────────────────────────────

func (a *App) screenshotPolicyPaths() []string {
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
	policy = policy.Normalize()
	if err := a.saveScreenshotPolicyFile(policy); err != nil {
		return err
	}
	a.screenshotMu.Lock()
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
		if wins, err := screenshot.ListWindowsWithOptions(true); err == nil {
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
			if wins, err := screenshot.ListWindowsWithOptions(true); err == nil {
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
			if wins, err := screenshot.ListWindowsWithOptions(true); err == nil {
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

func (a *App) screenshotConsentPaths() []string {
	return platform.ChatConfigPathCandidates(screenshotConsentFile)
}

func (a *App) loadScreenshotConsent() (screenshot.Decision, error) {
	for _, path := range a.screenshotConsentPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg struct {
			Decision string `json:"decision"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if cfg.Decision != "" {
			return screenshot.Decision(cfg.Decision), nil
		}
	}
	return screenshot.DecisionUndecided, nil
}

func (a *App) saveScreenshotConsent(d screenshot.Decision) error {
	data, err := json.MarshalIndent(map[string]string{"decision": string(d)}, "", "  ")
	if err != nil {
		return err
	}
	var lastErr error
	for _, path := range a.screenshotConsentPaths() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			lastErr = err
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			lastErr = err
			continue
		}
		if platform.IsElevated() {
			_ = platform.HardenSecretFileACL(path)
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nenhum caminho valido para persistir o consentimento")
	}
	return lastErr
}

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

// ScreenshotConsentStatusJSON implementa AppBridge: estado de autorização.
func (a *App) ScreenshotConsentStatusJSON() (json.RawMessage, error) {
	a.initScreenshotService()
	return json.Marshal(map[string]any{
		"decision":   string(a.screenshotConsent.Status()),
		"authorized": a.screenshotConsent.Authorized(),
		"audit":      a.screenshotConsent.Audit(),
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
	if _, err := a.screenshotConsent.Ensure(ctx, reason); err != nil {
		return nil, err
	}
	// Política local (blocklist/modo) e cota de capturas antes de executar.
	policy := a.currentScreenshotPolicy()
	if err := a.applyScreenshotPolicy(&req, policy); err != nil {
		return nil, err
	}
	now := time.Now()
	if err := a.checkScreenshotBudget(now); err != nil {
		return nil, err
	}
	result, err := a.runScreenshotRequest(ctx, req, reason, true)
	if err != nil {
		return nil, err
	}
	a.recordScreenshotBudget(now)
	return result, nil
}

func (a *App) runScreenshotRequest(ctx context.Context, req screenshot.Request, reason string, byLLM bool) (json.RawMessage, error) {
	if strings.EqualFold(req.Mode, screenshot.ModeInteractive) {
		return a.captureInteractive(ctx, reason, byLLM)
	}
	// A janela do agente é ocultada durante a captura para não sair no print.
	restoreVisibility := a.hideAgentWindowForCapture()
	defer restoreVisibility()
	res, err := screenshot.CaptureScreen(req)
	if err != nil {
		return nil, err
	}
	id := a.recordScreenshot(res, reason, byLLM)
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

// recordScreenshot registra a captura na auditoria, guarda a imagem cheia em
// memória (lightbox) e devolve o ID da entrada — 0 quando não registrada.
func (a *App) recordScreenshot(res *screenshot.CaptureResult, reason string, byLLM bool) int64 {
	if a.screenshotConsent == nil || res == nil {
		return 0
	}
	detail := res.Describe()
	if r := strings.TrimSpace(reason); r != "" {
		detail += " | motivo: " + r
	}
	entry := a.screenshotConsent.RecordCapture(screenshot.AuditEntry{
		Decision:  a.screenshotConsent.Status(),
		Mode:      res.Mode,
		Detail:    detail,
		ByLLM:     byLLM,
		Bytes:     len(res.Data),
		Thumbnail: res.Thumbnail,
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
	// overlay (o canvas já entrega reduzido; aqui é a última defesa).
	annotatedImageMaxDim = 1600
	// overlayImageMaxDim é o lado maior da imagem congelada exibida no overlay
	// (fundo da seleção/anotação).
	overlayImageMaxDim = 2560
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

// ── Overlay de seleção (tela congelada) ───────────────────────────────────

// PrepareScreenshotOverlay captura o desktop, coloca a janela principal em
// modo overlay cobrindo o desktop virtual e devolve o payload para a UI.
func (a *App) PrepareScreenshotOverlay(reason string) (string, error) {
	a.initScreenshotService()
	session, err := a.openScreenshotOverlay(strings.TrimSpace(reason), false)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(session.payload)
	if err != nil {
		return "", err
	}
	return string(payload), nil
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

// SetScreenshotPermission altera a autorização (UI: conceder/revogar).
func (a *App) SetScreenshotPermission(decision string) error {
	a.initScreenshotService()
	return a.screenshotConsent.Set(screenshot.Decision(strings.ToLower(strings.TrimSpace(decision))))
}

// screenshotAuditView é um item da auditoria exibido na UI de privacidade.
type screenshotAuditView struct {
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	Decision  string    `json:"decision"`
	Mode      string    `json:"mode"`
	Detail    string    `json:"detail"`
	ByLLM     bool      `json:"byLlm"`
	Bytes     int       `json:"bytes"`
	Thumbnail string    `json:"thumbnail,omitempty"`
}

// GetScreenshotAuditPanel devolve consentimento + política + uso do limite +
// auditoria (com miniaturas das últimas capturas) para o painel de privacidade.
func (a *App) GetScreenshotAuditPanel() (string, error) {
	a.initScreenshotService()
	policy := a.currentScreenshotPolicy()
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
		}
		if len(entry.Thumbnail) > 0 {
			view.Thumbnail = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(entry.Thumbnail)
		}
		views = append(views, view)
	}

	now := time.Now()
	body, err := json.Marshal(map[string]any{
		"decision":   string(a.screenshotConsent.Status()),
		"authorized": a.screenshotConsent.Authorized(),
		"locale":     applocale.DetectPreferredLocale(),
		"policy":     policy.Normalize(),
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
	a.screenshotMu.Unlock()
	if session == nil {
		return
	}
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

func (a *App) openScreenshotOverlay(reason string, byLLM bool) (*screenshotOverlaySession, error) {
	a.screenshotMu.Lock()
	if a.screenshotOverlay != nil {
		active := a.screenshotOverlay
		a.screenshotMu.Unlock()
		if active.byLLM == byLLM {
			return active, nil
		}
		return nil, fmt.Errorf("ja existe uma captura de tela em andamento")
	}
	a.screenshotMu.Unlock()

	if a.mainWindow == nil {
		return nil, fmt.Errorf("janela principal indisponivel para o overlay")
	}
	vx, vy, vw, vh, ok := screenshot.VirtualBounds()
	if !ok {
		return nil, fmt.Errorf("nao foi possivel determinar o desktop virtual")
	}
	// A janela do agente é ocultada ANTES do print (não sai na tela congelada)
	// e reaparece já como overlay, posicionada sobre o desktop virtual.
	restoreVisibility := a.hideAgentWindowForCapture()
	frame, err := a.captureDesktopFrame(vx, vy, vw, vh)
	if err != nil {
		restoreVisibility()
		return nil, err
	}
	overlayBytes, mime, err := screenshot.EncodeFrame(frame, 78, overlayImageMaxDim)
	if err != nil {
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
		Monitors:       screenshot.ListMonitors(),
		Windows:        wins,
		RequestedByLLM: byLLM,
		// A UI usa isso para não oferecer "Tela inteira" quando a política proíbe.
		PolicyFullScreenAllowed: policy.FullScreenAllowed(),
		PolicyWindowRequired:    policy.WindowRequired(),
	}
	restore, err := a.enterScreenshotOverlayBounds(vx, vy, vw, vh)
	if err != nil {
		restoreVisibility()
		return nil, err
	}
	// Garante a janela visível já como overlay (o SetWindowPos acima usa
	// SWP_SHOWWINDOW; o Show é idempotente e cobre o caso de hide pendente).
	restoreVisibility()
	session := &screenshotOverlaySession{
		id:      id,
		reason:  reason,
		byLLM:   byLLM,
		payload: payload,
		frame:   frame,
		restore: restore,
		waiter:  make(chan screenshotOverlayOutcome, 1),
	}
	a.screenshotMu.Lock()
	a.screenshotOverlay = session
	a.screenshotMu.Unlock()

	// O evento só é necessário quando a IA pediu a seleção: o fluxo manual
	// recebe o payload na resposta do binding. Emitir no fluxo manual deixava um
	// evento órfão no broker, entregue no próximo poll e capaz de reabrir o
	// overlay com uma sessão já encerrada.
	if byLLM {
		payloadJSON, _ := json.Marshal(payload)
		a.EmitEvent("screenshot:request", string(payloadJSON))
		a.PublishChatEvent("screenshot:request", string(payloadJSON))
	}
	return session, nil
}

func (a *App) captureDesktopFrame(x, y, w, h int) (*screen.Frame, error) {
	c, err := screen.NewGDICapturerRegion(x, y, w, h)
	if err != nil {
		return nil, fmt.Errorf("captura do desktop para o overlay: %w", err)
	}
	defer c.Close()
	frame, err := c.AcquireNextFrame()
	if err != nil {
		return nil, err
	}
	stride := frame.Stride
	if stride <= 0 {
		stride = frame.Width * 4
	}
	// OriginX/OriginY são essenciais: o recorte da seleção usa a origem do
	// frame para converter coordenadas físicas do desktop virtual em locais.
	// Sem copiá-los, multi-monitor com origem != (0,0) recortava a região errada.
	cp := &screen.Frame{
		Data:    append([]byte(nil), frame.Data...),
		Width:   frame.Width,
		Height:  frame.Height,
		Stride:  stride,
		OriginX: frame.OriginX,
		OriginY: frame.OriginY,
	}
	c.ReleaseFrame()
	return cp, nil
}

func (a *App) captureInteractive(ctx context.Context, reason string, byLLM bool) (json.RawMessage, error) {
	session, err := a.openScreenshotOverlay(reason, byLLM)
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
		a.recordScreenshot(out.result, reason, true)
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
		return nil, fmt.Errorf("sessao de captura expirada ou invalida")
	}
	var sel screenshot.Selection
	if err := json.Unmarshal([]byte(selectionJSON), &sel); err != nil {
		a.releaseScreenshotOverlay(sessionID, nil, fmt.Errorf("selecao invalida: %w", err))
		return nil, fmt.Errorf("selecao de captura invalida: %w", err)
	}
	res, err := a.captureFromSelection(session, sel)
	a.releaseScreenshotOverlay(sessionID, res, err)
	if err != nil {
		return nil, err
	}
	// Sessões pedidas pela IA registram a auditoria em captureInteractive (que
	// também recebe o resultado pelo waiter) — evita entrada duplicada.
	if !session.byLLM {
		a.recordScreenshot(res, session.reason, session.byLLM)
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
		data, mime, w, h, err := screenshot.DecodeDataURLImage(sel.AnnotatedDataURL, annotatedImageMaxDim, 82, 0)
		if err != nil {
			return nil, fmt.Errorf("imagem anotada invalida: %w", err)
		}
		mode := screenshot.ModeRegion
		if strings.EqualFold(sel.Kind, "window") {
			mode = screenshot.ModeWindow
		}
		return &screenshot.CaptureResult{
			Mode: mode, MIME: mime, Data: data,
			Width: w, Height: h,
			OriginX: sel.X, OriginY: sel.Y,
			Interactive: true, CapturedAt: time.Now(),
		}, nil
	}
	if strings.EqualFold(sel.Kind, "window") && sel.WindowHandle != 0 {
		// Blocklist já validada em validateSelectionPolicy.
		if res, err := screenshot.CaptureWindow(sel.WindowHandle, 80, 0); err == nil {
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
	data, mime, err := screenshot.EncodeFrame(cropped, 80, 0)
	if err != nil {
		return nil, err
	}
	mode := screenshot.ModeRegion
	if strings.EqualFold(sel.Kind, "window") {
		mode = screenshot.ModeWindow
	}
	return &screenshot.CaptureResult{
		Mode: mode, MIME: mime, Data: data,
		Width: cropped.Width, Height: cropped.Height,
		OriginX: sel.X, OriginY: sel.Y,
		Interactive: true, CapturedAt: time.Now(),
	}, nil
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
