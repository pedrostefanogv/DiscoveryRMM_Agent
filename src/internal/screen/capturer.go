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
// Tenta DXGI Desktop Duplication primeiro; fallback para GDI se indisponivel.
func NewCapturer(monitorIndex int) (Capturer, error) {
	c, err := NewDXGICapturer(monitorIndex)
	if err != nil {
		return NewGDICapturer()
	}
	return c, nil
}
