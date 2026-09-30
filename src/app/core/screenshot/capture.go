package screenshot

import (
	"fmt"
	"strings"
	"time"

	"discovery/app/core/screen"
)

// ListMonitors retorna os monitores no desktop virtual (coordenadas físicas).
func ListMonitors() []MonitorInfo {
	mons, err := screen.GetMonitors()
	if err != nil {
		return nil
	}
	out := make([]MonitorInfo, 0, len(mons))
	for _, m := range mons {
		out = append(out, MonitorInfo{
			Index: m.Index, X: m.X, Y: m.Y, Width: m.Width, Height: m.Height,
			Name: m.Name, Primary: m.IsPrimary,
		})
	}
	return out
}

// VirtualBounds retorna a união de todos os monitores (coordenadas físicas).
func VirtualBounds() (x, y, w, h int, ok bool) {
	if bx, by, bw, bh, vok := screen.VirtualDesktopBounds(); vok {
		return bx, by, bw, bh, true
	}
	mons := ListMonitors()
	if len(mons) == 0 {
		return 0, 0, 0, 0, false
	}
	minX, minY := mons[0].X, mons[0].Y
	maxX, maxY := mons[0].X+mons[0].Width, mons[0].Y+mons[0].Height
	for _, m := range mons[1:] {
		if m.X < minX {
			minX = m.X
		}
		if m.Y < minY {
			minY = m.Y
		}
		if m.X+m.Width > maxX {
			maxX = m.X + m.Width
		}
		if m.Y+m.Height > maxY {
			maxY = m.Y + m.Height
		}
	}
	return minX, minY, maxX - minX, maxY - minY, true
}

// ClampRegion valida/limita uma região ao desktop virtual.
func ClampRegion(x, y, w, h int) (int, int, int, int, error) {
	vx, vy, vw, vh, ok := VirtualBounds()
	if !ok {
		return 0, 0, 0, 0, fmt.Errorf("nao foi possivel determinar os limites do desktop")
	}
	if w <= 0 || h <= 0 {
		return 0, 0, 0, 0, fmt.Errorf("regiao de captura invalida (%dx%d)", w, h)
	}
	if x < vx {
		w -= vx - x
		x = vx
	}
	if y < vy {
		h -= vy - y
		y = vy
	}
	if x+w > vx+vw {
		w = vx + vw - x
	}
	if y+h > vy+vh {
		h = vy + vh - y
	}
	if w <= 0 || h <= 0 {
		return 0, 0, 0, 0, fmt.Errorf("regiao de captura fora do desktop")
	}
	return x, y, w, h, nil
}

// captureWithCapturer adquire um frame, copia os bytes (os capturers reusam o
// buffer) e encerra o capturador.
func captureWithCapturer(c screen.Capturer) (*screen.Frame, error) {
	if c == nil {
		return nil, fmt.Errorf("capturador indisponivel")
	}
	defer c.Close()
	frame, err := c.AcquireNextFrame()
	if err != nil {
		return nil, err
	}
	if frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		c.ReleaseFrame()
		return nil, fmt.Errorf("capturador retornou frame vazio")
	}
	stride := frame.Stride
	if stride <= 0 {
		stride = frame.Width * 4
	}
	copyFrame := &screen.Frame{
		Data:       append([]byte(nil), frame.Data...),
		Width:      frame.Width,
		Height:     frame.Height,
		Stride:     stride,
		OriginX:    frame.OriginX,
		OriginY:    frame.OriginY,
		ColorSpace: frame.ColorSpace,
	}
	c.ReleaseFrame()
	return copyFrame, nil
}

// CaptureMonitor captura um monitor completo.
func CaptureMonitor(monitorIndex, quality, maxDim int) (*CaptureResult, error) {
	mons := ListMonitors()
	if len(mons) == 0 {
		return nil, fmt.Errorf("nenhum monitor detectado")
	}
	if monitorIndex < 0 || monitorIndex >= len(mons) {
		monitorIndex = 0
	}
	c, err := screen.NewCapturerMode(monitorIndex, true)
	if err != nil {
		return nil, fmt.Errorf("captura do monitor %d: %w", monitorIndex, err)
	}
	frame, err := captureWithCapturer(c)
	if err != nil {
		return nil, fmt.Errorf("captura do monitor %d: %w", monitorIndex, err)
	}
	return encodeResult(frame, ModeMonitor, monitorIndex, quality, maxDim)
}

