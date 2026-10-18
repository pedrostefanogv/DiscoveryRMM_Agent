//go:build windows

package winui

import "testing"

func TestButtonStyleFillForStates(t *testing.T) {
	style := ButtonStyle{Fill: RGB(1, 1, 1), HoverFill: RGB(2, 2, 2), PressedFill: RGB(3, 3, 3)}
	if got := style.fillFor(false, false); got != style.Fill {
		t.Fatalf("normal = %v", got)
	}
	if got := style.fillFor(true, false); got != style.HoverFill {
		t.Fatalf("hover = %v", got)
	}
	if got := style.fillFor(true, true); got != style.PressedFill {
		t.Fatalf("pressionado deveria vencer o hover: %v", got)
	}
	// Sem variantes definidas mantém o fundo (nenhum botão "some" no hover).
	plain := ButtonStyle{Fill: RGB(9, 9, 9)}
	if got := plain.fillFor(true, true); got != plain.Fill {
		t.Fatalf("fallback = %v", got)
	}
}

func TestOutlineButtonUsesThemeColors(t *testing.T) {
	theme := LightTheme()
	style := OutlineButton(theme, 9)
	if style.Fill != theme.Surface || style.Label != theme.Text || !style.HasBorder || style.Border != theme.Border {
		t.Fatalf("estilo neutro inesperado: %+v", style)
	}
}
