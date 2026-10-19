//go:build windows

package winui

import (
	"math"
	"sync"
	"syscall"
	"unsafe"
)

// ── Desenho com antialiasing (GDI+) ──
//
// O GDI (RoundRect/Ellipse) não faz antialiasing: cantos arredondados saem
// serrilhados ("quadriculados"), o que ficava evidente nos botões do prompt de
// consentimento. Aqui os contornos arredondados são preenchidos via GDI+ com
// SmoothingModeAntiAlias.
//
// A API "flat" do GDI+ recebe REAL (float) em vários pontos, e syscall.Syscall
// não passa floats em XMM — por isso só usamos as variantes inteiras:
// GdipAddPathPolygonI (polígono fechado) + GdipFillPath. O arco é aproximado
// por segmentos de reta (8 por canto), que com AA fica visualmente perfeito no
// tamanho dos nossos controles.
//
// Se o GDI+ não estiver disponível (raro), as funções caem no desenho GDI
// antigo — o pior caso é o visual anterior, nunca uma falha de pintura.

var (
	gdiplusDLL              = syscall.NewLazyDLL("gdiplus.dll")
	procGdiplusStartup      = gdiplusDLL.NewProc("GdiplusStartup")
	procGdipCreateFromHDC   = gdiplusDLL.NewProc("GdipCreateFromHDC")
	procGdipDeleteGraphics  = gdiplusDLL.NewProc("GdipDeleteGraphics")
	procGdipSetSmoothing    = gdiplusDLL.NewProc("GdipSetSmoothingMode")
	procGdipSetPixelOffset  = gdiplusDLL.NewProc("GdipSetPixelOffsetMode")
	procGdipCreateSolidFill = gdiplusDLL.NewProc("GdipCreateSolidFill")
	procGdipDeleteBrush     = gdiplusDLL.NewProc("GdipDeleteBrush")
	procGdipCreatePath      = gdiplusDLL.NewProc("GdipCreatePath")
	procGdipAddPolygonI     = gdiplusDLL.NewProc("GdipAddPathPolygonI")
	procGdipFillPath        = gdiplusDLL.NewProc("GdipFillPath")
	procGdipDeletePath      = gdiplusDLL.NewProc("GdipDeletePath")
	procGdipFillEllipseI    = gdiplusDLL.NewProc("GdipFillEllipseI")
)

// gdiplusStartupInput espelha GdiplusStartupInput (GdiplusInit.h).
type gdiplusStartupInput struct {
	GdiplusVersion           uint32
	DebugEventCallback       uintptr
	SuppressBackgroundThread int32
	SuppressExternalCodecs   int32
}

// gdiplusPoint espelha GpPoint (INT x, INT y).
type gdiplusPoint struct {
	X int32
	Y int32
}

const (
	// smoothingModeAntiAlias / pixelOffsetModeHalf (enums do GDI+).
	smoothingModeAntiAlias = 4
	pixelOffsetModeHalf    = 4
	// roundRectArcSegments: segmentos de reta por canto do retângulo arredondado.
	roundRectArcSegments = 8
)

var (
	gdiplusOnce  sync.Once
	gdiplusToken uintptr
	gdiplusOK    bool
)

// ensureGdiplus inicializa o GDI+ uma única vez por processo. O token não é
// liberado: o GDI+ fica ativo enquanto o agente viver (shutdown não é
// necessário para um processo que termina).
func ensureGdiplus() bool {
	gdiplusOnce.Do(func() {
		input := gdiplusStartupInput{GdiplusVersion: 1}
		status, _, _ := procGdiplusStartup.Call(
			uintptr(unsafe.Pointer(&gdiplusToken)),
			uintptr(unsafe.Pointer(&input)),
			0,
		)
		gdiplusOK = status == 0 && gdiplusToken != 0
	})
	return gdiplusOK
}

// argb converte COLORREF (0x00BBGGRR) em ARGB do GDI+ (0xAARRGGBB).
func argb(c Color) uint32 {
	return 0xFF000000 | uint32(c.R())<<16 | uint32(c.G())<<8 | uint32(c.B())
}

