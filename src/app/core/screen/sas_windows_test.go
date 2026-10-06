//go:build windows

package screen

import "testing"

// O caminho é interpolado na mensagem de erro do SendSAS — um separador
// perdido faz o operador receber um caminho inválido e a leitura do valor
// falhar silenciosamente ("ausente" mesmo com a política configurada).
func TestSASPolicyKeyPath(t *testing.T) {
	want := `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`
	if sasPolicyKey != want {
		t.Fatalf("caminho da politica incorreto:\n got=%q\nwant=%q", sasPolicyKey, want)
	}
}
