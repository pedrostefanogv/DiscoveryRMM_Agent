//go:build windows

package screen

import (
	"fmt"
	"image"
	"runtime"

	"github.com/kirides/go-d3d/d3d11"
	"github.com/kirides/go-d3d/outputduplication"
)

// ── DXGI Capturer via go-d3d ──
//
// Substitui capturer_dxgi.go (VTable manual) por bindings tipados.
// Usa GetImage() que internamente:
//   - AcquireNextFrame com timeout
//   - GetFrameDirtyRects (GPU hardware, zero CPU)
//   - CopyResource GPU→staging
//   - Map staging→CPU (apenas dirty regions)
//   - DrawCursor overlay
//   - ReleaseFrame imediato

type dxgiGoD3dCapturer struct {
	dup        *outputduplication.OutputDuplicator
	device     *d3d11.ID3D11Device
	deviceCtx  *d3d11.ID3D11DeviceContext
	img        *image.RGBA
	width      int
	height     int
}

// NewDXGIGoD3dCapturer cria um capturador DXGI usando go-d3d.
func NewDXGIGoD3dCapturer(monitorIndex int) (Capturer, error) {
	runtime.LockOSThread()

	// Cria D3D11 device
	device, deviceCtx, err := d3d11.NewD3D11Device()
	if err != nil {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("go-d3d D3D11CreateDevice: %w", err)
	}

	dup, err := outputduplication.NewIDXGIOutputDuplication(device, deviceCtx, uint(monitorIndex))
	if err != nil {
		device.Release()
		deviceCtx.Release()
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("go-d3d NewIDXGIOutputDuplication: %w", err)
	}

	// Habilita cursor drawing + pointer update
	dup.DrawPointer = true
	dup.UpdatePointerInfo = true

	bounds, err := dup.GetBounds()
	if err != nil {
		dup.Release()
		device.Release()
		deviceCtx.Release()
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("go-d3d GetBounds: %w", err)
	}

	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))

	return &dxgiGoD3dCapturer{
		dup:       dup,
		device:    device,
		deviceCtx: deviceCtx,
		img:       img,
		width:     width,
		height:    height,
	}, nil
}

func (c *dxgiGoD3dCapturer) AcquireNextFrame() (*Frame, error) {
	// GetImage: AcquireFrame(200ms) → CopyResource → Map → DrawCursor → ReleaseFrame
	// Retorna ErrNoImageYet se timeout (sem frame novo)
	err := c.dup.GetImage(c.img, 200)
	if err != nil {
		if err == outputduplication.ErrNoImageYet {
			return nil, fmt.Errorf("timeout")
		}
		// DXGI_ERROR_ACCESS_LOST ou outro erro
		return nil, fmt.Errorf("go-d3d GetImage: %w", err)
	}

	// image.RGBA já está em BGRA (DXGI_FORMAT_B8G8R8A8_UNORM)
	// NOTA: session_screen.go faz cópia adicional (frameCopy) para liberar capturador.
	// Nao fazemos copia aqui — o caller decide se precisa.
	return &Frame{
		Data:   c.img.Pix,
		Width:  c.width,
		Height: c.height,
		Stride: c.img.Stride,
	}, nil
}

func (c *dxgiGoD3dCapturer) ReleaseFrame() {
	// GetImage já chamou ReleaseFrame() internamente — no-op.
}

func (c *dxgiGoD3dCapturer) Close() error {
	if c.dup != nil {
		c.dup.Release()
		c.dup = nil
	}
	if c.device != nil {
		c.device.Release()
		c.device = nil
	}
	if c.deviceCtx != nil {
		c.deviceCtx.Release()
		c.deviceCtx = nil
	}
	return nil
}

func (c *dxgiGoD3dCapturer) Name() string {
	return "dxgi-go-d3d"
}