// gdiplusGraphics cria um Graphics para o HDC com AA ligado.
func gdiplusGraphics(hdc uintptr) uintptr {
	if hdc == 0 || !ensureGdiplus() {
		return 0
	}
	var graphics uintptr
	if status, _, _ := procGdipCreateFromHDC.Call(hdc, uintptr(unsafe.Pointer(&graphics))); status != 0 || graphics == 0 {
		return 0
	}
	procGdipSetSmoothing.Call(graphics, smoothingModeAntiAlias)
	procGdipSetPixelOffset.Call(graphics, pixelOffsetModeHalf)
	return graphics
}

// roundRectPolygon devolve os pontos do contorno arredondado (sentido horário,
// começando na borda superior direita) para o GdipAddPathPolygonI.
func roundRectPolygon(r Rect, radius int) []gdiplusPoint {
	if radius < 0 {
		radius = 0
	}
	maxRadius := r.W / 2
	if r.H/2 < maxRadius {
		maxRadius = r.H / 2
	}
	if radius > maxRadius {
		radius = maxRadius
	}
	if radius == 0 {
		return []gdiplusPoint{
			{X: int32(r.X), Y: int32(r.Y)},
			{X: int32(r.Right()), Y: int32(r.Y)},
			{X: int32(r.Right()), Y: int32(r.Bottom())},
			{X: int32(r.X), Y: int32(r.Bottom())},
		}
	}

	points := make([]gdiplusPoint, 0, 4*(roundRectArcSegments+1))
	addArc := func(centerX, centerY int, startDegrees float64) {
		for i := 0; i <= roundRectArcSegments; i++ {
			angle := (startDegrees + 90.0*float64(i)/roundRectArcSegments) * math.Pi / 180.0
			points = append(points, gdiplusPoint{
				X: int32(math.Round(float64(centerX) + float64(radius)*math.Cos(angle))),
				Y: int32(math.Round(float64(centerY) + float64(radius)*math.Sin(angle))),
			})
		}
	}
	addArc(r.Right()-radius, r.Y+radius, -90)      // superior direito
	addArc(r.Right()-radius, r.Bottom()-radius, 0) // inferior direito
	addArc(r.X+radius, r.Bottom()-radius, 90)      // inferior esquerdo
	addArc(r.X+radius, r.Y+radius, 180)            // superior esquerdo
	return points
}

