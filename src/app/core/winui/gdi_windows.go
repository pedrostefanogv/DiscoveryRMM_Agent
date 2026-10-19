//go:build windows

package winui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Desenho GDI ──
//
// As janelas nativas do agente não usam controles padrão do Win32 (que ignoram
// o tema claro/escuro e ficam com aquele visual "cinza 95"): o cartão, os
// textos e os botões são desenhados no DC da janela com GDI + double buffering.
// Aqui ficam os primitivos compartilhados pelo banner e pelo prompt.

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procFillRect              = user32.NewProc("FillRect")
	procDrawTextW             = user32.NewProc("DrawTextW")
	procSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
	procGetSystemMetrics      = user32.NewProc("GetSystemMetrics")
	procGetDC                 = user32.NewProc("GetDC")
	procReleaseDC             = user32.NewProc("ReleaseDC")

	procCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	procCreatePen              = gdi32.NewProc("CreatePen")
	procCreateFontW            = gdi32.NewProc("CreateFontW")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procSetBkMode              = gdi32.NewProc("SetBkMode")
	procSetTextColor           = gdi32.NewProc("SetTextColor")
	procRoundRect              = gdi32.NewProc("RoundRect")
	procEllipse                = gdi32.NewProc("Ellipse")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procGetStockObject         = gdi32.NewProc("GetStockObject")
)

const (
	// transparent é o fundo de texto transparente (SetBkMode).
	transparent = 1
	// psSolid é a caneta sólida do CreatePen.
	psSolid = 0
	// nullPen/nullBrush são objetos de estoque do GDI (5 = NULL_BRUSH;
	// NÃO usar 1, que é LTGRAY_BRUSH e pintaria o cartão inteiro de cinza).
	nullPen   = 8
	nullBrush = 5
	// srcCopy é o ROP do BitBlt.
	srcCopy = 0x00CC0020
	// cleartypeQuality/defaultCharset do CreateFontW.
	cleartypeQuality = 5
	defaultCharset   = 1
	// spiGetWorkArea é o índice do SystemParametersInfoW.
	spiGetWorkArea = 48
	// smCXScreen/smCYScreen do GetSystemMetrics.
	smCXScreen = 0
	smCYScreen = 1
	// dtCalcRect/dtWordBreak/dtNoPrefix simulam/medem o texto sem desenhar.
	dtCalcRect     = 0x00000400
	dtWordBreak    = 0x00000010
	dtNoPrefix     = 0x00000800
	dtWordEllipsis = 0x00040000
)

// Rect é um retângulo em pixels da janela (origem no canto superior esquerdo).
type Rect struct {
	X int
	Y int
	W int
	H int
}

// Right devolve o limite direito (exclusivo).
func (r Rect) Right() int { return r.X + r.W }

// Bottom devolve o limite inferior (exclusivo).
func (r Rect) Bottom() int { return r.Y + r.H }

// Empty indica retângulo sem área.
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Contains informa se o ponto está dentro do retângulo.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.Right() && y >= r.Y && y < r.Bottom()
}

// Inset encolhe o retângulo em dx/dy pixels de cada lado.
func (r Rect) Inset(dx, dy int) Rect {
	return Rect{X: r.X + dx, Y: r.Y + dy, W: r.W - 2*dx, H: r.H - 2*dy}
}

// InsetUniform encolhe o retângulo em d pixels em todos os lados.
func (r Rect) InsetUniform(d int) Rect { return r.Inset(d, d) }

// Offset desloca o retângulo.
func (r Rect) Offset(dx, dy int) Rect { return Rect{X: r.X + dx, Y: r.Y + dy, W: r.W, H: r.H} }

// nativeRect espelha RECT do Win32.
type nativeRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

func (r Rect) native() nativeRect {
	return nativeRect{Left: int32(r.X), Top: int32(r.Y), Right: int32(r.Right()), Bottom: int32(r.Bottom())}
}

// FillRect preenche um retângulo com a cor.
func FillRect(hdc uintptr, r Rect, color Color) {
	if r.Empty() {
		return
	}
	brush, _, _ := procCreateSolidBrush.Call(color.ref())
	if brush == 0 {
		return
	}
	nr := r.native()
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&nr)), brush)
	procDeleteObject.Call(brush)
}

// FillRoundRect preenche um retângulo arredondado (sem borda).
func FillRoundRect(hdc uintptr, r Rect, radius int, color Color) {
	roundRect(hdc, r, radius, true, color, 0, 0)
}

// StrokeRoundRect desenha apenas a borda arredondada.
func StrokeRoundRect(hdc uintptr, r Rect, radius, width int, color Color) {
	roundRect(hdc, r, radius, false, 0, width, color)
}

// FillRoundRectBorder preenche e desenha a borda arredondada de 1px.
func FillRoundRectBorder(hdc uintptr, r Rect, radius int, fill, border Color) {
	roundRect(hdc, r, radius, true, fill, 1, border)
}

