package notifyui

import "testing"

// normalizeIconKind mapeia aliases do contrato para os 4 kinds canônicos.
func TestNormalizeIconKind(t *testing.T) {
	cases := map[string]string{
		"":          IconInfo,
		"info":      IconInfo,
		"WARNING":   IconWarning,
		"warn":      IconWarning,
		"Error":     IconError,
		"question":  IconQuestion,
		"desconhecido": IconInfo,
	}
	for in, want := range cases {
		if got := normalizeIconKind(in); got != want {
			t.Fatalf("normalizeIconKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// Todos os glifos do Material Symbols flatten em subpaths fechados plausíveis
// (o primeiro subpath dos ícones circulares/triângulo contorna o corpo).
func TestParseIconSubpaths(t *testing.T) {
	for _, kind := range []string{IconInfo, IconWarning, IconError, IconQuestion} {
		sps := parseIconSubpaths(kind)
		if len(sps) == 0 {
			t.Fatalf("kind %q não flattenou nenhum subpath", kind)
		}
		for i, sp := range sps {
			if len(sp) < 3 {
				t.Fatalf("kind %q subpath %d com %d pontos", kind, i, len(sp))
			}
			for _, p := range sp {
				// viewBox 24x24: nada pode escapar muito disso.
				if p.X < -1 || p.X > 25 || p.Y < -1 || p.Y > 25 {
					t.Fatalf("kind %q ponto fora do viewBox: %+v", kind, p)
				}
			}
		}
	}
}

// Kind desconhecido não desenha nada (nil).
func TestParseIconSubpathsUnknown(t *testing.T) {
	if sps := parseIconSubpaths("desconhecido"); sps != nil {
		t.Fatalf("esperava nil, got %d subpaths", len(sps))
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
