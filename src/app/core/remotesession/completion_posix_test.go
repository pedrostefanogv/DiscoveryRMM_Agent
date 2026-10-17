//go:build windows

package remotesession

import (
	"context"
	"strings"
	"testing"
)

// posixFS monta o cenário padrão dos testes do provider POSIX.
func posixFS() completionFS {
	return fakeCompletionFS(
		map[string][]completionFSEntry{
			"/home/ana": {
				entryDir("Documents"),
				entryDir("Downloads"),
				entryFile("my file.txt"),
				entryFile(".env"),
				entryFile("relatorio.txt"),
			},
			"/home/ana/Documents": {entryFile("cv.txt")},
			"/usr":                {entryDir("bin")},
			"/usr/bin":            {entryFile("git"), entryFile("grep"), entryFile("ls")},
		},
		[]string{"/usr/bin"},
		"/home/ana",
		"/home/ana",
	)
}

// completarPOSIX executa o provider POSIX com o cursor no fim do input.
func completarPOSIX(t *testing.T, fs completionFS, input string) ([]completionMatch, tabExpansionResult) {
	t.Helper()
	p := newPOSIXCompletionProvider(fs)
	res, err := p.CompleteSpan(context.Background(), input, len([]rune(input)))
	if err != nil {
		t.Fatalf("CompleteSpan(%q) erro: %v", input, err)
	}
	return res.Matches, res
}

// ── Primeiro token: builtins e PATH ──

func TestPOSIXProvider_BuiltinNoPrimeiroToken(t *testing.T) {
	matches, res := completarPOSIX(t, posixFS(), "ec")
	m := exigirMatch(t, matches, "echo")
	if m.Type != "command" || m.Tooltip == "" {
		t.Fatalf("builtin sem tipo/tooltip: %+v", m)
	}
	if res.ReplacementIndex != 0 || res.ReplacementLength != 2 {
		t.Fatalf("replacement = (%d,%d), want (0,2)", res.ReplacementIndex, res.ReplacementLength)
	}
}

func TestPOSIXProvider_ExecutaveisDoPATH(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "gre")
	m := exigirMatch(t, matches, "grep")
	if m.Type != "command" {
		t.Fatalf("sugestão de PATH com tipo %q, want command", m.Type)
	}
	// No primeiro token sem caminho, arquivos do cwd não entram (só builtins/PATH).
	naoExigirMatch(t, matches, "relatorio.txt")
}

func TestPOSIXProvider_PrimeiroTokenComCaminhoListaArquivos(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "cat ./re")
	exigirMatch(t, matches, "./relatorio.txt")
}

// ── Diretórios: barra final ──

func TestPOSIXProvider_DiretorioComBarraFinal(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "cd Do")
	m := exigirMatch(t, matches, "Documents/")
	if m.Type != "directory" {
		t.Fatalf("tipo = %q, want directory", m.Type)
	}
}

// ── Quoting de arquivo com espaço ──

func TestPOSIXProvider_ArquivoComEspacoRecebeAspas(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "cat my")
	m := exigirMatch(t, matches, "'my file.txt'")
	if m.ListItem != "my file.txt" {
		t.Fatalf("listItem = %q, want my file.txt", m.ListItem)
	}
}

func TestPOSIXProvider_AspasSimplesAbertasContinuam(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "cat 'my")
	exigirMatch(t, matches, "'my file.txt'")
}

func TestPOSIXProvider_AspasDuplasAbertasContinuam(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "cat \"my")
	exigirMatch(t, matches, "\"my file.txt\"")
}

// ── Ocultos só com prefixo "." ──

func TestPOSIXProvider_OcultosSoComPrefixoPonto(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "cat e")
	for _, m := range matches {
		if strings.Contains(m.Text, ".env") {
			t.Fatalf("arquivo oculto sugerido sem prefixo '.': %+v", m)
		}
	}
	matches, _ = completarPOSIX(t, posixFS(), "cat .e")
	exigirMatch(t, matches, ".env")
}

// ── Prefixos relativo/absoluto e "~/" ──

func TestPOSIXProvider_PrefixoRelativoComSubdiretorio(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "cat Documents/c")
	exigirMatch(t, matches, "Documents/cv.txt")
}

func TestPOSIXProvider_PrefixoAbsoluto(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "cat /usr/bi")
	exigirMatch(t, matches, "/usr/bin/")
}

func TestPOSIXProvider_PrefixoHomeComTil(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "cd ~/Do")
	exigirMatch(t, matches, "~/Documents/")
	// "~" sozinho lista o home mantendo o prefixo "~/".
	matches, _ = completarPOSIX(t, posixFS(), "cd ~")
	if !contemTexto(matches, "~/Documents/") {
		t.Fatalf("'~' deveria sugerir ~/Documents/; obtidos: %v", matchTexts(matches))
	}
}

// ── Token depois de operador não é comando ──

func TestPOSIXProvider_TokenDepoisDeOperadorNaoEComando(t *testing.T) {
	matches, _ := completarPOSIX(t, posixFS(), "ls | ec")
	naoExigirMatch(t, matches, "echo")
	matches, _ = completarPOSIX(t, posixFS(), "ls | re")
	exigirMatch(t, matches, "relatorio.txt")
}
