//go:build windows

package screen

// Capturer captura frames da tela do Windows.
type Capturer interface {
	// AcquireNextFrame captura o proximo frame. Retorna os bytes raw BGRA e as dimensoes.
	AcquireNextFrame() (*Frame, error)
	// ReleaseFrame libera o frame (obrigatorio apos uso).
	ReleaseFrame()
	// Close encerra o capturador e libera recursos GPU.
	Close() error
	// Name retorna o nome do capturador (dxgi, gdi).
	Name() string
}

// Frame representa um frame capturado da tela.
type Frame struct {
	Data   []byte // dados raw BGRA (width * height * 4)
	Width  int
	Height int
	Stride int // bytes por linha (width * 4 + padding)

	// ColorSpace indica o color space do frame. 0 = SDR (BGRA 8-bit, padrão).
	// Quando HDR (scRGB float), o frame.Data é R16G16B16A16_FLOAT (8 bytes/px)
	// e precisa de tone mapping antes do encode. Valores: DXGI_COLOR_SPACE_*.
	ColorSpace uint32
}

// NewCapturer cria o capturador apropriado para o sistema.
// Prioridade: go-d3d (HW dirty rects + cursor) → DXGI manual → GDI fallback.
func NewCapturer(monitorIndex int) (Capturer, error) {
	return NewCapturerMode(monitorIndex, true)
}

// NewCapturerMode cria o capturador com controle de cursor.
// drawCursor=true: cursor desenhado no frame (compat).
// drawCursor=false: cursor separado (enviado via subject .cursor pelo viewer).
func NewCapturerMode(monitorIndex int, drawCursor bool) (Capturer, error) {
	// Em monitor HDR/Advanced Color, o go-d3d força R8G8B8A8_UNORM (SDR 8-bit)
	// e o driver converte sem tone mapping → conteúdo HDR lavado. Nesse caso,
	// usamos o capturador DXGI manual, que captura em scRGB float e permite
	// tone mapping HDR→SDR no pipeline.
	if ac, err := DetectAdvancedColor(monitorIndex); err == nil && ac != nil && ac.IsHDR {
		c, err := NewDXGICapturer(monitorIndex)
		if err == nil {
			return c, nil
		}
		// Se o DXGI manual falhar, cai para o fluxo normal (go-d3d → GDI).
	}

	// Tenta go-d3d primeiro (HW dirty rects, release otimizado)
	c, err := NewDXGIGoD3dCapturerMode(monitorIndex, drawCursor)
	if err == nil {
		return c, nil
	}
	// Fallback: DXGI manual (VTable syscall)
	c, err = NewDXGICapturer(monitorIndex)
	if err == nil {
		return c, nil
	}
	// Fallback final: GDI BitBlt (VM sem GPU, RDP)
	return NewGDICapturerMonitor(monitorIndex)
}
