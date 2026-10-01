package ai

import "testing"

func TestResolveRoundSessionID(t *testing.T) {
	cases := []struct {
		name                   string
		previous, returned, fb string
		want                   string
	}{
		{"id novo vence", "sess-1", "sess-2", "turno", "sess-2"},
		{"round falhou mantem sessao", "sess-1", "", "turno", "sess-1"},
		{"sem anterior usa fallback", "", "", "turno", "turno"},
		{"espacos contam como vazio", "  ", "  ", "turno", "turno"},
		{"retorno com espacos e valido", "", "  sess-3 ", "turno", "  sess-3 "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveRoundSessionID(tc.previous, tc.returned, tc.fb); got != tc.want {
				t.Fatalf("resolveRoundSessionID(%q,%q,%q) = %q, want %q", tc.previous, tc.returned, tc.fb, got, tc.want)
			}
		})
	}
}
