//go:build !windows

package screen

import "errors"

// errNotSupported é retornado quando uma funcionalidade de screen capture é
// usada em plataformas não-Windows.
var errNotSupported = errors.New("screen capture não suportado nesta plataforma")

// Este arquivo fornece stubs para plataformas não-Windows (darwin, linux).
// O screen capture é uma funcionalidade exclusiva do Windows (DXGI/GDI).
// Os stubs mantêm a compilação dos pacotes que referenciam os tipos.

// Capturer captura frames da tela. Stub para não-Windows.
type Capturer interface {
	AcquireNextFrame() (*Frame, error)
	ReleaseFrame()
	Close() error
	Name() string
}

// Frame representa um frame capturado da tela.
type Frame struct {
	Data   []byte
	Width  int
	Height int
	Stride int

	// ColorSpace indica o color space do frame (0 = SDR BGRA 8-bit).
	ColorSpace uint32
}

// Monitor representa um monitor conectado.
type Monitor struct {
	Index     int    `json:"index"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Name      string `json:"name"`
	IsPrimary bool   `json:"isPrimary"`
}

// CursorImage representa o bitmap do cursor.
type CursorImage struct {
	Width  int
	Height int
	HotX   int
	HotY   int
	PNG    []byte
}

// CursorInfo representa a posicao do cursor.
type CursorInfo struct {
	X      int
	Y      int
	Handle uintptr
}

// CursorSpriteSender envia sprites do cursor.
type CursorSpriteSender struct{}

// GPUCapability indica capacidades da GPU.
type GPUCapability struct {
	DXGIAvailable bool
	MemoryMB      int64
}

// NewCapturer retorna erro em plataformas não-Windows.
func NewCapturer(monitorIndex int) (Capturer, error) {
	return nil, errNotSupported
}

// NewCapturerMode retorna erro em plataformas não-Windows.
func NewCapturerMode(monitorIndex int, drawCursor bool) (Capturer, error) {
	return nil, errNotSupported
}

// DetectGPU retorna capacidade vazia em plataformas não-Windows.
func DetectGPU() GPUCapability {
	return GPUCapability{}
}

// GetMonitors retorna erro em plataformas não-Windows.
func GetMonitors() ([]Monitor, error) {
	return nil, errNotSupported
}

// GetCursorPos retorna erro em plataformas não-Windows.
func GetCursorPos() (CursorInfo, error) {
	return CursorInfo{}, errNotSupported
}

// GetCursorHandle retorna 0 em plataformas não-Windows.
func GetCursorHandle() uintptr { return 0 }

// CaptureCursorImage retorna nil em plataformas não-Windows.
func CaptureCursorImage() *CursorImage { return nil }

// NewCursorSpriteSender retorna um sender vazio em plataformas não-Windows.
func NewCursorSpriteSender() *CursorSpriteSender { return &CursorSpriteSender{} }
