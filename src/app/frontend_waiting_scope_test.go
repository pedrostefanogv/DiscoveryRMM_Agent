package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readServerAuthJs(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "frontend", "js", "server-authorization.js"))
	if err != nil {
		t.Fatalf("ler server-authorization.js: %v", err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// As telas de CONTEÚDO só podem entrar no painel de espera no modo
// "aguardando liberação". O modo "sem credenciais" tem tela própria (overlay de
// provisionamento) e mensagem diferente — usar o texto de liberação ali seria
// mentir para o usuário sobre o que está faltando.
func TestScreensOnlyUseWaitingPanelForApprovalMode(t *testing.T) {
	for _, f := range []string{"app-store-updates.js", "app-support.js", "app-knowledge.js"} {
		raw, err := os.ReadFile(filepath.Join("..", "frontend", "js", f))
		if err != nil {
			t.Fatalf("ler %s: %v", f, err)
		}
		js := strings.ReplaceAll(string(raw), "\r\n", "\n")
		if strings.Contains(js, "isOnboardingBlocked()") {
			t.Fatalf("%s não pode usar isOnboardingBlocked(): o painel de espera explica falta de "+
				"LIBERAÇÃO, não falta de credenciais", f)
		}
	}
}

// renderServerAuthWaitingState precisa falar de liberação (o único modo em que
// é usada hoje) — e o módulo precisa deixar claro qual modo ele retrata.
func TestWaitingPanelCopyIsAboutAuthorization(t *testing.T) {
	js := readServerAuthJs(t)
	if !strings.Contains(js, "serverAuth.pending.message") {
		t.Fatal("o painel de espera precisa usar a cópia de 'aguardando liberação do servidor'")
	}
	// isServerAuthorizationPending é estrito: só awaiting-approval.
	if !strings.Contains(js, "return __onboardingMode === 'awaiting-approval';") {
		t.Fatal("isServerAuthorizationPending precisa ser estrito a 'awaiting-approval'")
	}
}