// CaptureDesktop captura o desktop virtual inteiro (todos os monitores).
func CaptureDesktop(quality, maxDim int) (*CaptureResult, error) {
	x, y, w, h, ok := VirtualBounds()
	if !ok {
		return nil, fmt.Errorf("nao foi possivel determinar os limites do desktop")
	}
	return captureRegionRaw(x, y, w, h, ModeFull, quality, maxDim)
}

// CaptureRegion captura uma região arbitrária do desktop virtual.
func CaptureRegion(x, y, w, h, quality, maxDim int) (*CaptureResult, error) {
	cx, cy, cw, ch, err := ClampRegion(x, y, w, h)
	if err != nil {
		return nil, err
	}
	return captureRegionRaw(cx, cy, cw, ch, ModeRegion, quality, maxDim)
}

func captureRegionRaw(x, y, w, h int, mode string, quality, maxDim int) (*CaptureResult, error) {
	c, err := screen.NewGDICapturerRegion(x, y, w, h)
	if err != nil {
		return nil, fmt.Errorf("captura de regiao: %w", err)
	}
	frame, err := captureWithCapturer(c)
	if err != nil {
		return nil, fmt.Errorf("captura de regiao: %w", err)
	}
	res, err := encodeResult(frame, mode, -1, quality, maxDim)
	if err != nil {
		return nil, err
	}
	res.OriginX = x
	res.OriginY = y
	return res, nil
}

func encodeResult(frame *screen.Frame, mode string, monitor, quality, maxDim int) (*CaptureResult, error) {
	frame = toneMapIfHDR(frame)
	originX, originY := frame.OriginX, frame.OriginY
	if maxDim == 0 {
		maxDim = defaultMaxDimension
	}
	frame = Downscale(frame, maxDim)
	data, mime, err := EncodeFrame(frame, quality, -1)
	if err != nil {
		return nil, err
	}
	res := &CaptureResult{
		Mode: mode, MIME: mime, Data: data,
		Width: frame.Width, Height: frame.Height,
		OriginX: originX, OriginY: originY,
		Monitor: monitor, CapturedAt: time.Now(),
	}
	// Miniatura para transparência no chat (não vai ao LLM).
	if thumbFrame := Downscale(frame, thumbnailMaxDimension); thumbFrame != nil {
		if thumbData, thumbErr := screen.NewJPEGEncoder().Encode(thumbFrame, thumbnailQuality); thumbErr == nil {
			res.Thumbnail = thumbData
			res.ThumbnailMIME = "image/jpeg"
		}
	}
	return res, nil
}

// CaptureWindow captura uma janela específica por handle (PrintWindow).
func CaptureWindow(handle uint64, quality, maxDim int) (*CaptureResult, error) {
	return captureWindow(handle, quality, maxDim)
}

// CaptureScreen despacha a captura conforme o modo solicitado.
func CaptureScreen(req Request) (*CaptureResult, error) {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	switch mode {
	case "", ModeFull:
		return CaptureDesktop(req.Quality, req.MaxDimension)
	case ModeMonitor:
		return CaptureMonitor(req.MonitorIndex, req.Quality, req.MaxDimension)
	case ModeRegion:
		return CaptureRegion(req.X, req.Y, req.Width, req.Height, req.Quality, req.MaxDimension)
	case ModeWindow:
		if req.WindowHandle == 0 {
			return nil, fmt.Errorf("windowHandle obrigatorio no modo window")
		}
		return captureWindow(req.WindowHandle, req.Quality, req.MaxDimension)
	default:
		return nil, fmt.Errorf("modo de captura desconhecido: %q", req.Mode)
	}
}