// roundRect desenha um RoundRect com preenchimento/borda opcionais.
func roundRect(hdc uintptr, r Rect, radius int, fillEnabled bool, fill Color, borderWidth int, border Color) {
	if r.Empty() {
		return
	}
	if radius < 0 {
		radius = 0
	}

	var brush uintptr
	if fillEnabled {
		brush, _, _ = procCreateSolidBrush.Call(fill.ref())
		if brush != 0 {
			defer procDeleteObject.Call(brush)
		}
	}

	var pen uintptr
	if borderWidth > 0 {
		pen, _, _ = procCreatePen.Call(psSolid, uintptr(borderWidth), border.ref())
		if pen != 0 {
			defer procDeleteObject.Call(pen)
		}
	}
	if brush == 0 && pen == 0 {
		return
	}

	var oldBrush, oldPen uintptr
	if brush != 0 {
		oldBrush, _, _ = procSelectObject.Call(hdc, brush)
	} else {
		stock, _, _ := procGetStockObject.Call(nullBrush)
		oldBrush, _, _ = procSelectObject.Call(hdc, stock)
	}
	if pen != 0 {
		oldPen, _, _ = procSelectObject.Call(hdc, pen)
	} else {
		stock, _, _ := procGetStockObject.Call(nullPen)
		oldPen, _, _ = procSelectObject.Call(hdc, stock)
	}

	procRoundRect.Call(
		hdc,
		uintptr(r.X), uintptr(r.Y), uintptr(r.Right()), uintptr(r.Bottom()),
		uintptr(radius*2), uintptr(radius*2),
	)

	procSelectObject.Call(hdc, oldBrush)
	procSelectObject.Call(hdc, oldPen)
}

// FillEllipse preenche uma elipse inscrita no retângulo.
func FillEllipse(hdc uintptr, r Rect, color Color) {
	if r.Empty() {
		return
	}
	brush, _, _ := procCreateSolidBrush.Call(color.ref())
	if brush == 0 {
		return
	}
	defer procDeleteObject.Call(brush)
	pen, _, _ := procGetStockObject.Call(nullPen)
	oldBrush, _, _ := procSelectObject.Call(hdc, brush)
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	procEllipse.Call(hdc, uintptr(r.X), uintptr(r.Y), uintptr(r.Right()), uintptr(r.Bottom()))
	procSelectObject.Call(hdc, oldBrush)
	procSelectObject.Call(hdc, oldPen)
}

// Alinhamentos horizontais de texto.
const (
	AlignLeft = iota
	AlignCenter
	AlignRight
)

// CreateFont cria uma fonte Segoe UI no tamanho (altura) informado em pixels.
// weight 400 = normal, 600 = semibold, 700 = bold.
func CreateFont(sizePx, weight int) uintptr {
	if sizePx <= 0 {
		sizePx = 12
	}
	face, err := windows.UTF16PtrFromString("Segoe UI")
	if err != nil {
		return 0
	}
	font, _, _ := procCreateFontW.Call(
		negUintptr(sizePx), // altura de caractere (negativo = usa a altura, não a célula)
		0, 0, 0,
		uintptr(weight), 0, 0, 0,
		defaultCharset, 0, 0, cleartypeQuality, 0,
		uintptr(unsafe.Pointer(face)),
	)
	return font
}

// DeleteGDIObject libera um objeto GDI (fonte, bitmap, ...).
func DeleteGDIObject(handle uintptr) {
	if handle != 0 {
		procDeleteObject.Call(handle)
	}
}

// negUintptr converte um inteiro positivo em uintptr negativo (convenção do
// CreateFontW para altura de caractere).
func negUintptr(value int) uintptr {
	return uintptr(^uint32(value) + 1)
}

// drawFlags monta as flags do DrawTextW conforme o alinhamento.
func drawFlags(align int, singleLine, ellipsis bool) uint32 {
	var flags uint32 = 0x00000800 // DT_NOPREFIX
	switch align {
	case AlignCenter:
		flags |= 0x00000001 // DT_CENTER
	case AlignRight:
		flags |= 0x00000002 // DT_RIGHT
	}
	if singleLine {
		flags |= 0x00000020 | 0x00000004 // DT_SINGLELINE | DT_VCENTER
	}
	if ellipsis {
		flags |= 0x00008000 // DT_END_ELLIPSIS
	}
	return flags
}

// DrawTextSingle desenha uma linha única (com reticências se não couber).
func DrawTextSingle(hdc uintptr, text string, r Rect, font uintptr, color Color, align int) {
	drawText(hdc, text, r, font, color, drawFlags(align, true, true))
}

// DrawTextWrapped desenha texto com quebra de linha automática.
func DrawTextWrapped(hdc uintptr, text string, r Rect, font uintptr, color Color, align int) {
	drawText(hdc, text, r, font, color, drawFlags(align, false, false)|dtWordBreak)
}

