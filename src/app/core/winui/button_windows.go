//go:build windows

package winui

// ButtonStyle descreve um botão desenhado pelo agente: fundo arredondado,
// variação de cor para hover/pressionado, borda opcional e anel de foco.
type ButtonStyle struct {
	Fill        Color
	HoverFill   Color
	PressedFill Color
	HasBorder   bool
	Border      Color
	Label       Color
	Radius      int
}

// fillFor devolve a cor de fundo considerando hover/pressionado.
func (s ButtonStyle) fillFor(hover, pressed bool) Color {
	switch {
	case pressed && s.PressedFill != 0:
		return s.PressedFill
	case hover && s.HoverFill != 0:
		return s.HoverFill
	default:
		return s.Fill
	}
}

// PaintButton desenha o botão (fundo + borda + anel de foco + rótulo).
// O anel de foco é desenhado 2px FORA do retângulo: mantenha folga em volta.
func PaintButton(hdc uintptr, r Rect, label string, font uintptr, style ButtonStyle, hover, pressed, focused bool, focusColor Color) {
	radius := style.Radius
	if radius <= 0 {
		radius = 8
	}
	FillRoundRect(hdc, r, radius, style.fillFor(hover, pressed))
	if style.HasBorder {
		StrokeRoundRect(hdc, r, radius, 1, style.Border)
	}
	if focused {
		StrokeRoundRect(hdc, r.InsetUniform(-3), radius+3, 2, focusColor)
	}
	DrawTextSingle(hdc, label, r, font, style.Label, AlignCenter)
}

// PillButton monta o estilo de um botão arredondado a partir de uma cor de
// fundo: o texto usa o contraste automático e hover/pressionado clareiam ou
// escurecem levemente (nunca mudam a legibilidade).
func PillButton(fill, hover, pressed Color, label Color, radius int) ButtonStyle {
	return ButtonStyle{
		Fill:        fill,
		HoverFill:   hover,
		PressedFill: pressed,
		Label:       label,
		Radius:      radius,
	}
}

// OutlineButton monta o estilo de um botão neutro (fundo de superfície + borda).
func OutlineButton(theme Theme, radius int) ButtonStyle {
	fill := theme.Surface
	hover := theme.Surface.Blend(theme.Text, 8)
	pressed := theme.Surface.Blend(theme.Text, 14)
	return ButtonStyle{
		Fill:        fill,
		HoverFill:   hover,
		PressedFill: pressed,
		HasBorder:   true,
		Border:      theme.Border,
		Label:       theme.Text,
		Radius:      radius,
	}
}
