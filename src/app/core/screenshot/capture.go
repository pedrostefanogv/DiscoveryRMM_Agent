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
	return captureMonitorFormat(monitorIndex, quality, maxDim, FormatAuto)
}

func captureMonitorFormat(monitorIndex, quality, maxDim int, format string) (*CaptureResult, error) {
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
	// `format` (não FormatAuto): o modo monitor precisa respeitar a preferência
	// png/webp da política, como os outros modos — ignorá-la fazia o print sair em
	// WebP quando a UI pedia PNG.
	return encodeResult(frame, ModeMonitor, monitorIndex, quality, maxDim, format)
}

// CaptureDesktop captura o desktop virtual inteiro (todos os monitores).
func CaptureDesktop(quality, maxDim int) (*CaptureResult, error) {
	return captureDesktopFormat(quality, maxDim, FormatAuto)
}

func captureDesktopFormat(quality, maxDim int, format string) (*CaptureResult, error) {
	x, y, w, h, ok := VirtualBounds()
	if !ok {
		return nil, fmt.Errorf("nao foi possivel determinar os limites do desktop")
	}
	return captureRegionRaw(x, y, w, h, ModeFull, quality, maxDim, format)
}

// CaptureRegion captura uma região arbitrária do desktop virtual.
func CaptureRegion(x, y, w, h, quality, maxDim int) (*CaptureResult, error) {
	return captureRegionFormat(x, y, w, h, quality, maxDim, FormatAuto)
}

func captureRegionFormat(x, y, w, h, quality, maxDim int, format string) (*CaptureResult, error) {
	cx, cy, cw, ch, err := ClampRegion(x, y, w, h)
	if err != nil {
		return nil, err
	}
	return captureRegionRaw(cx, cy, cw, ch, ModeRegion, quality, maxDim, format)
}

func captureRegionRaw(x, y, w, h int, mode string, quality, maxDim int, format string) (*CaptureResult, error) {
	frame, err := captureRegionFrame(x, y, w, h)
	if err != nil {
		return nil, fmt.Errorf("captura de regiao: %w", err)
	}
	res, err := encodeResult(frame, mode, -1, quality, maxDim, format)
	if err != nil {
		return nil, err
	}
	res.OriginX = x
	res.OriginY = y
	return res, nil
}

// CaptureDesktopFrame captura o retângulo físico do desktop (x,y,w,h) e devolve
// o frame BGRA já copiado (pode ser usado depois de fechar o capturer). É o que
// o overlay usa para congelar a tela.
func CaptureDesktopFrame(x, y, w, h int) (*screen.Frame, error) {
	frame, err := captureRegionFrame(x, y, w, h)
	if err != nil {
		return nil, err
	}
	// Monitor HDR/Advanced Color: o capturer devolve scRGB float (8 bytes/px) e
	// TODO o pipeline do overlay (encode do frame congelado, recorte geométrico,
	// miniatura e imagem anotada) assume BGRA 8-bit — sem o tone mapping aqui o
	// print do overlay saía com cores corrompidas. O caminho de captura direta
	// já fazia isso em encodeResult; o overlay ficou sem.
	return toneMapIfHDR(frame), nil
}

// captureRegionFrame captura o retângulo físico. Em monitor Advanced Color/HDR,
// o BitBlt (GDI) devolve SDR LAVADO e ignora o tone mapping, então preferimos
// capturar o monitor inteiro pelo capturer HDR-aware e recortar. Qualquer
// desconfiança (retângulo fora do monitor, captura falhou) volta para o GDI, que
// é o comportamento antigo.
func captureRegionFrame(x, y, w, h int) (*screen.Frame, error) {
	if frame, ok := captureRegionFrameHDR(x, y, w, h); ok {
		return frame, nil
	}
	c, err := screen.NewGDICapturerRegion(x, y, w, h)
	if err != nil {
		return nil, err
	}
	return captureWithCapturer(c)
}

func captureRegionFrameHDR(x, y, w, h int) (*screen.Frame, bool) {
	mons := ListMonitors()
	if len(mons) == 0 || w <= 0 || h <= 0 {
		return nil, false
	}
	centerX, centerY := x+w/2, y+h/2
	index := -1
	for _, m := range mons {
		if centerX >= m.X && centerX < m.X+m.Width && centerY >= m.Y && centerY < m.Y+m.Height {
			index = m.Index
			break
		}
	}
	if index < 0 {
		return nil, false
	}
	ac, err := screen.DetectAdvancedColor(index)
	if err != nil || ac == nil || !ac.IsHDR {
		return nil, false
	}
	c, err := screen.NewCapturerMode(index, true)
	if err != nil {
		return nil, false
	}
	frame, err := captureWithCapturer(c)
	if err != nil || frame == nil {
		return nil, false
	}
	offsetX, offsetY := x-frame.OriginX, y-frame.OriginY
	// Guarda: o recorte só vale se o retângulo pedido estiver inteiro dentro do
	// frame capturado — assim uma origem inesperada nunca devolve região errada.
	if offsetX < 0 || offsetY < 0 || offsetX+w > frame.Width || offsetY+h > frame.Height {
		return nil, false
	}
	crop, cerr := CropFrame(frame, offsetX, offsetY, w, h)
	if cerr != nil {
		return nil, false
	}
	return crop, true
}

func encodeResult(frame *screen.Frame, mode string, monitor, quality, maxDim int, format string) (*CaptureResult, error) {
	frame = toneMapIfHDR(frame)
	originX, originY := frame.OriginX, frame.OriginY
	if maxDim == 0 {
		maxDim = defaultMaxDimension
	}
	frame = Downscale(frame, maxDim)
	data, mime, err := EncodeFrameFormat(frame, quality, -1, format)
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
		if thumbData, thumbMime := encodeThumbnail(thumbFrame); len(thumbData) > 0 {
			res.Thumbnail = thumbData
			res.ThumbnailMIME = thumbMime
		}
	}
	return res, nil
}

// CaptureWindow captura uma janela específica por handle (PrintWindow).
func CaptureWindow(handle uint64, quality, maxDim int) (*CaptureResult, error) {
	return captureWindowFormat(handle, quality, maxDim, FormatAuto)
}

// CaptureScreen despacha a captura conforme o modo solicitado.
func CaptureScreen(req Request) (*CaptureResult, error) {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	switch mode {
	case "", ModeFull:
		return captureDesktopFormat(req.Quality, req.MaxDimension, req.Format)
	case ModeMonitor:
		return captureMonitorFormat(req.MonitorIndex, req.Quality, req.MaxDimension, req.Format)
	case ModeRegion:
		return captureRegionFormat(req.X, req.Y, req.Width, req.Height, req.Quality, req.MaxDimension, req.Format)
	case ModeWindow:
		if req.WindowHandle == 0 {
			return nil, fmt.Errorf("windowHandle obrigatorio no modo window")
		}
		return captureWindowFormat(req.WindowHandle, req.Quality, req.MaxDimension, req.Format)
	default:
		return nil, fmt.Errorf("modo de captura desconhecido: %q", req.Mode)
	}
}
