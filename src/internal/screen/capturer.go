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
// NOTA: DXGI Desktop Duplication ainda e um stub — GDI e usado como primario ate
// a implementacao completa dos bindings COM na Fase 5 (Dirty Rects + Otimizacoes).
func NewCapturer(monitorIndex int) (Capturer, error) {
	// DXGI ainda e stub — usar GDI diretamente
	// TODO: reativar DXGI quando os bindings COM de IDXGIOutputDuplication
	// forem implementados na Fase 5.
	return NewGDICapturer()
}
