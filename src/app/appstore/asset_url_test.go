package appstore

import "testing"

// TestAbsoluteAssetURL cobre a resolução de URLs relativas devolvidas pela API
// (ícone de pacote custom). Sem isso a <img> do WebView do agent resolve a URL
// contra a origem local do Wails e a imagem não carrega.
func TestAbsoluteAssetURL(t *testing.T) {
	cfg := DebugConfig{ApiScheme: "https", ApiServer: "tngplacas.com.br"}

	tests := []struct {
		name string
		cfg  DebugConfig
		raw  string
		want string
	}{
		{"relativa vira absoluta", cfg, "/api/v1/app-store/custom/foxitedito/icon", "https://tngplacas.com.br/api/v1/app-store/custom/foxitedito/icon"},
		{"absoluta nao muda", cfg, "https://cdn.exemplo/logo.png", "https://cdn.exemplo/logo.png"},
		{"vazia continua vazia", cfg, "", ""},
		{"sem servidor mantem relativa", DebugConfig{ApiScheme: "https"}, "/api/v1/x", "/api/v1/x"},
		{"scheme http respeitado", DebugConfig{ApiScheme: "http", ApiServer: "host.local:8080"}, "/api/v1/x", "http://host.local:8080/api/v1/x"},
		{"scheme vazio assume https", DebugConfig{ApiServer: "host.local"}, "/api/v1/x", "https://host.local/api/v1/x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := absoluteAssetURL(tt.cfg, tt.raw); got != tt.want {
				t.Fatalf("absoluteAssetURL(%q) = %q, esperado %q", tt.raw, got, tt.want)
			}
		})
	}
}
