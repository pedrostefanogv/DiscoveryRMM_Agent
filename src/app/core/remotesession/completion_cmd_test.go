//go:build windows

package remotesession

import (
	"context"
	"testing"
)

// cmdFS monta o cenário padrão dos testes do provider cmd.
func cmdFS() completionFS {
	return fakeCompletionFS(
		map[string][]completionFSEntry{
			"C:\\Users\\ana": {
				entryDir("Documents"),
				entryDir("Downloads"),
				entryDir("Program Files"),
				entryFile("notes.txt"),
				entryFile("relatorio final.docx"),
			},
			"C:\\Users\\ana\\Documents": {entryFile("cv.txt"), entryDir("antigo")},
			"C:\\Tools":                 {entryFile("git.exe"), entryFile("go.exe"), entryFile("readme.txt")},
			"C:\\Windows":               {entryDir("System32"), entryFile("win.ini")},
			"C:\\":                      {entryDir("Windows"), entryDir("Users")},
			// A raiz UNC canônica (filepath.Clean) mantém a barra final.
			"\\\\servidor\\share\\": {entryDir("pasta")},
		},
		[]string{"C:\\Windows\\System32", "C:\\Tools"},
		"C:\\Users\\ana",
		"C:\\Users\\ana",
	)
}

// completarCMD executa o provider cmd com o cursor no fim do input.
func completarCMD(t *testing.T, fs completionFS, input string) ([]completionMatch, tabExpansionResult) {
	t.Helper()
	p := newCmdCompletionProvider(fs)
	res, err := p.CompleteSpan(context.Background(), input, len([]rune(input)))
	if err != nil {
		t.Fatalf("CompleteSpan(%q) erro: %v", input, err)
	}
	return res.Matches, res
}

// ── Comandos internos no primeiro token ──

func TestCmdProvider_ComandoInternoNoPrimeiroToken(t *testing.T) {
	matches, res := completarCMD(t, cmdFS(), "di")
	m := exigirMatch(t, matches, "dir")
	if m.Type != "command" || m.Tooltip == "" {
		t.Fatalf("comando interno sem tipo/tooltip: %+v", m)
	}
	if res.ReplacementIndex != 0 || res.ReplacementLength != 2 {
		t.Fatalf("replacement = (%d,%d), want (0,2)", res.ReplacementIndex, res.ReplacementLength)
	}
	// Case-insensitive no Windows.
	matches, _ = completarCMD(t, cmdFS(), "DI")
	exigirMatch(t, matches, "dir")
}

// ── Executáveis do PATH ──

func TestCmdProvider_ExecutaveisDoPATH(t *testing.T) {
	matches, _ := completarCMD(t, cmdFS(), "gi")
	m := exigirMatch(t, matches, "git")
	if m.ListItem != "git.exe" || m.Type != "command" {
		t.Fatalf("sugestão de PATH inesperada: %+v", m)
	}
	// A extensão não vira sugestão.
	naoExigirMatch(t, matches, "git.exe")
}

// ── Arquivos/diretórios: caminho relativo, absoluto e barra final ──

func TestCmdProvider_ArquivoSemEspaco(t *testing.T) {
	matches, res := completarCMD(t, cmdFS(), "type no")
	exigirMatch(t, matches, "notes.txt")
	if res.ReplacementIndex != len([]rune("type ")) {
		t.Fatalf("replacementIndex = %d, want %d", res.ReplacementIndex, len([]rune("type ")))
	}
}

func TestCmdProvider_DiretorioComBarraFinal(t *testing.T) {
	matches, _ := completarCMD(t, cmdFS(), "cd Do")
	exigirMatch(t, matches, "Documents\\")
}

func TestCmdProvider_ArquivoComEspacoRecebeAspas(t *testing.T) {
	matches, _ := completarCMD(t, cmdFS(), "type re")
	m := exigirMatch(t, matches, "\"relatorio final.docx\"")
	if m.Type != "file" {
		t.Fatalf("tipo = %q, want file", m.Type)
	}
}

func TestCmdProvider_DiretorioComEspacoRecebeAspas(t *testing.T) {
	matches, _ := completarCMD(t, cmdFS(), "cd Pro")
	m := exigirMatch(t, matches, "\"Program Files\"\\")
	if m.Type != "directory" {
		t.Fatalf("tipo = %q, want directory", m.Type)
	}
}

func TestCmdProvider_PrefixoAbsoluto(t *testing.T) {
	matches, _ := completarCMD(t, cmdFS(), "type C:\\Users\\ana\\Doc")
	exigirMatch(t, matches, "C:\\Users\\ana\\Documents\\")
}

func TestCmdProvider_PrefixoRelativoComSubdiretorio(t *testing.T) {
	matches, _ := completarCMD(t, cmdFS(), "type Documents\\c")
	exigirMatch(t, matches, "Documents\\cv.txt")
}

// ── Caminho enraizado na unidade atual (`\Windows`) ──

// "\Windows\s" é relativo à RAIZ da unidade do cwd, não ao cwd: antes o Join
// com o cwd produzia "C:\<cwd>\Windows" e a listagem vinha vazia.
func TestCmdProvider_CaminhoEnraizadoNaUnidade(t *testing.T) {
	matches, _ := completarCMD(t, cmdFS(), "type \\Windows\\s")
	exigirMatch(t, matches, "\\Windows\\System32\\")

	// Raiz sozinha: lista o topo da unidade do cwd.
	matches, _ = completarCMD(t, cmdFS(), "type \\W")
	exigirMatch(t, matches, "\\Windows\\")

	// UNC já era absoluto e continua funcionando.
	matches, _ = completarCMD(t, cmdFS(), "type \\\\servidor\\share\\p")
	exigirMatch(t, matches, "\\\\servidor\\share\\pasta\\")
}

// ── Aspas já abertas ──

func TestCmdProvider_AspasAbertasContinuam(t *testing.T) {
	input := "cd \"C:\\Users\\ana\\Pro"
	matches, res := completarCMD(t, cmdFS(), input)
	exigirMatch(t, matches, "\"C:\\Users\\ana\\Program Files\"\\")
	if res.ReplacementIndex != 3 {
		t.Fatalf("replacementIndex = %d, want 3", res.ReplacementIndex)
	}
	if res.ReplacementLength != len([]rune(input))-3 {
		t.Fatalf("replacementLength = %d, want %d", res.ReplacementLength, len([]rune(input))-3)
	}
}

// ── Sem sugestão de opções ──

func TestCmdProvider_NaoSugereOpcoes(t *testing.T) {
	for _, input := range []string{"dir /", "dir /a", "dir -"} {
		matches, _ := completarCMD(t, cmdFS(), input)
		if len(matches) != 0 {
			t.Fatalf("%q deveria devolver zero sugestões; obtidos: %v", input, matchTexts(matches))
		}
	}
}

// ── Token depois de operador não é comando ──

func TestCmdProvider_TokenDepoisDeOperadorNaoEComando(t *testing.T) {
	matches, _ := completarCMD(t, cmdFS(), "dir & di")
	naoExigirMatch(t, matches, "dir")
	matches, _ = completarCMD(t, cmdFS(), "dir | no")
	exigirMatch(t, matches, "notes.txt")
}
