//go:build windows

package winui

import "unsafe"

// PointF é um ponto com coordenadas fracionárias para desenho vetorial GDI+.
type PointF struct {
	X float32
	Y float32
}

const (
	// PixelFormat32bppPARGB = 11 | (32<<8) | PixelFormatGDI(0x20000) | PixelFormatAlpha(0x40000) | PixelFormatPAlpha(0x80000)
	pixelFormat32bppPARGB = 0x000E200B
	fillModeAlternate     = 0
	fillModeWinding       = 1
)

var (
	procGdipStartPathFigure         = gdiplusDLL.NewProc("GdipStartPathFigure")
	procGdipCreateBitmapFromScan0   = gdiplusDLL.NewProc("GdipCreateBitmapFromScan0")
	procGdipGetImageGraphicsContext = gdiplusDLL.NewProc("GdipGetImageGraphicsContext")
)

// PARGBCanvas encapsula um GpBitmap 32bpp PARGB sobre um buffer de memória
// externo (ex.: DIB section de UpdateLayeredWindow) e seu GpGraphics associado,
// garantindo premultiplied alpha exato e antialiasing.
type PARGBCanvas struct {
	bitmap   uintptr
	graphics uintptr
}

// NewPARGBCanvas cria um canvas GDI+ 32bpp PARGB apontando para bits (BGRA).
func NewPARGBCanvas(bits unsafe.Pointer, width, height int) *PARGBCanvas {
	if bits == nil || width <= 0 || height <= 0 || !ensureGdiplus() {
		return nil
	}
	var bitmap uintptr
	status, _, _ := procGdipCreateBitmapFromScan0.Call(
		uintptr(width),
		uintptr(height),
		uintptr(width*4),
		uintptr(pixelFormat32bppPARGB),
		uintptr(bits),
		uintptr(unsafe.Pointer(&bitmap)),
	)
	if status != 0 || bitmap == 0 {
		return nil
	}
	var graphics uintptr
	status, _, _ = procGdipGetImageGraphicsContext.Call(bitmap, uintptr(unsafe.Pointer(&graphics)))
	if status != 0 || graphics == 0 {
		procGdipDisposeImage.Call(bitmap)
		return nil
	}
	procGdipSetSmoothing.Call(graphics, smoothingModeAntiAlias)
	procGdipSetPixelOffset.Call(graphics, pixelOffsetModeHalf)
	return &PARGBCanvas{bitmap: bitmap, graphics: graphics}
}

// FillPolygons preenche uma lista de polígonos fechados com ARGB real.
// winding=true usa FillModeWinding (ideal para traços/setas com junções);
// winding=false usa FillModeAlternate (ideal para anéis vazados).
func (c *PARGBCanvas) FillPolygons(polygons [][]PointF, argbColor uint32, winding bool) bool {
	if c == nil || c.graphics == 0 || len(polygons) == 0 {
		return false
	}
	mode := uintptr(fillModeAlternate)
	if winding {
		mode = uintptr(fillModeWinding)
	}
	var path uintptr
	if status, _, _ := procGdipCreatePath.Call(mode, uintptr(unsafe.Pointer(&path))); status != 0 || path == 0 {
		return false
	}
	defer procGdipDeletePath.Call(path)

	added := 0
	for _, poly := range polygons {
		if len(poly) < 3 {
			continue
		}
		procGdipStartPathFigure.Call(path)
		if status, _, _ := procGdipAddPathPolygon.Call(
			path,
			uintptr(unsafe.Pointer(&poly[0])),
			uintptr(len(poly)),
		); status == 0 {
			added++
		}
	}
	if added == 0 {
		return false
	}

	var brush uintptr
	if status, _, _ := procGdipCreateSolidFill.Call(uintptr(argbColor), uintptr(unsafe.Pointer(&brush))); status != 0 || brush == 0 {
		return false
	}
	defer procGdipDeleteBrush.Call(brush)

	status, _, _ := procGdipFillPath.Call(c.graphics, brush, path)
	return status == 0
}

// Close libera os recursos GDI+ do canvas (não libera o buffer externo).
func (c *PARGBCanvas) Close() {
	if c == nil {
		return
	}
	if c.graphics != 0 {
		procGdipDeleteGraphics.Call(c.graphics)
		c.graphics = 0
	}
	if c.bitmap != 0 {
		procGdipDisposeImage.Call(c.bitmap)
		c.bitmap = 0
	}
}
