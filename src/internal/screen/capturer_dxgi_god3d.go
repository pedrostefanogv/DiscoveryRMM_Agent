//go:build windows

package screen

import (
	"fmt"
	"image"
	"runtime"
	"unsafe"

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
	drawCursor bool // se true, desenha o cursor no frame (compat); false = cursor separado
}

// NewDXGIGoD3dCapturer cria um capturador DXGI usando go-d3d.
func NewDXGIGoD3dCapturer(monitorIndex int) (Capturer, error) {
	return NewDXGIGoD3dCapturerMode(monitorIndex, true)
}

// NewDXGIGoD3dCapturerMode cria um capturador DXGI usando go-d3d.
// drawCursor=true desenha o cursor no frame (comportamento antigo/compatível);
// drawCursor=false deixa o frame limpo (cursor enviado separadamente pelo viewer).
func NewDXGIGoD3dCapturerMode(monitorIndex int, drawCursor bool) (Capturer, error) {
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

	// Cursor: desenha no frame (compat) ou deixa limpo (cursor separado).
	dup.DrawPointer = drawCursor
	dup.UpdatePointerInfo = drawCursor

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
		dup:        dup,
		device:     device,
		deviceCtx:  deviceCtx,
		img:        img,
		width:      width,
		height:     height,
		drawCursor: drawCursor,
	}, nil
}

func (c *dxgiGoD3dCapturer) AcquireNextFrame() (*Frame, error) {
	// Usa Snapshot() DIRETAMENTE (não GetImage) para evitar o latch de swizzle
	// do go-d3d: GetImage aplica swizzle.BGRA() (BGRA→RGBA) SOMENTE após o
	// primeiro frame sem metadata — antes disso retorna BGRA cru. Isso fazia o
	// frame alternar entre BGRA e RGBA de forma intermitente → cores invertidas.
	//
	// Snapshot() retorna SEMPRE os bytes BGRA crus (DXGI_FORMAT_B8G8R8A8_UNORM),
	// independentemente do estado do latch. Os encoders (jpeg/webp) já esperam
	// BGRA e fazem o swap B↔R corretamente.
	unmap, mappedRect, size, err := c.dup.Snapshot(200)
	if err != nil {
		if err == outputduplication.ErrNoImageYet {
			return nil, fmt.Errorf("timeout")
		}
		return nil, fmt.Errorf("go-d3d Snapshot: %w", err)
	}
	defer unmap()

	// Copia linha a linha respeitando o pitch (padding entre linhas).
	dataSize := int(mappedRect.Pitch) * int(size.Y)
	data := unsafe.Slice((*byte)(mappedRect.PBits), dataSize)
	contentWidth := int(size.X) * 4
	dataWidth := int(mappedRect.Pitch)

	frame := &Frame{
		Data:   make([]byte, contentWidth*int(size.Y)),
		Width:  int(size.X),
		Height: int(size.Y),
		Stride: contentWidth,
	}
	var src, dst int
	for i := 0; i < int(size.Y); i++ {
		copy(frame.Data[dst:], data[src:src+contentWidth])
		src += dataWidth
		dst += contentWidth
	}

	if c.drawCursor {
		// Desenha o cursor sobre o frame capturado (BGRA) — igual ao
		// GetImage com DrawPointer, mas preservando o formato BGRA.
		c.drawCursorBGRA(frame)
	}

	return frame, nil
}

// drawCursorBGRA desenha o cursor do sistema sobre um frame BGRA.
// Equivalente ao dup.DrawPointer do go-d3d, mas operando sobre []byte BGRA
// (não image.RGBA), preservando o formato esperado pelos encoders.
func (c *dxgiGoD3dCapturer) drawCursorBGRA(frame *Frame) {
	// O go-d3d não expõe a forma do cursor fora do drawPointer interno.
	// No modo cursor separado (padrão), drawCursor=false e este caminho não
	// é executado. Para compatibilidade, desenha um cursor básico na posição
	// atual do mouse via GetCursorPos.
	info, err := GetCursorPos()
	if err != nil {
		return
	}
	x, y := int(info.X), int(info.Y)
	if x < 0 || y < 0 || x >= frame.Width || y >= frame.Height {
		return
	}

	// Seta simples (12x12) em branco com contorno — BGRA
	path := [][2]int{
		{0, 0}, {0, 12}, {1, 12}, {1, 1}, {12, 1}, {12, 0},
	}
	for _, p := range path {
		px, py := x+p[0], y+p[1]
		if px < 0 || py < 0 || px >= frame.Width || py >= frame.Height {
			continue
		}
		off := py*frame.Stride + px*4
		frame.Data[off] = 0xff
		frame.Data[off+1] = 0xff
		frame.Data[off+2] = 0xff
		frame.Data[off+3] = 0xff
	}
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
