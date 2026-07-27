package screen

import "fmt"

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
// Prioriza DXGI; fallback para GDI.
func NewCapturer(monitorIndex int) (Capturer, error) {
	// Tenta DXGI primeiro
	dxgi, err := NewDXGICapturer(monitorIndex)
	if err == nil {
		return dxgi, nil
	}

	// Fallback para GDI
	gdi, gdiErr := NewGDICapturer()
	if gdiErr != nil {
		return nil, fmt.Errorf("nenhum capturador disponivel: dxgi=%v, gdi=%v", err, gdiErr)
	}
	return gdi, nil
}
