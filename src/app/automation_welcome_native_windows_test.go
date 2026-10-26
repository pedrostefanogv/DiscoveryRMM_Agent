//go:build windows

package app

import "testing"

// normalizeProcessName alimenta a comparação de CloseProcesses do Welcome
// nativo: precisa tolerar "FIREFOX.EXE", " firefox ", "Firefox" e devolver a
// mesma chave para todos — igual ao PSADT, que compara sem o sufixo .exe.
func TestNormalizeProcessName(t *testing.T) {
	cases := map[string]string{
		"FIREFOX.EXE": "firefox",
		" firefox ":   "firefox",
		"Firefox":     "firefox",
		"firefox.exe": "firefox",
		"WINWORD.EXE": "winword",
		"":            "",
		"   ":         "",
		".exe":        "",
	}
	for input, want := range cases {
		if got := normalizeProcessName(input); got != want {
			t.Fatalf("normalizeProcessName(%q) = %q, want %q", input, got, want)
		}
	}
}

// O fechamento precisa ignorar listas vazias/inválidas sem tocar no snapshot
// de processos.
func TestCloseProcessesByNameEmptyNoOp(t *testing.T) {
	a := &App{}
	a.closeProcessesByName(nil)
	a.closeProcessesByName([]string{"", "   ", ".exe"})
}
