//go:build windows

// Package winui desenha as janelas Win32 próprias do agente (banner da sessão
// remota e prompt de consentimento) com a MESMA identidade visual do Windows do
// usuário atendido: tema claro/escuro (AppsUseLightTheme) e cor de destaque
// (DWM AccentColor).
//
// O worker da sessão remota roda como SYSTEM dentro da sessão interativa; por
// isso o tema NÃO pode ser lido do HKCU do processo (que seria o perfil do
// SYSTEM) — ele é lido de HKEY_USERS\<SID> do dono do explorer.exe da sessão
// (ver user_theme_windows.go).
package winui

// Color é um COLORREF do Win32 (0x00BBGGRR): o byte baixo é vermelho.
// Usar COLORREF (e não RGB) evita conversões erradas entre GDI e registry.
type Color uint32

// RGB monta a cor a partir dos canais RGB.
func RGB(red, green, blue uint8) Color {
	return Color(uint32(red) | uint32(green)<<8 | uint32(blue)<<16)
}

// R devolve o canal vermelho.
func (c Color) R() uint8 { return uint8(c) }

// G devolve o canal verde.
func (c Color) G() uint8 { return uint8(uint32(c) >> 8) }

// B devolve o canal azul.
func (c Color) B() uint8 { return uint8(uint32(c) >> 16) }

// ref devolve o COLORREF no formato esperado pelas chamadas GDI.
func (c Color) ref() uintptr { return uintptr(c) }

// Luminance estima o brilho percebido (0 = preto, 1 = branco). Usada para
// decidir texto claro/escuro e recusar cores de destaque ilegíveis.
func (c Color) Luminance() float64 {
	channel := func(v uint8) float64 { return float64(v) / 255.0 }
	return 0.2126*channel(c.R()) + 0.7152*channel(c.G()) + 0.0722*channel(c.B())
}

// Blend mistura c com other: percent=0 devolve c, 100 devolve other.
func (c Color) Blend(other Color, percent int) Color {
	if percent <= 0 {
		return c
	}
	if percent >= 100 {
		return other
	}
	mix := func(a, b uint8) uint8 {
		return uint8((int(a)*(100-percent) + int(b)*percent + 50) / 100)
	}
	return RGB(mix(c.R(), other.R()), mix(c.G(), other.G()), mix(c.B(), other.B()))
}

// Lighten aproxima a cor do branco.
func (c Color) Lighten(percent int) Color { return c.Blend(RGB(0xFF, 0xFF, 0xFF), percent) }

// Darken aproxima a cor do preto.
func (c Color) Darken(percent int) Color { return c.Blend(RGB(0x00, 0x00, 0x00), percent) }

// ContrastText devolve o texto legível sobre c (quase-preto ou branco).
func (c Color) ContrastText() Color {
	if c.Luminance() > 0.55 {
		return RGB(0x10, 0x10, 0x10)
	}
	return RGB(0xFF, 0xFF, 0xFF)
}

// DefaultAccent é o azul padrão do Windows 11, usado quando o accent do usuário
// não está disponível (perfil não carregado, cor ilegível, ...).
var DefaultAccent = RGB(0x00, 0x78, 0xD4)

// Theme é a paleta das janelas nativas do agente — deliberadamente enxuta:
// superfícies, texto e três cores de ação (destaque, permitir, negar).
type Theme struct {
	// Dark indica tema escuro do Windows (AppsUseLightTheme = 0).
	Dark bool

	// Card é o fundo da janela (cartão).
	Card Color
	// Surface é o fundo de blocos internos (ex.: caixa da mensagem).
	Surface Color
	// Border é a cor de divisórias/bordas e do trilho da barra de progresso.
	Border Color
	// Text é o texto principal.
	Text Color
	// TextMuted é o texto secundário (subtítulo, dicas, rótulos).
	TextMuted Color

	// Accent é a cor de destaque do Windows do usuário.
	Accent Color
	// AccentText é o texto legível sobre Accent.
	AccentText Color

	// Success/rótulos da ação "permitir".
	Success     Color
	SuccessText Color

	// Danger/rótulos da ação "negar"/encerrar.
	Danger     Color
	DangerText Color
}

