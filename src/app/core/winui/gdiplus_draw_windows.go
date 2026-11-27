//go:build windows

package winui

import (
	"sync"
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
		if status, _, _ := procGdipAddPolygonI.Call(path,
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

// FillPolygonsAAf preenche vários polígonos com coordenadas fracionárias
// (subpaths independentes; um polígono dentro de outro vira furo pelo
// FillModeAlternate) com antialiasing sub-pixel.
func FillPolygonsAAf(hdc uintptr, polygons [][]PointF, color Color) {
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
		points := make([]gdiplusPointF, len(poly))
		for i, p := range poly {
			points[i] = gdiplusPointF{X: p.X, Y: p.Y}
		}
		if status, _, _ := procGdipAddPathPolygon.Call(path,
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
	obj := (*iUnknownObject)(pointerFromUintptr(p))
	if obj == nil || obj.vtbl == nil || obj.vtbl.Release == 0 {
		return
	}
	syscall.SyscallN(obj.vtbl.Release, p)
}

// DrawPNGMemory decodifica um PNG em memória (com cache por conteúdo) e
// desenha preenchendo o retângulo, preservando transparência. Devolve false
// quando GDI+ ou a decodificação falham (chamador decide o fallback).
var (
	pngImageCacheMu sync.Mutex
	pngImageCache   = make(map[pngCacheKey]uintptr)
)

type pngCacheKey struct {
	length int
	hash   uint64
}

func fnv1a64(data []byte) uint64 {
	var h uint64 = 14695981039346656037
	for _, b := range data {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return h
}

func DrawPNGMemory(hdc uintptr, png []byte, r Rect) bool {
	if len(png) == 0 || r.Empty() || !ensureGdiplus() {
		return false
	}

	graphics := gdiplusGraphics(hdc)
	if graphics == 0 {
		return false
	}
	defer procGdipDeleteGraphics.Call(graphics)

	key := pngCacheKey{length: len(png), hash: fnv1a64(png)}
	pngImageCacheMu.Lock()
	defer pngImageCacheMu.Unlock()

	image := pngImageCache[key]
	if image == 0 {
		stream, _, _ := procSHCreateMemStream.Call(
			uintptr(unsafe.Pointer(&png[0])), uintptr(len(png)))
		if stream == 0 {
			return false
		}
		if status, _, _ := procGdipLoadImageFromStream.Call(stream, uintptr(unsafe.Pointer(&image))); status != 0 || image == 0 {
			releaseCOM(stream)
			return false
		}
		releaseCOM(stream)
		if len(pngImageCache) >= 32 {
			for k, oldImg := range pngImageCache {
				if oldImg != 0 {
					procGdipDisposeImage.Call(oldImg)
				}
				delete(pngImageCache, k)
			}
		}
		pngImageCache[key] = image
	}

	status, _, _ := procGdipDrawImageRectI.Call(graphics, image,
		uintptr(r.X), uintptr(r.Y), uintptr(r.W), uintptr(r.H))
	return status == 0
}
