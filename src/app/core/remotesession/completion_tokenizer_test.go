//go:build windows

package remotesession

import "testing"

// ── Tokenização: aspas contam como conteúdo do token ──

// Um argumento entre aspas (mesmo vazio) é um token: o token seguinte NÃO pode
// ser tratado como comando (Indice 0). Antes da correção, "cmd \"a b\" " dava
// Indice 0 no token vazio depois do espaço e o TAB oferecia builtins/PATH em
// vez de só arquivos.
func TestTokenizeCompletion_TokenDepoisDeAspasNaoEComando(t *testing.T) {
	casos := []struct {
		nome   string
		input  string
		style  completionQuoteStyle
		indice int
		inicio int
		texto  string
	}{
		{
			nome:   "posix: argumento entre aspas fechadas",
			input:  "\"a b\" c",
			style:  completionQuotePOSIX,
			indice: 1,
			inicio: 6,
			texto:  "c",
		},
		{
			nome:   "cmd: caminho com espaço entre aspas fechadas",
			input:  "\"C:\\Program Files\" xx",
			style:  completionQuoteCMD,
			indice: 1,
			inicio: 19,
			texto:  "xx",
		},
		{
			nome:   "posix: token vazio logo após o espaço",
			input:  "\"cmd\" ",
			style:  completionQuotePOSIX,
			indice: 1,
			inicio: 6,
			texto:  "",
		},
		{
			nome:   "posix: aspas vazias também são token",
			input:  "\"\" x",
			style:  completionQuotePOSIX,
			indice: 1,
			inicio: 3,
			texto:  "x",
		},
	}
	for _, c := range casos {
		tok := tokenizeCompletion(c.input, len([]rune(c.input)), c.style)
		if tok.Indice != c.indice || tok.Inicio != c.inicio || tok.Texto != c.texto {
			t.Fatalf("%s: tokenize(%q) = {Indice:%d Inicio:%d Texto:%q}, want {%d %d %q}",
				c.nome, c.input, tok.Indice, tok.Inicio, tok.Texto, c.indice, c.inicio, c.texto)
		}
	}
}

// O primeiro token continua sendo o comando (aspas não mudam isso).
func TestTokenizeCompletion_AspasNoPrimeiroToken(t *testing.T) {
	tok := tokenizeCompletion("\"C:\\Program Files\\x.exe\" -", len([]rune("\"C:\\Program Files\\x.exe\" -")), completionQuoteCMD)
	if tok.Indice != 1 {
		t.Fatalf("Indice = %d, want 1 (segundo token)", tok.Indice)
	}
}

// ── POSIX: dentro de "..." o '\' só escapa $ crase " \ e newline ──

func TestUnquoteCompletionToken_PosixAspasDuplas(t *testing.T) {
	casos := []struct {
		nome  string
		input string
		style completionQuoteStyle
		quer  string
	}{
		{
			nome:  "barra antes de letra é literal (caminho do Git Bash)",
			input: "\"C:\\Users\\ana\"",
			style: completionQuotePOSIX,
			quer:  "C:\\Users\\ana",
		},
		{
			nome:  "barra escapa a própria barra",
			input: "\"a\\\\b\"",
			style: completionQuotePOSIX,
			quer:  "a\\b",
		},
		{
			nome:  "barra escapa aspas duplas",
			input: "\"a\\\"b\"",
			style: completionQuotePOSIX,
			quer:  "a\"b",
		},
		{
			nome:  "barra escapa $",
			input: "\"a\\$b\"",
			style: completionQuotePOSIX,
			quer:  "a$b",
		},
		{
			nome:  "aspas simples: barra é sempre literal",
			input: "'a\\b'",
			style: completionQuotePOSIX,
			quer:  "a\\b",
		},
		{
			nome:  "fora de aspas a barra sempre escapa",
			input: "a\\ b",
			style: completionQuotePOSIX,
			quer:  "a b",
		},
	}
	for _, c := range casos {
		if got := unquoteCompletionToken(c.input, c.style); got != c.quer {
			t.Fatalf("%s: unquote(%q) = %q, want %q", c.nome, c.input, got, c.quer)
		}
	}
}

// ── Providers: nenhuma sugestão de comando depois de um token entre aspas ──

func TestCmdProvider_NaoSugereComandoDepoisDeArgumentoEntreAspas(t *testing.T) {
	matches, _ := completarCMD(t, cmdFS(), "cd \"Program Files\" ")
	naoExigirMatch(t, matches, "dir")
	naoExigirMatch(t, matches, "echo")
	// Continua listando o diretório corrente (o TAB ali completa arquivos).
	exigirMatch(t, matches, "Documents\\")
}

func TestPOSIXProvider_NaoSugereComandoDepoisDeArgumentoEntreAspas(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "cat 'my file' ")
	naoExigirMatch(t, matches, "echo")
	exigirMatch(t, matches, "Documents/")
}
