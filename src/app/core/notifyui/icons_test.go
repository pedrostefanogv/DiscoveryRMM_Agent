package notifyui

import "testing"

// normalizeIconKind mapeia aliases do contrato para os 4 kinds canônicos.
func TestNormalizeIconKind(t *testing.T) {
	cases := map[string]string{
		"":             IconInfo,
		"info":         IconInfo,
		"WARNING":      IconWarning,
		"warn":         IconWarning,
		"Error":        IconError,
		"question":     IconQuestion,
		"desconhecido": IconInfo,
	}
	for in, want := range cases {
		if got := normalizeIconKind(in); got != want {
			t.Fatalf("normalizeIconKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// Todos os glifos do Material Symbols flatten em subpaths fechados plausíveis
// dentro do viewBox (0 -960 960 960), incluindo os glifos extras (chat, desktop).
func TestParseIconSubpaths(t *testing.T) {
	for _, kind := range []string{IconInfo, IconWarning, IconError, IconQuestion, GlyphChat, GlyphDesktop} {
		sps := parseGlyph(kind)
		if len(sps) == 0 {
			t.Fatalf("glifo %q não flattenou nenhum subpath", kind)
		}
		for i, sp := range sps {
			if len(sp) < 3 {
				t.Fatalf("glifo %q subpath %d com %d pontos", kind, i, len(sp))
			}
			for _, p := range sp {
				// viewBox do Material Symbols (0 -960 960 960).
				if p.X < -5 || p.X > iconViewBoxSize+5 || p.Y < iconViewBoxMinY-5 || p.Y > 5 {
					t.Fatalf("glifo %q ponto fora do viewBox: %+v", kind, p)
				}
			}
		}
	}
}

// Regressão: 'm' relativo seguido de pares de coordenadas implícitos deve
// continuar relativo ('l'), não virar 'L' absoluto.
func TestFlattenSVGPathRelativeMoveToLine(t *testing.T) {
	sps := flattenSVGPath("m240-240-92 92 100 0z")
	if len(sps) != 1 {
		t.Fatalf("subpaths = %d, want 1", len(sps))
	}
	want := []iconPoint{
		{240, -240},
		{148, -148},
		{248, -148},
		{240, -240},
	}
	if len(sps[0]) != len(want) {
		t.Fatalf("pontos = %v, want %v", sps[0], want)
	}
	for i := range want {
		if sps[0][i] != want[i] {
			t.Fatalf("ponto[%d] = %v, want %v", i, sps[0][i], want[i])
		}
	}
}

// Glifo desconhecido não desenha nada (nil).
func TestParseGlyphUnknown(t *testing.T) {
	if sps := parseGlyph("desconhecido"); sps != nil {
		t.Fatalf("esperava nil, got %d subpaths", len(sps))
	}
}

// Os glifos extras (chat, desktop) e os kinds flatten no mesmo conjunto.
func TestParseGlyphExtras(t *testing.T) {
	for _, name := range []string{GlyphChat, GlyphDesktop} {
		if sps := parseGlyph(name); len(sps) == 0 {
			t.Fatalf("glifo %q não flattenou", name)
		}
	}
}

// NormalizeCardRequest preenche o Icon default (info) e preserva o informado.
func TestNormalizeCardRequestIcon(t *testing.T) {
	if got := NormalizeCardRequest(CardRequest{Title: "x"}).Icon; got != IconInfo {
		t.Fatalf("icon default = %q, want %q", got, IconInfo)
	}
	if got := NormalizeCardRequest(CardRequest{Title: "x", Icon: "ERROR"}).Icon; got != IconError {
		t.Fatalf("icon = %q, want %q", got, IconError)
	}
}
