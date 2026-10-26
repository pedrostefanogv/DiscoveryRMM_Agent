//go:build windows

package notifyui

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
)

// ── Janela nativa de progresso ──
//
// Substituto do Show-ADTInstallationProgress: cartão compacto topmost com
// barra determinada (0..100%) ou indeterminada (animação), tema do Windows.
// Fecha sozinha após DurationSeconds ou pelo contexto.

const (
	progressClassName  = "DiscoveryRmmNotifyProgress"
	progressWidth      = 560
	progressRadius     = 16
	progressPad        = 24
	progressTimerID    = 1
	progressTickMS     = 100
	progressWMAppClose = 0x8000 + 44
)

// progressLayout devolve os retângulos em pixels físicos.
func progressLayout(scale float64, extraHeight int) (badge, title, subtitle, divider, messageBox, statusText, statusDetail, progressTrack, percentLabel winui.Rect) {
	if scale <= 0 {
		scale = 1
	}
	sw := func(v int) int { return winui.ScalePx(v, scale) }
	badge = winui.Rect{X: sw(progressPad), Y: sw(20), W: sw(40), H: sw(40)}
	headerLeft := badge.Right() + sw(14)
	headerWidth := sw(progressWidth) - headerLeft - sw(progressPad)
	title = winui.Rect{X: headerLeft, Y: sw(20), W: headerWidth, H: sw(28)}
	subtitle = winui.Rect{X: headerLeft, Y: sw(52), W: headerWidth, H: sw(18)}
	divider = winui.Rect{X: sw(progressPad), Y: sw(86), W: sw(progressWidth) - 2*sw(progressPad), H: 1}
	messageBox = winui.Rect{X: sw(progressPad), Y: sw(98), W: sw(progressWidth) - 2*sw(progressPad), H: sw(64) + extraHeight}
	statusText = messageBox.Inset(sw(14), sw(8))
	statusDetail = winui.Rect{X: statusText.X, Y: statusText.Bottom() + sw(4), W: statusText.W, H: sw(16)}
	progressTrack = winui.Rect{X: sw(progressPad), Y: messageBox.Bottom() + sw(16), W: sw(progressWidth) - 2*sw(progressPad), H: sw(7)}
	percentLabel = winui.Rect{X: sw(progressPad), Y: progressTrack.Bottom() + sw(6), W: sw(progressWidth) - 2*sw(progressPad), H: sw(16)}
	return
}

// progressHeight devolve a altura total para o layout.
func progressHeight(percentLabel winui.Rect, scale float64) int {
	return percentLabel.Bottom() + winui.ScalePx(progressPad, scale)
}

type progressFonts struct {
	title, subtitle, body, detail, percent uintptr
}

func (f *progressFonts) release() {
	for _, h := range []uintptr{f.title, f.subtitle, f.body, f.detail, f.percent} {
		if h != 0 {
			winui.DeleteGDIObject(h)
		}
	}
}

type progressState struct {
	req      ProgressRequest
	start    time.Time
	deadline time.Time
	fonts    progressFonts
	layout   progressLayoutFn
	theme    winui.Theme
	scale    float64
}

// progressLayoutFn evita um tipo nomeado redundante (assinatura do layout).
type progressLayoutFn = func(scale float64, extraHeight int) (badge, title, subtitle, divider, messageBox, statusText, statusDetail, progressTrack, percentLabel winui.Rect)

var (
	progressClassOnce sync.Once
	progressClassErr  error
	progressStates    sync.Map
)

func ensureProgressClass() error {
	progressClassOnce.Do(func() {
		progressClassErr = winui.RegisterWindowClass(
			progressClassName,
			windows.NewCallback(progressWndProc),
			winui.ClassStyleDropShadow,
		)
	})
	return progressClassErr
}

func progressWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	raw, ok := progressStates.Load(hwnd)
	if !ok {
		return winui.DefWindowProc(hwnd, msg, wparam, lparam)
	}
	state, _ := raw.(*progressState)

	switch msg {
	case winui.MsgEraseBkgnd:
		return 1
	case winui.MsgPaint:
		hdc, paint := winui.BeginPaint(hwnd)
		state.paint(hdc)
		winui.EndPaint(hwnd, &paint)
		return 0
	case winui.MsgPrintClient:
		state.paint(wparam)
		return 0
	case winui.MsgTimer:
		if state.req.DurationSeconds > 0 && time.Now().After(state.deadline) {
			progressStates.Delete(hwnd)
			winui.KillTimer(hwnd, progressTimerID)
			winui.DestroyWindow(hwnd)
			return 0
		}
		winui.Invalidate(hwnd)
		return 0
	case progressWMAppClose:
		progressStates.Delete(hwnd)
		winui.KillTimer(hwnd, progressTimerID)
		winui.DestroyWindow(hwnd)
		return 0
	case winui.MsgDestroy:
		state.fonts.release()
		progressStates.Delete(hwnd)
		winui.PostQuitMessage(0)
		return 0
	}
	return winui.DefWindowProc(hwnd, msg, wparam, lparam)
}

// progressRatio devolve o avanço do tempo (1.0 = completo).
func (s *progressState) progressRatio() float64 {
	if s.req.DurationSeconds <= 0 {
		return 0
	}
	elapsed := time.Since(s.start)
	ratio := elapsed / (time.Duration(s.req.DurationSeconds) * time.Second)
	return math.Min(1, math.Max(0, float64(ratio)))
}

// indeterminatePhase devolve 0..1 para a animação da barra indeterminada.
func (s *progressState) indeterminatePhase() float64 {
	phase := (float64(time.Since(s.start).Milliseconds()) / 1200.0) / (2 * math.Pi)
	return phase - math.Floor(phase)
}

func (s *progressState) paint(screenDC uintptr) {
	scale := s.scale
	width := winui.ScalePx(progressWidth, scale)
	badge, title, subtitle, divider, messageBox, statusText, statusDetail, progressTrack, percentLabel :=
		s.layout(scale, 0)
	height := progressHeight(percentLabel, scale)

	memDC, bitmap, oldBitmap := winui.BufferedPaint(screenDC, width, height)
	hdc := memDC
	if hdc == 0 {
		hdc = screenDC
	} else {
		defer winui.FlushBufferedPaint(screenDC, memDC, bitmap, oldBitmap, width, height)
	}

	theme := s.theme
	card := winui.Rect{W: width, H: height}
	cardRadius := winui.ScalePx(progressRadius, scale)
	winui.FillRoundRectAA(hdc, card, cardRadius, theme.Card)
	accentMargin := winui.ScalePx(10, scale)
	winui.FillRoundRectAA(hdc,
		winui.Rect{X: winui.ScalePx(3, scale), Y: accentMargin, W: winui.ScalePx(4, scale), H: height - 2*accentMargin},
		winui.ScalePx(2, scale), theme.Accent)
	winui.StrokeRoundRectAA(hdc, card, cardRadius, 1, theme.Border)

	drawCardBadge(hdc, badge, theme, scale)
	winui.DrawTextSingle(hdc, s.req.Title, title, s.fonts.title, theme.Text, winui.AlignLeft)
	winui.DrawTextSingle(hdc, s.req.Subtitle, subtitle, s.fonts.subtitle, theme.TextMuted, winui.AlignLeft)
	winui.FillRect(hdc, divider, theme.Border)
	winui.FillRoundRectBorderAA(hdc, messageBox, winui.ScalePx(8, scale), theme.Surface, theme.Border)
	winui.DrawTextWrappedEllipsis(hdc, s.req.StatusMessage, statusText, s.fonts.body, theme.Text, winui.AlignLeft)
	winui.DrawTextSingle(hdc, s.req.StatusMessageDetail, statusDetail, s.fonts.detail, theme.TextMuted, winui.AlignLeft)

	winui.FillRoundRectAA(hdc, progressTrack, winui.ScalePx(4, scale), theme.Border)
	bar := progressTrack
	if s.req.IsIndeterminate() {
		// Marquee: 35% da trilha deslizando da esquerda para a direita.
		barW := progressTrack.W * 35 / 100
		phase := s.indeterminatePhase()
		bar.X = progressTrack.X + int(float64(progressTrack.W-barW)*phase)
		bar.W = barW
	} else {
		bar.W = int(float64(progressTrack.W) * float64(s.req.Percent) / 100)
		if bar.W < winui.ScalePx(6, scale) {
			bar.W = winui.ScalePx(6, scale)
		}
		if bar.W > progressTrack.W {
			bar.W = progressTrack.W
		}
	}
	winui.FillRoundRectAA(hdc, bar, winui.ScalePx(4, scale), theme.Accent)

	percentText := "Aguarde..."
	if !s.req.IsIndeterminate() {
		percentText = fmt.Sprintf("%d%% concluído", s.req.Percent)
	}
	winui.DrawTextSingle(hdc, percentText, percentLabel, s.fonts.percent, theme.TextMuted, winui.AlignLeft)
}

