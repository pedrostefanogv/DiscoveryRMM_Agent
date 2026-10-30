//go:build windows

package winui

import (
	"math"
	"unsafe"
)

// ── Desenho antialiased com coordenadas fracionárias (GDI+) ──
//
// As funções AA de gdiplus_windows.go recebem Rect com coordenadas INTEIRAS
// (GdipAddPathPolygonI): o arredondamento para o pixel inteiro faz o controle
// "pular" de 1 em 1 px quando ele se move devagar. Era exatamente isso que
// aparecia como degraus na barra do contador de notificações.
//
// O GDI+ tem a API correspondente em REAL (float), mas syscall.Syscall não
// passa floats em XMM. A saída aqui são só as funções em que os floats viajam
// DENTRO de structs, passadas por ponteiro:
//
//   - GdipAddPathPolygon(Path, GpPointF*, count) — contorno do retângulo
//     arredondado com coordenadas fracionárias;
//   - GdipCreateLineBrush(GpPointF*, GpPointF*, ARGB, ARGB, wrap, brush) —
//     gradiente linear para o preenchimento da barra.
//
// Nenhum argumento escalar REAL é usado; se o GDI+ não estiver disponível o
// fallback é o desenho AA inteiro (mesmo visual de antes), nunca uma falha.

// RectF é um retângulo com coordenadas fracionárias (px de dispositivo).
type RectF struct {
	X float64
	Y float64
	W float64
	H float64
}

// Right devolve o limite direito.
func (r RectF) Right() float64 { return r.X + r.W }

// Bottom devolve o limite inferior.
func (r RectF) Bottom() float64 { return r.Y + r.H }

// Empty indica retângulo sem área.
func (r RectF) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Round converte para pixels inteiros (usado apenas no fallback).
func (r RectF) Round() Rect {
	return Rect{
		X: int(math.Round(r.X)),
		Y: int(math.Round(r.Y)),
		W: int(math.Round(r.W)),
		H: int(math.Round(r.H)),
	}
}

// gdiplusPointF espelha GpPointF do GDI+ (REAL = float de 32 bits).
type gdiplusPointF struct {
	X float32
	Y float32
}

var (
	procGdipAddPathPolygon  = gdiplusDLL.NewProc("GdipAddPathPolygon")
	procGdipCreateLineBrush = gdiplusDLL.NewProc("GdipCreateLineBrush")
)

// roundRectPolygonF devolve o contorno arredondado em coordenadas
// fracionárias (mesma ordem do roundRectPolygon inteiro: horário, começando
// na borda superior direita).
func roundRectPolygonF(r RectF, radius float64) []gdiplusPointF {
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
	if radius <= 0 {
		return []gdiplusPointF{
			{X: float32(r.X), Y: float32(r.Y)},
			{X: float32(r.Right()), Y: float32(r.Y)},
			{X: float32(r.Right()), Y: float32(r.Bottom())},
			{X: float32(r.X), Y: float32(r.Bottom())},
		}
	}

	points := make([]gdiplusPointF, 0, 4*(roundRectArcSegments+1))
	addArc := func(centerX, centerY, startDegrees float64) {
		for i := 0; i <= roundRectArcSegments; i++ {
			angle := (startDegrees + 90.0*float64(i)/float64(roundRectArcSegments)) * math.Pi / 180.0
			points = append(points, gdiplusPointF{
				X: float32(centerX + radius*math.Cos(angle)),
				Y: float32(centerY + radius*math.Sin(angle)),
			})
		}
	}
	addArc(r.Right()-radius, r.Y+radius, -90)      // superior direito
	addArc(r.Right()-radius, r.Bottom()-radius, 0) // inferior direito
	addArc(r.X+radius, r.Bottom()-radius, 90)      // inferior esquerdo
	addArc(r.X+radius, r.Y+radius, 180)            // superior esquerdo
	return points
}

