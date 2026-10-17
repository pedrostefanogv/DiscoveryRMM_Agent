//go:build windows

package remotesession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── Utilidades de teste dos providers de completação ──
//
// Nenhum teste toca o FS real: completionFS é sempre injetado.

// dirKey normaliza a chave de diretório do FS falso (mesma regra do provider).
func dirKey(dir string) string {
	return filepath.Clean(dir)
}

// fakeCompletionFS monta um FS em memória. dirs mapeia diretório ->
// entradas; diretório ausente devolve os.ErrNotExist.
func fakeCompletionFS(dirs map[string][]completionFSEntry, pathDirs []string, home, cwd string) completionFS {
	normalizado := make(map[string][]completionFSEntry, len(dirs))
	for k, v := range dirs {
		normalizado[dirKey(k)] = v
	}
	return completionFS{
		ListDir: func(d string) ([]completionFSEntry, error) {
			if e, ok := normalizado[dirKey(d)]; ok {
				return e, nil
			}
			return nil, os.ErrNotExist
		},
		PathDirs: func() []string { return pathDirs },
		HomeDir:  func() string { return home },
		Cwd:      func() (string, error) { return cwd, nil },
	}
}

// entryFile/entryDir encurtam a montagem do FS falso.
func entryFile(nome string) completionFSEntry { return completionFSEntry{Name: nome} }
func entryDir(nome string) completionFSEntry  { return completionFSEntry{Name: nome, IsDir: true} }

// matchTexts devolve os Text sugeridos (para asserts legíveis).
func matchTexts(matches []completionMatch) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.Text)
	}
	return out
}

// temMatch informa se algum Text é exatamente igual a quer.
func temMatch(matches []completionMatch, quer string) bool {
	for _, m := range matches {
		if m.Text == quer {
			return true
		}
	}
	return false
}

// procuraText devolve o match cujo Text casa com a condição (nil se nenhum).
func procuraText(matches []completionMatch, quer string) *completionMatch {
	for i := range matches {
		if matches[i].Text == quer {
			return &matches[i]
		}
	}
	return nil
}

// exigirMatch falha o teste com uma mensagem útil quando o item não veio.
func exigirMatch(t *testing.T, matches []completionMatch, quer string) completionMatch {
	t.Helper()
	if m := procuraText(matches, quer); m != nil {
		return *m
	}
	t.Fatalf("match %q ausente; obtidos: %v", quer, matchTexts(matches))
	return completionMatch{}
}

// naoExigirMatch falha quando o item apareceu.
func naoExigirMatch(t *testing.T, matches []completionMatch, proibido string) {
	t.Helper()
	if m := procuraText(matches, proibido); m != nil {
		t.Fatalf("match %q nao deveria aparecer; obtidos: %v", proibido, matchTexts(matches))
	}
}

// contemTexto informa se algum Text contém a substring.
func contemTexto(matches []completionMatch, sub string) bool {
	for _, m := range matches {
		if strings.Contains(m.Text, sub) {
			return true
		}
	}
	return false
}
