package winget

import (
	"context"
	"testing"
)

// A extração do LOCALAPPDATA define QUAL winget será executado no contexto do
// usuário. Antes usávamos HasPrefix(ToUpper(kv), ...) + kv[len(prefix):], que
// pode fatiar fora de faixa quando ToUpper altera o comprimento da string.
func TestEnvValue(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		key  string
		want string
	}{
		{name: "basico", env: []string{"LOCALAPPDATA=C:\\Users\\u\\AppData\\Local"}, key: "LOCALAPPDATA", want: "C:\\Users\\u\\AppData\\Local"},
		{name: "case-insensitive na chave", env: []string{"localappdata=C:\\x"}, key: "LOCALAPPDATA", want: "C:\\x"},
		{name: "ausente", env: []string{"APPDATA=C:\\a"}, key: "LOCALAPPDATA", want: ""},
		{name: "valor com igual", env: []string{"LOCALAPPDATA=C:\\a=b"}, key: "LOCALAPPDATA", want: "C:\\a=b"},
		{name: "entrada sem igual", env: []string{"SEMIGUAL"}, key: "SEMIGUAL", want: ""},
		{name: "valor com espacos", env: []string{"LOCALAPPDATA=  C:\\y  "}, key: "LOCALAPPDATA", want: "C:\\y"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envValue(tc.env, tc.key); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Sem token no ctx não há resolução por usuário: o caller usa o caminho normal.
func TestResolveUserContextExecutable_NoToken(t *testing.T) {
	got, err := resolveUserContextExecutable(context.Background())
	if err != nil {
		t.Fatalf("sem token nao deveria errar: %v", err)
	}
	if got != "" {
		t.Fatalf("sem token deveria devolver vazio, got %q", got)
	}
}