// roundRectPathF monta o path GDI+ do retângulo arredondado fracionário.
// Devolve 0 quando o GDI+ não está disponível.
func roundRectPathF(r RectF, radius float64) uintptr {
	points := roundRectPolygonF(r, radius)
	if len(points) < 3 {
		return 0
	}
	var path uintptr
	if status, _, _ := procGdipCreatePath.Call(uintptr(0), uintptr(unsafe.Pointer(&path))); status != 0 || path == 0 {
		return 0
	}
	if status, _, _ := procGdipAddPathPolygon.Call(path, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points))); status != 0 {
		procGdipDeletePath.Call(path)
		return 0
	}
	return path
}

// FillRoundRectAAf preenche um retângulo arredondado usando coordenadas
// fracionárias — é o que permite mover a barra da contagem em passos menores
// que 1 px (movimento contínuo, sem degraus).
func FillRoundRectAAf(hdc uintptr, r RectF, radius float64, color Color) {
	if r.Empty() {
		return
	}
	graphics := gdiplusGraphics(hdc)
	if graphics == 0 {
		FillRoundRectAA(hdc, r.Round(), int(math.Round(radius)), color)
		return
	}
	defer procGdipDeleteGraphics.Call(graphics)

	path := roundRectPathF(r, radius)
	if path == 0 {
		FillRoundRectAA(hdc, r.Round(), int(math.Round(radius)), color)
		return
	}
	defer procGdipDeletePath.Call(path)

	var brush uintptr
	if status, _, _ := procGdipCreateSolidFill.Call(uintptr(argb(color)), uintptr(unsafe.Pointer(&brush))); status != 0 || brush == 0 {
		FillRoundRectAA(hdc, r.Round(), int(math.Round(radius)), color)
		return
	}
	defer procGdipDeleteBrush.Call(brush)

	procGdipFillPath.Call(graphics, brush, path)
}

// FillRoundRectGradientAAf preenche o retângulo arredondado com um gradiente
// horizontal. gradFrom/gradTo são os LIMITES do gradiente (px de dispositivo):
// passam as bordas do trilho para que a escala de cor fique ancorada e não
// comprima junto com o preenchimento (o fim da contagem ficava quase sólido).
// Sem gradiente válido, cai no preenchimento sólido AA.
func FillRoundRectGradientAAf(hdc uintptr, r RectF, radius float64, from, to Color, gradFrom, gradTo float64) {
	if r.Empty() {
		return
	}
	if err := procGdipCreateLineBrush.Find(); err != nil {
		FillRoundRectAAf(hdc, r, radius, from)
		return
	}
	if gradTo <= gradFrom {
		FillRoundRectAAf(hdc, r, radius, from)
		return
	}
	graphics := gdiplusGraphics(hdc)
	if graphics == 0 {
		FillRoundRectAAf(hdc, r, radius, from)
		return
	}
	defer procGdipDeleteGraphics.Call(graphics)

	path := roundRectPathF(r, radius)
	if path == 0 {
		FillRoundRectAAf(hdc, r, radius, from)
		return
	}
	defer procGdipDeletePath.Call(path)

	// Os dois pontos do gradiente vão em GpPointF (floats dentro da struct,
	// passada por ponteiro) — nunca como argumento escalar. O gradiente é
	// ancorado nos limites pedidos, independente do tamanho do preenchimento.
	p1 := gdiplusPointF{X: float32(gradFrom), Y: float32(r.Y)}
	p2 := gdiplusPointF{X: float32(gradTo), Y: float32(r.Y)}
	var brush uintptr
	status, _, _ := procGdipCreateLineBrush.Call(
		uintptr(unsafe.Pointer(&p1)),
		uintptr(unsafe.Pointer(&p2)),
		uintptr(argb(from)),
		uintptr(argb(to)),
		uintptr(0), // WrapModeTile
		uintptr(unsafe.Pointer(&brush)),
	)
	if status != 0 || brush == 0 {
		FillRoundRectAAf(hdc, r, radius, from)
		return
	}
	defer procGdipDeleteBrush.Call(brush)

	procGdipFillPath.Call(graphics, brush, path)
}