// LightTheme devolve a paleta clara (Fluent light).
func LightTheme() Theme {
	return Theme{
		Card:        RGB(0xF3, 0xF3, 0xF3),
		Surface:     RGB(0xFF, 0xFF, 0xFF),
		Border:      RGB(0xE0, 0xE0, 0xE0),
		Text:        RGB(0x1B, 0x1B, 0x1B),
		TextMuted:   RGB(0x5C, 0x5C, 0x5C),
		Accent:      DefaultAccent,
		AccentText:  RGB(0xFF, 0xFF, 0xFF),
		Success:     RGB(0x0F, 0x7B, 0x0F),
		SuccessText: RGB(0xFF, 0xFF, 0xFF),
		Danger:      RGB(0xC4, 0x2B, 0x1C),
		DangerText:  RGB(0xFF, 0xFF, 0xFF),
	}
}

// DarkTheme devolve a paleta escura (Fluent dark).
func DarkTheme() Theme {
	return Theme{
		Dark:        true,
		Card:        RGB(0x20, 0x20, 0x20),
		Surface:     RGB(0x2C, 0x2C, 0x2C),
		Border:      RGB(0x3D, 0x3D, 0x3D),
		Text:        RGB(0xFF, 0xFF, 0xFF),
		TextMuted:   RGB(0xB4, 0xB4, 0xB4),
		Accent:      RGB(0x60, 0xCD, 0xFF),
		AccentText:  RGB(0x05, 0x2A, 0x3B),
		Success:     RGB(0x6C, 0xCB, 0x5F),
		SuccessText: RGB(0x0B, 0x2A, 0x08),
		Danger:      RGB(0xFF, 0x99, 0xA4),
		DangerText:  RGB(0x2B, 0x05, 0x08),
	}
}

// ThemeForPrefs monta a paleta a partir do modo claro/escuro e do destaque do
// usuário. Aceita apenas destaques legíveis; caso contrário mantém o default.
func ThemeForPrefs(useLightTheme bool, accent Color) Theme {
	theme := DarkTheme()
	if useLightTheme {
		theme = LightTheme()
	}
	theme = themeWithAccent(theme, accent)
	return theme
}

// themeWithAccent aplica o destaque do usuário quando ele é utilizável.
func themeWithAccent(theme Theme, accent Color) Theme {
	if !usableAccent(accent) {
		return theme
	}
	theme.Accent = accent
	theme.AccentText = accent.ContrastText()
	// Em tema escuro um accent muito escuro se perde no cartão; clareia.
	if theme.Dark && accent.Luminance() < 0.35 {
		theme.Accent = accent.Lighten(35)
		theme.AccentText = theme.Accent.ContrastText()
	}
	return theme
}

// usableAccent descarta cores ausentes/quase puras preto/branco, que
// desapareceriam no cartão ou no anel de foco.
func usableAccent(accent Color) bool {
	if accent == 0 {
		return false
	}
	lum := accent.Luminance()
	return lum > 0.06 && lum < 0.94
}

// colorFromABGR converte o formato do valor DWM\AccentColor (0xAABBGGRR).
func colorFromABGR(value uint32) Color {
	return RGB(
		uint8(value&0xFF),
		uint8((value>>8)&0xFF),
		uint8((value>>16)&0xFF),
	)
}

// colorFromARGB converte o formato do valor DWM\ColorizationColor (0xAARRGGBB).
func colorFromARGB(value uint32) Color {
	return RGB(
		uint8((value>>16)&0xFF),
		uint8((value>>8)&0xFF),
		uint8(value&0xFF),
	)
}