// fillRoundRectPath preenche UM contorno arredondado (sem fallback).
func fillRoundRectPath(hdc uintptr, r Rect, radius int, fill Color) bool {
	if r.Empty() {
		return false
	}
	graphics := gdiplusGraphics(hdc)
	if graphics == 0 {
		return false
	}
	defer procGdipDeleteGraphics.Call(graphics)

	var path uintptr
	if status, _, _ := procGdipCreatePath.Call(uintptr(0), uintptr(unsafe.Pointer(&path))); status != 0 || path == 0 {
		return false
	}
	defer procGdipDeletePath.Call(path)

	points := roundRectPolygon(r, radius)
	status, _, _ := procGdipAddPolygonI.Call(path, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
	if status != 0 {
		return false
	}

	var brush uintptr
	if status, _, _ := procGdipCreateSolidFill.Call(uintptr(argb(fill)), uintptr(unsafe.Pointer(&brush))); status != 0 || brush == 0 {
		return false
	}
	defer procGdipDeleteBrush.Call(brush)

	status, _, _ = procGdipFillPath.Call(graphics, brush, path)
	return status == 0
}

// fillRoundRectRingPath preenche o anel entre o contorno externo e o interno
// (FillModeAlternate: dois contornos no mesmo path deixam o miolo vazado). É
// como desenhamos bordas e anéis de foco sem usar canetas com REAL.
func fillRoundRectRingPath(hdc uintptr, outer Rect, radius, ringWidth int, fill Color) bool {
	inner := outer.InsetUniform(ringWidth)
	if inner.Empty() {
		return false
	}
	graphics := gdiplusGraphics(hdc)
	if graphics == 0 {
		return false
	}
	defer procGdipDeleteGraphics.Call(graphics)

	var path uintptr
	if status, _, _ := procGdipCreatePath.Call(uintptr(0), uintptr(unsafe.Pointer(&path))); status != 0 || path == 0 {
		return false
	}
	defer procGdipDeletePath.Call(path)

	outerPoints := roundRectPolygon(outer, radius)
	innerRadius := radius - ringWidth
	if innerRadius < 0 {
		innerRadius = 0
	}
	innerPoints := roundRectPolygon(inner, innerRadius)
	if status, _, _ := procGdipAddPolygonI.Call(path, uintptr(unsafe.Pointer(&outerPoints[0])), uintptr(len(outerPoints))); status != 0 {
		return false
	}
	if status, _, _ := procGdipAddPolygonI.Call(path, uintptr(unsafe.Pointer(&innerPoints[0])), uintptr(len(innerPoints))); status != 0 {
		return false
	}

	var brush uintptr
	if status, _, _ := procGdipCreateSolidFill.Call(uintptr(argb(fill)), uintptr(unsafe.Pointer(&brush))); status != 0 || brush == 0 {
		return false
	}
	defer procGdipDeleteBrush.Call(brush)

	status, _, _ := procGdipFillPath.Call(graphics, brush, path)
	return status == 0
}

// FillRoundRectAA preenche um retângulo arredondado com antialiasing.
// Fallback: GDI puro (visual antigo) se o GDI+ não estiver disponível.
func FillRoundRectAA(hdc uintptr, r Rect, radius int, color Color) {
	if r.Empty() {
		return
	}
	if !fillRoundRectPath(hdc, r, radius, color) {
		FillRoundRect(hdc, r, radius, color)
	}
}

// FillRoundRectBorderAA preenche o retângulo arredondado (fill) e desenha a
// borda de 1px (border) com antialiasing.
func FillRoundRectBorderAA(hdc uintptr, r Rect, radius int, fill, border Color) {
	if r.Empty() {
		return
	}
	if !fillRoundRectPath(hdc, r, radius, border) {
		FillRoundRectBorder(hdc, r, radius, fill, border)
		return
	}
	inner := r.InsetUniform(1)
	if !inner.Empty() {
		innerRadius := maxInt(radius-1, 0)
		// Se o GDI+ falhar só no preenchimento interno, completa com GDI para o
		// botão não ficar com a cor da borda (fallback parcial).
		if !fillRoundRectPath(hdc, inner, innerRadius, fill) {
			FillRoundRect(hdc, inner, innerRadius, fill)
		}
	}
}

// StrokeRoundRectAA desenha um anel (borda) de ringWidth px com antialiasing.
func StrokeRoundRectAA(hdc uintptr, r Rect, radius, ringWidth int, color Color) {
	if r.Empty() || ringWidth <= 0 {
		return
	}
	if !fillRoundRectRingPath(hdc, r, radius, ringWidth, color) {
		StrokeRoundRect(hdc, r, radius, ringWidth, color)
	}
}

// FillEllipseAA preenche uma elipse com antialiasing (GdipFillEllipseI usa
// apenas inteiros — sem problemas de REAL no syscall).
func FillEllipseAA(hdc uintptr, r Rect, color Color) {
	if r.Empty() {
		return
	}
	graphics := gdiplusGraphics(hdc)
	if graphics == 0 {
		FillEllipse(hdc, r, color)
		return
	}
	defer procGdipDeleteGraphics.Call(graphics)
	var brush uintptr
	if status, _, _ := procGdipCreateSolidFill.Call(uintptr(argb(color)), uintptr(unsafe.Pointer(&brush))); status != 0 || brush == 0 {
		FillEllipse(hdc, r, color)
		return
	}
	defer procGdipDeleteBrush.Call(brush)
	status, _, _ := procGdipFillEllipseI.Call(graphics, brush,
		uintptr(r.X), uintptr(r.Y), uintptr(r.W), uintptr(r.H))
	if status != 0 {
		FillEllipse(hdc, r, color)
	}
}

// maxInt é local para não depender de builtin max em versões antigas de Go.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
