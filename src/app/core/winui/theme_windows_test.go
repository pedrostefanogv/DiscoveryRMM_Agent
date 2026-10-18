//go:build windows

package winui

import "testing"

func TestRGBChannels(t *testing.T) {
	color := RGB(0x12, 0x34, 0x56)
	if color.R() != 0x12 || color.G() != 0x34 || color.B() != 0x56 {
		t.Fatalf("canais errados: r=%02x g=%02x b=%02x", color.R(), color.G(), color.B())
	}
}

func TestBlendAndContrastText(t *testing.T) {
	black := RGB(0, 0, 0)
	white := RGB(255, 255, 255)
	if got := black.Blend(white, 0); got != black {
		t.Fatalf("blend 0%% = %v, want preto", got)
	}
	if got := black.Blend(white, 100); got != white {
		t.Fatalf("blend 100%% = %v, want branco", got)
	}
	if got := black.Blend(white, 50); got.Luminance() < 0.4 || got.Luminance() > 0.6 {
		t.Fatalf("blend 50%% fora do meio: %v", got)
	}
	if got := black.ContrastText(); got.Luminance() < 0.9 {
		t.Fatalf("texto sobre preto deveria ser claro: %v", got)
	}
	if got := white.ContrastText(); got.Luminance() > 0.2 {
		t.Fatalf("texto sobre branco deveria ser escuro: %v", got)
	}
}

func TestColorFromABGRAndARGB(t *testing.T) {
	// AccentColor do DWM é 0xAABBGGRR (azul default do Windows: 0xFFD47800).
	if got := colorFromABGR(0xFFD47800); got != RGB(0x00, 0x78, 0xD4) {
		t.Fatalf("ABGR errado: %v", got)
	}
	// ColorizationColor é 0xAARRGGBB.
	if got := colorFromARGB(0xC40078D4); got != RGB(0x00, 0x78, 0xD4) {
		t.Fatalf("ARGB errado: %v", got)
	}
}

func TestThemeForPrefs(t *testing.T) {
	light := ThemeForPrefs(true, RGB(0x00, 0x78, 0xD4))
	if light.Dark {
		t.Fatal("tema claro marcado como escuro")
	}
	dark := ThemeForPrefs(false, RGB(0x00, 0x78, 0xD4))
	if !dark.Dark {
		t.Fatal("tema escuro não marcado")
	}
	if light.Card == dark.Card {
		t.Fatal("cartão claro e escuro iguais")
	}
	// Texto sempre legível sobre o cartão (contraste mínimo grosseiro).
	if diff := light.Card.Luminance() - light.Text.Luminance(); diff < 0.5 {
		t.Fatalf("texto claro sem contraste: %f", diff)
	}
	if diff := dark.Text.Luminance() - dark.Card.Luminance(); diff < 0.5 {
		t.Fatalf("texto escuro sem contraste: %f", diff)
	}
}

func TestThemeKeepsDefaultAccentWhenUnusable(t *testing.T) {
	for _, accent := range []Color{0, RGB(0, 0, 0), RGB(255, 255, 255)} {
		theme := ThemeForPrefs(true, accent)
		if theme.Accent != DefaultAccent {
			t.Fatalf("accent %v deveria cair no default, veio %v", accent, theme.Accent)
		}
	}
	theme := ThemeForPrefs(true, RGB(0x8A, 0x2B, 0xE2))
	if theme.Accent != RGB(0x8A, 0x2B, 0xE2) {
		t.Fatalf("accent utilizável ignorado: %v", theme.Accent)
	}
	if theme.AccentText.Luminance() < 0.5 {
		t.Fatal("texto sobre accent roxo deveria ser claro")
	}
}

func TestUsableAccent(t *testing.T) {
	cases := map[Color]bool{
		0:                     false,
		RGB(0, 0, 0):          false,
		RGB(255, 255, 255):    false,
		RGB(0x00, 0x78, 0xD4): true,
	}
	for color, want := range cases {
		if got := usableAccent(color); got != want {
			t.Fatalf("usableAccent(%v) = %v, want %v", color, got, want)
		}
	}
}

func TestRectHelpers(t *testing.T) {
	rect := Rect{X: 10, Y: 20, W: 100, H: 40}
	if rect.Right() != 110 || rect.Bottom() != 60 {
		t.Fatalf("limites errados: %d/%d", rect.Right(), rect.Bottom())
	}
	if !rect.Contains(10, 20) || !rect.Contains(109, 59) {
		t.Fatal("pontos internos recusados")
	}
	if rect.Contains(110, 20) || rect.Contains(10, 60) {
		t.Fatal("limite exclusivo deveria ficar fora")
	}
	inner := rect.Inset(5, 5)
	if inner != (Rect{X: 15, Y: 25, W: 90, H: 30}) {
		t.Fatalf("inset errado: %+v", inner)
	}
	if !(Rect{}).Empty() {
		t.Fatal("retângulo zero deveria ser vazio")
	}
}
