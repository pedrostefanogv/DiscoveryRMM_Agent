package app

import "testing"

func TestNormalizeSupportedLocale(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty defaults to pt-br", input: "", want: "pt-BR"},
		{name: "pt-br remains pt-br", input: "pt-BR", want: "pt-BR"},
		{name: "pt-pt collapses to pt-br", input: "pt-PT", want: "pt-BR"},
		{name: "english collapses to en-us", input: "en", want: "en-US"},
		{name: "en-gb collapses to en-us", input: "en-GB", want: "en-US"},
		{name: "underscore locale is normalized", input: "en_US", want: "en-US"},
		{name: "unsupported falls back to pt-br", input: "es-ES", want: "pt-BR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeSupportedLocale(tt.input)
			if got != tt.want {
				t.Fatalf("normalizeSupportedLocale(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
