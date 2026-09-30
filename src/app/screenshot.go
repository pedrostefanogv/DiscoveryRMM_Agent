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
)

// ── Captura de tela assistida (chat IA) ───────────────────────────────────
//
// Liga o pacote core/screenshot ao app: consentimento, bindings do overlay de
// seleção, tools MCP e trilha de auditoria.

const screenshotConsentFile = "screenshot_consent.json"

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
	)
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

// ListOpenWindowsJSON implementa AppBridge: janelas visíveis + monitores.
func (a *App) ListOpenWindowsJSON() (json.RawMessage, error) {
	wins, err := screenshot.ListWindows()
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"ok":       true,
		"count":    len(wins),
		"windows":  wins,
		"monitors": screenshot.ListMonitors(),
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
	return a.runScreenshotRequest(ctx, req, reason, true)
}

func (a *App) runScreenshotRequest(ctx context.Context, req screenshot.Request, reason string, byLLM bool) (json.RawMessage, error) {
	if strings.EqualFold(req.Mode, screenshot.ModeInteractive) {
		return a.captureInteractive(ctx, reason, byLLM)
	}
	res, err := screenshot.CaptureScreen(req)
	if err != nil {
		return nil, err
	}
	a.recordScreenshot(res, reason, byLLM)
	// Transparência: mostra no chat o que a IA acabou de capturar (miniatura),
	// já que uma captura automática não passa pelo overlay de seleção.
	a.emitScreenshotCaptured(res, reason, byLLM)
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

func (a *App) recordScreenshot(res *screenshot.CaptureResult, reason string, byLLM bool) {
	if a.screenshotConsent == nil || res == nil {
		return
	}
	detail := res.Describe()
	if r := strings.TrimSpace(reason); r != "" {
		detail += " | motivo: " + r
	}
	a.screenshotConsent.RecordCapture(screenshot.AuditEntry{
		Decision: a.screenshotConsent.Status(),
		Mode:     res.Mode,
		Detail:   detail,
		ByLLM:    byLLM,
		Bytes:    len(res.Data),
	})
	a.Logs.Append("[screenshot] " + detail)
}

// emitScreenshotCaptured publica no chat a miniatura de uma captura concluída
// (o LLM recebe a imagem cheia no tool result; o usuário vê o que foi capturado).
func (a *App) emitScreenshotCaptured(res *screenshot.CaptureResult, reason string, byLLM bool) {
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

// parseScreenshotArgs normaliza os argumentos JSON da tool capture_screenshot.
func parseScreenshotArgs(args map[string]any) (screenshot.Request, string, error) {
	req := screenshot.Request{Mode: strings.ToLower(screenshotStringArg(args, "mode"))}
	if req.Mode == "" {
		req.Mode = screenshot.ModeInteractive
	}
	switch req.Mode {
	case screenshot.ModeFull, screenshot.ModeInteractive:
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
		return req, "", fmt.Errorf("modo de captura invalido: %q (use full, window, monitor, region ou interactive)", req.Mode)
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

// GetOpenWindows devolve as janelas visíveis (binding de UI).
func (a *App) GetOpenWindows() (string, error) {
	b, err := a.ListOpenWindowsJSON()
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
	frame, err := a.captureDesktopFrame(vx, vy, vw, vh)
	if err != nil {
		return nil, err
	}
	overlayBytes, mime, err := screenshot.EncodeFrame(frame, 78, 2560)
	if err != nil {
		return nil, err
	}
	wins, _ := screenshot.ListWindows()
	id := fmt.Sprintf("shot-%d", time.Now().UnixNano())
	payload := &screenshot.OverlayPayload{
		Session:        id,
		At:             time.Now().UTC(),
		Reason:         reason,
		VirtualX:       vx,
		VirtualY:       vy,
		VirtualWidth:   vw,
		VirtualHeight:  vh,
		ImageWidth:     frame.Width,
		ImageHeight:    frame.Height,
		ImageDataURL:   "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(overlayBytes),
		Monitors:       screenshot.ListMonitors(),
		Windows:        wins,
		RequestedByLLM: byLLM,
	}
	restore, err := a.enterScreenshotOverlayBounds(vx, vy, vw, vh)
	if err != nil {
		return nil, err
	}
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
	if strings.EqualFold(sel.Kind, "window") && sel.WindowHandle != 0 {
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
