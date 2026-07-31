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
}

// NewCapturer cria o capturador apropriado para o sistema.
// Prioridade: go-d3d (HW dirty rects + cursor) → DXGI manual → GDI fallback.
func NewCapturer(monitorIndex int) (Capturer, error) {
	// Tenta go-d3d primeiro (HW dirty rects, cursor overlay, release otimizado)
	c, err := NewDXGIGoD3dCapturer(monitorIndex)
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
