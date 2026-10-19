package windowsupdate

import (
	"strings"
	"testing"
)

// Caso REAL da maquina 019faa6f: o WMI devolve Caption = URL de suporte e a
// extracao antiga (VARIANT aposentado + SysStringLen) zerava TODA string,
// gerando titulos como "Atualizacao KB5127690". Estes testes fixam a
// normalizacao do que deve aparecer na aba de atualizacoes.
func TestNormalizeQFETitleRealCaptions(t *testing.T) {
	cases := []struct {
		name        string
		kb          string
		caption     string
		desc        string
		want        string
		placeholder bool
	}{
		{
			name:        "caption real da KB",
			kb:          "5127690",
			caption:     "2026-10 Atualizacao cumulativa para Windows 11 Version 25H2 (KB5127690)",
			desc:        "http://support.microsoft.com/?kbid=5127690",
			want:        "2026-10 Atualizacao cumulativa para Windows 11 Version 25H2 (KB5127690)",
			placeholder: false,
		},
		{
			name:        "caption e apenas a URL do suporte",
			kb:          "5127690",
			caption:     "http://support.microsoft.com/?kbid=5127690",
			want:        "Atualizacao KB5127690",
			placeholder: true,
		},
		{
			name:        "caption e URL help kb",
			kb:          "5127846",
			caption:     "https://support.microsoft.com/help/5127846",
			desc:        "Atualizacao de seguranca",
			want:        "Atualizacao KB5127846",
			placeholder: true,
		},
		{
			name:        "caption vazio bug real string zerada",
			kb:          "5127839",
			caption:     "",
			want:        "Atualizacao KB5127839",
			placeholder: true,
		},
		{
			name:        "caption apenas o numero do KB",
			kb:          "KB5127839",
			caption:     "KB5127839",
			want:        "Atualizacao KB5127839",
			placeholder: true,
		},
		{
			name:        "caption com espacos e quebras de linha",
			kb:          "5034441",
			caption:     "  Atualizacao   para Windows 11 \r\n (KB5034441)  ",
			want:        "Atualizacao para Windows 11 (KB5034441)",
			placeholder: false,
		},
		{
			name:        "sem kb e sem caption",
			kb:          "",
			caption:     "",
			want:        "Atualizacao do Windows",
			placeholder: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeQFETitle(tc.kb, tc.caption, tc.desc)
			if got.Title != tc.want {
				t.Fatalf("Title=%q want %q", got.Title, tc.want)
			}
			if got.Placeholder != tc.placeholder {
				t.Fatalf("Placeholder=%v want %v", got.Placeholder, tc.placeholder)
			}
		})
	}
}

func TestNormalizeQFETitleDoesNotEchoURLAsTitle(t *testing.T) {
	info := NormalizeQFETitle("5127690", "https://support.microsoft.com/?kbid=5127690", "x")
	if strings.Contains(info.Title, "http") {
		t.Fatalf("titulo nao pode ser URL: %q", info.Title)
	}
}

func TestIsQFEURLCaption(t *testing.T) {
	for _, caption := range []string{
		"http://support.microsoft.com/?kbid=5127690",
		"https://support.microsoft.com/help/5127690/",
		"HTTPS://Support.Microsoft.com/help/5127690",
	} {
		if !IsQFEURLCaption(caption) {
			t.Fatalf("%q deveria ser reconhecida como URL de KB", caption)
		}
	}
	for _, caption := range []string{
		"Atualizacao cumulativa (KB5127690)",
		"",
		"http://example.com/download",
	} {
		if IsQFEURLCaption(caption) {
			t.Fatalf("%q NAO deveria ser URL de KB", caption)
		}
	}
}
