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
	// FocusGap/FocusWidth permitem escalar o anel de foco (DPI). Zero usa os
	// padrões de projeto (4px de folga, 2px de espessura).
	FocusGap   int
	FocusWidth int
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
	fill := style.fillFor(hover, pressed)
	if style.HasBorder {
		// Borda + preenchimento em uma passada antialiased (GDI+): o GDI puro
		// deixava o canto arredondado serrilhado nos botões.
		FillRoundRectBorderAA(hdc, r, radius, fill, style.Border)
	} else {
		FillRoundRectAA(hdc, r, radius, fill)
	}
	if focused {
		// Anel com 2px de folga em relação ao botão (mesma geometria do desenho
		// GDI anterior), agora com cantos antialiased. Gap/espessura podem vir
		// escalados pelo DPI via ButtonStyle.
		gap := style.FocusGap
		if gap <= 0 {
			gap = 4
		}
		ringWidth := style.FocusWidth
		if ringWidth <= 0 {
			ringWidth = 2
		}
		StrokeRoundRectAA(hdc, r.InsetUniform(-gap), radius+gap, ringWidth, focusColor)
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
