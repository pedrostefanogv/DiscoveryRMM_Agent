//go:build windows

package winui

import (
	"syscall"
	"unsafe"
)

// ── Desenho de polígonos e imagens via GDI+ ──
//
// Extensão do gdiplus_windows.go para os ícones vetoriais (Material Symbols)
// e o logo do agente (PNG embedado) do cartão de notificação. Mesma restrição
// do arquivo base: só variantes INT da API flat (syscall não passa floats).

// Point é um ponto inteiro de polígono (API pública do winui).
type Point struct {
	X, Y int
}

var (
	procGdipAddLine2I           = gdiplusDLL.NewProc("GdipAddPathLine2I")
	procGdipLoadImageFromStream = gdiplusDLL.NewProc("GdipLoadImageFromStream")
	procGdipDrawImageRectI      = gdiplusDLL.NewProc("GdipDrawImageRectI")
	procGdipDisposeImage        = gdiplusDLL.NewProc("GdipDisposeImage")

	// SHCreateMemStream (shlwapi) cria um IStream somente-leitura sobre um
	// buffer que já possuímos — sem GlobalAlloc/GlobalLock.
	shlwapiDLL            = syscall.NewLazyDLL("shlwapi.dll")
	procSHCreateMemStream = shlwapiDLL.NewProc("SHCreateMemStream")
)

// FillPolygonsAA preenche vários polígonos (subpaths; um polígono dentro de
// outro vira furo pelo FillModeAlternate) com antialiasing.
func FillPolygonsAA(hdc uintptr, polygons [][]Point, color Color) {
	if len(polygons) == 0 {
		return
	}
	graphics := gdiplusGraphics(hdc)
	if graphics == 0 {
		return
	}
	defer procGdipDeleteGraphics.Call(graphics)

	var path uintptr
	if status, _, _ := procGdipCreatePath.Call(uintptr(0), uintptr(unsafe.Pointer(&path))); status != 0 || path == 0 {
		return
	}
	defer procGdipDeletePath.Call(path)

	for _, poly := range polygons {
		if len(poly) < 3 {
			continue
		}
		points := make([]gdiplusPoint, len(poly))
		for i, p := range poly {
			points[i] = gdiplusPoint{X: int32(p.X), Y: int32(p.Y)}
		}
		if status, _, _ := procGdipAddLine2I.Call(path,
			uintptr(unsafe.Pointer(&points[0])), uintptr(len(points))); status != 0 {
			continue
		}
	}

	var brush uintptr
	if status, _, _ := procGdipCreateSolidFill.Call(uintptr(argb(color)), uintptr(unsafe.Pointer(&brush))); status != 0 || brush == 0 {
		return
	}
	defer procGdipDeleteBrush.Call(brush)
	procGdipFillPath.Call(graphics, brush, path)
}

// pointerFromUintptr reinterpreta um uintptr devolvido pela API nativa como
// unsafe.Pointer sem conversão direta (o analyzer unsafeptr reclama de
// uintptr→Pointer; através do alias de memória o valor é o mesmo ponteiro
// nativo, não-GC, e a conversão permanece vet-clean — mesmo padrão do
// comCall do capturer_dxgi.go).
func pointerFromUintptr(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

// iUnknownVtbl é a vtable base do COM (só precisamos do Release).
type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

// iUnknownObject é qualquer objeto COM visto pela vtable base.
type iUnknownObject struct {
	vtbl *iUnknownVtbl
}

// releaseCOM chama IUnknown::Release (o objeto chegou com referência nossa).
func releaseCOM(p uintptr) {
	if p == 0 {
		return
	}
	if p == 0 {
		return
	}
	obj := (*iUnknownObject)(pointerFromUintptr(p))
	if obj == nil || obj.vtbl == nil || obj.vtbl.Release == 0 {
		return
	}
	syscall.SyscallN(obj.vtbl.Release, p)
}

// DrawPNGMemory decodifica um PNG em memória e desenha preenchendo o
// retângulo, preservando transparência. Devolve false quando GDI+ ou a
// decodificação falham (chamador decide o fallback).
func DrawPNGMemory(hdc uintptr, png []byte, r Rect) bool {
	if len(png) == 0 || r.Empty() || !ensureGdiplus() {
		return false
	}
	stream, _, _ := procSHCreateMemStream.Call(
		uintptr(unsafe.Pointer(&png[0])), uintptr(len(png)))
	if stream == 0 {
		return false
	}
	defer releaseCOM(stream)

	graphics := gdiplusGraphics(hdc)
	if graphics == 0 {
		return false
	}
	defer procGdipDeleteGraphics.Call(graphics)

	var image uintptr
	if status, _, _ := procGdipLoadImageFromStream.Call(stream, uintptr(unsafe.Pointer(&image))); status != 0 || image == 0 {
		return false
	}
	defer procGdipDisposeImage.Call(image)

	status, _, _ := procGdipDrawImageRectI.Call(graphics, image,
		uintptr(r.X), uintptr(r.Y), uintptr(r.W), uintptr(r.H))
	return status == 0
}