// ShowProgress abre a janela de progresso e aguarda o desfecho
// (tempo esgotado ou contexto cancelado).
func ShowProgress(ctx context.Context, req ProgressRequest) (ProgressResult, bool) {
	req = NormalizeProgressRequest(req)
	if err := ensureProgressClass(); err != nil {
		return ProgressResult{Outcome: ProgressError, Reason: err.Error()}, false
	}
	if ctx.Err() != nil {
		return ProgressResult{Outcome: ProgressCanceled}, true
	}
	resultCh := make(chan ProgressResult, 1)
	go func() {
		resultCh <- runProgressWindow(ctx, req)
	}()
	return <-resultCh, true
}

func runProgressWindow(ctx context.Context, req ProgressRequest) ProgressResult {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	scale0 := 1.0
	_, _, _, _, _, _, _, _, percent0 := progressLayout(scale0, 0)
	height0 := progressHeight(percent0, scale0)

	x, y := winui.BottomRightOnActiveWorkArea(progressWidth, height0, progressPad)
	hwnd, err := winui.CreateWindow(
		progressClassName,
		req.Title,
		winui.ExStyleTopmost|winui.ExStyleToolWindow,
		winui.WinStylePopup|winui.WinStyleClipSiblings,
		x, y, progressWidth, height0,
	)
	if err != nil {
		return ProgressResult{Outcome: ProgressError, Reason: err.Error()}
	}

	scale := winui.ScaleForDPI(winui.DpiForWindow(hwnd))

	// Crescimento dinâmico do texto de status.
	bodyFont := winui.CreateFont(winui.ScalePx(15, scale), 400)
	_, _, _, _, _, statusTextRect, _, _, _ := progressLayout(scale, 0)
	extraHeight := 0
	if needed := winui.MeasureWrappedText(bodyFont, req.StatusMessage, statusTextRect.W); needed > statusTextRect.H {
		extraHeight = needed - statusTextRect.H
		if maxExtra := winui.ScalePx(cardMessageMaxExtraPx, scale); extraHeight > maxExtra {
			extraHeight = maxExtra
		}
	}

	state := &progressState{
		req:    req,
		start:  time.Now(),
		theme:  winui.DetectTheme(),
		scale:  scale,
	}
	if req.DurationSeconds > 0 {
		state.deadline = state.start.Add(time.Duration(req.DurationSeconds) * time.Second)
	}
	state.layout = func(s float64, extra int) (badge, title, subtitle, divider, messageBox, statusText, statusDetail, progressTrack, percentLabel winui.Rect) {
		return progressLayout(s, extraHeight)
	}
	state.fonts = progressFonts{
		title:   winui.CreateFont(winui.ScalePx(22, scale), 600),
		subtitle: winui.CreateFont(winui.ScalePx(14, scale), 400),
		body:    bodyFont,
		detail:  winui.CreateFont(winui.ScalePx(12, scale), 400),
		percent: winui.CreateFont(winui.ScalePx(12, scale), 400),
	}

	progressStates.Store(hwnd, state)

	winui.ShowWindow(hwnd, winui.SwShowNormal)
	winui.EnsureTopMost(hwnd)
	winui.UpdateWindow(hwnd)
	winui.SetTimer(hwnd, progressTimerID, progressTickMS)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			winui.PostMessage(hwnd, progressWMAppClose, 0, 0)
		case <-done:
		}
	}()

	var msg winui.Msg
	for winui.NextMessage(&msg) > 0 {
		winui.TranslateMessage(&msg)
		winui.DispatchMessage(&msg)
	}

	if ctx.Err() != nil {
		return ProgressResult{Outcome: ProgressCanceled}
	}
	return ProgressResult{Outcome: ProgressCompleted}
}
