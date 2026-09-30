package ai

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestFormatToolProgressStatus cobre o formato de wire do status de progresso
// por ferramenta consumido por describeChatActivity (frontend/js/app-chat.js).
//
// Contexto (2026-09-30): num lote de 4 upgrade_package o core emitia apenas um
// status para o lote inteiro, então a UI mostrava o mesmo rótulo por ~5min
// enquanto o winget/choco executava os itens em sequência.
func TestFormatToolProgressStatus(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		index int
		total int
		args  string
		want  string
	}{
		{
			name:  "lote com hint de id",
			tool:  "upgrade_package",
			index: 2,
			total: 4,
			args:  `{"id":"Google.Chrome.EXE"}`,
			want:  "Executando: upgrade_package (2/4) [Google.Chrome.EXE]...",
		},
		{
			name:  "lote sem hint aproveitavel",
			tool:  "get_pending_updates",
			index: 1,
			total: 3,
			args:  `{}`,
			want:  "Executando: get_pending_updates (1/3)...",
		},
		{
			name:  "tool unica nao repete o contador",
			tool:  "upgrade_all_packages",
			index: 1,
			total: 1,
			args:  `{"confirm":true}`,
			want:  "Executando: upgrade_all_packages...",
		},
		{
			name:  "args invalidos nao quebram o status",
			tool:  "ping_host",
			index: 1,
			total: 2,
			args:  `nao-e-json`,
			want:  "Executando: ping_host (1/2)...",
		},
		{
			name:  "campo vazio cai para o proximo candidato",
			tool:  "install_package",
			index: 1,
			total: 2,
			args:  `{"id":"   ","name":"Kudu"}`,
			want:  "Executando: install_package (1/2) [Kudu]...",
		},
		{
			name:  "colchetes do hint sao neutralizados",
			tool:  "install_package",
			index: 1,
			total: 2,
			args:  `{"id":"Pkg[beta]\nline2"}`,
			want:  "Executando: install_package (1/2) [Pkg(beta) line2]...",
		},
		{
			name:  "virgula no hint nao quebra a lista de tools",
			tool:  "install_package",
			index: 1,
			total: 2,
			args:  `{"name":"Foo, Bar"}`,
			want:  "Executando: install_package (1/2) [Foo; Bar]...",
		},
		{
			name:  "candidato nao-string cai para o proximo",
			tool:  "install_package",
			index: 1,
			total: 2,
			args:  `{"id":123,"name":"Kudu"}`,
			want:  "Executando: install_package (1/2) [Kudu]...",
		},
		{
			// O contador real fica FORA do bloco: o parser do frontend ancora no
			// fim da string, então "(1/9)" dentro do hint não confunde.
			name:  "parenteses no hint ficam dentro do bloco",
			tool:  "install_package",
			index: 2,
			total: 3,
			args:  `{"id":"Foo (1/9)"}`,
			want:  "Executando: install_package (2/3) [Foo (1/9)]...",
		},
		{
			name:  "data url nao vira rotulo",
			tool:  "capture_screenshot",
			index: 1,
			total: 2,
			args:  `{"path":"data:image/png;base64,AAAA"}`,
			want:  "Executando: capture_screenshot (1/2)...",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatToolProgressStatus(tc.tool, tc.index, tc.total, tc.args)
			if got != tc.want {
				t.Fatalf("formatToolProgressStatus() = %q, want %q", got, tc.want)
			}
			// Invariantes do parser do frontend: o status sempre termina em
			// "..." e o bloco de hint (quando existe) fecha com um unico "]".
			if !strings.HasSuffix(got, "...") {
				t.Errorf("status deve terminar em reticencias: %q", got)
			}
			if strings.Count(got, "[") != strings.Count(got, "]") {
				t.Errorf("bloco de hint desbalanceado: %q", got)
			}
			// O parser do frontend divide o status por "," para listar várias
			// tools: uma vírgula vinda do hint criaria uma "tool" fantasma.
			if strings.Count(got, ",") != 0 {
				t.Errorf("status de uma unica tool nao pode conter virgula: %q", got)
			}
		})
	}
}

// TestToolProgressHintTrunca verifica o teto de 60 runes do hint (o rótulo vai
// para um chip da UI) e que ele respeita limites de rune multiplos bytes.
func TestToolProgressHintTrunca(t *testing.T) {
	long := strings.Repeat("á", 80)
	got := toolProgressHint(`{"id":"` + long + `"}`)
	if utf8Len := len([]rune(got)); utf8Len != 61 { // 60 runes + "…"
		t.Fatalf("hint truncado tem %d runes, esperado 61: %q", utf8Len, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("hint truncado deve terminar em …: %q", got)
	}
	// O corte por runes não pode gerar bytes UTF-8 inválidos (o status vai para
	// JSON e para o DOM).
	if !utf8.ValidString(got) {
		t.Fatalf("hint truncado gerou UTF-8 inválido: %q", got)
	}
}