// DrawTextWrappedEllipsis é igual a DrawTextWrapped, mas sinaliza truncamento
// com "…" na última linha visível (usado na mensagem do consentimento quando
// um texto extremo excede a altura máxima do cartão).
func DrawTextWrappedEllipsis(hdc uintptr, text string, r Rect, font uintptr, color Color, align int) {
	drawText(hdc, text, r, font, color, drawFlags(align, false, false)|dtWordBreak|dtWordEllipsis)
}

// drawText chama o DrawTextW com a fonte/cor informadas.
func drawText(hdc uintptr, text string, r Rect, font uintptr, color Color, flags uint32) {
	if text == "" || r.Empty() || hdc == 0 {
		return
	}
	ptr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	var oldFont uintptr
	if font != 0 {
		oldFont, _, _ = procSelectObject.Call(hdc, font)
	}
	procSetBkMode.Call(hdc, transparent)
	procSetTextColor.Call(hdc, color.ref())
	nr := r.native()
	procDrawTextW.Call(
		hdc,
		uintptr(unsafe.Pointer(ptr)),
		^uintptr(0), // -1: string terminada em NUL
		uintptr(unsafe.Pointer(&nr)),
		uintptr(flags),
	)
	if oldFont != 0 {
		procSelectObject.Call(hdc, oldFont)
	}
}

// MeasureWrappedText devolve a altura (px) que o texto ocupa ao ser quebrado
// em largura width com a fonte informada — sem desenhar nada (DT_CALCRECT).
// Usado para dimensionar dinamicamente a caixa da mensagem do consentimento.
func MeasureWrappedText(font uintptr, text string, width int) int {
	if font == 0 || text == "" || width <= 0 {
		return 0
	}
	dc, _, _ := procGetDC.Call(0)
	if dc == 0 {
		return 0
	}
	defer procReleaseDC.Call(0, dc)

	ptr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return 0
	}
	oldFont, _, _ := procSelectObject.Call(dc, font)
	defer procSelectObject.Call(dc, oldFont)

	nr := nativeRect{Left: 0, Top: 0, Right: int32(width)}
	procDrawTextW.Call(
		dc,
		uintptr(unsafe.Pointer(ptr)),
		^uintptr(0),
		uintptr(unsafe.Pointer(&nr)),
		uintptr(dtCalcRect|dtWordBreak|dtNoPrefix),
	)
	return int(nr.Bottom - nr.Top)
}

// WorkArea devolve a área útil do monitor primário (sem a barra de tarefas).
func WorkArea() Rect {
	var nr nativeRect
	ret, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&nr)), 0)
	if ret == 0 {
		width, _, _ := procGetSystemMetrics.Call(smCXScreen)
		height, _, _ := procGetSystemMetrics.Call(smCYScreen)
		return Rect{W: int(width), H: int(height)}
	}
	return Rect{X: int(nr.Left), Y: int(nr.Top), W: int(nr.Right - nr.Left), H: int(nr.Bottom - nr.Top)}
}

// CenteredOnWorkArea devolve a posição para centralizar uma janela no monitor
// primário (levemente acima do centro, como os diálogos do Windows).
func CenteredOnWorkArea(width, height int) (x, y int) {
	area := WorkArea()
	x = area.X + (area.W-width)/2
	y = area.Y + (area.H-height)/2
	if y < area.Y {
		y = area.Y
	}
	return x, y
}

// bottomRightInArea calcula a posição ancorada no canto inferior direito de
// area, recuada por margin pixels das bordas direita/inferior.
func bottomRightInArea(area Rect, width, height, margin int) (x, y int) {
	if margin < 0 {
		margin = 0
	}
	x = area.Right() - width - margin
	y = area.Bottom() - height - margin
	if x < area.X {
		x = area.X
	}
	if y < area.Y {
		y = area.Y
	}
	return x, y
}

// BottomRightOnWorkArea devolve a posição para ancorar uma janela no canto
// inferior direito do monitor primário, acima da barra de tarefas — o mesmo
// canto das notificações/toasts do Windows (comportamento adotado no prompt de
// consentimento remoto para não cobrir o centro da tela do usuário).
func BottomRightOnWorkArea(width, height, margin int) (x, y int) {
	return bottomRightInArea(WorkArea(), width, height, margin)
}

// BottomRightOnActiveWorkArea ancora a janela no canto inferior direito do
// monitor em que o usuário está trabalhando (janela em primeiro plano), e não
// necessariamente no primário: em setup multi-monitor o prompt precisa nascer
// no monitor ativo para ser visto antes do timeout. Sem janela em primeiro
// plano, usa o primário.
func BottomRightOnActiveWorkArea(width, height, margin int) (x, y int) {
	return bottomRightInArea(ActiveWorkArea(), width, height, margin)
}
