//go:build windows

package app

// Testes de regressão do guard-rail UIPI do controle remoto.
//
// BUG de referência ("controle remoto morre ao abrir o Gerenciador de
// Tarefas / a UI do agente; volta ao fechá-las"): um injetor de input com
// integridade menor que a janela em PRIMEIRO PLANO tem o SendInput
// descartado pelo UIPI (ERROR_ACCESS_DENIED, errno=5). Gerenciador de
// Tarefas roda SEMPRE High (manifest autoElevate) e a UI do agente é High
// (manifest requireAdministrator) — um worker Medium perde cliques/teclado
// enquanto qualquer uma delas tem foco. Estes testes travam a regra:
//
//	1. Somente High/System suportam injeção em todas as janelas;
//	2. hardenUserSessionToken NUNCA devolve um token Medium em silêncio —
//	   ou devolve High/System, ou devolve erro (fail-visible).
//
// As capacidades reais dependem de como o teste roda (elevado ou não), então
// as asserts são sobre INVARIANTES, não sobre valores absolutos.

import (
	"testing"

	"discovery/app/core/platform"

	"golang.org/x/sys/windows"
)

func TestIntegritySupportsUipiInjection(t *testing.T) {
	cases := map[platform.IntegrityLevel]bool{
		platform.IntegritySystem:    true,
		platform.IntegrityHigh:      true,
		platform.IntegrityMedium:    false,
		platform.IntegrityLow:       false,
		platform.IntegrityUntrusted: false,
		platform.IntegrityUnknown:   false,
	}
	for level, want := range cases {
		if got := platform.IntegritySupportsUipiInjection(level); got != want {
			t.Errorf("IntegritySupportsUipiInjection(%s) = %t, want %t", level, got, want)
		}
	}
}

// TestTokenIntegrityLevelCurrentProcess garante que a leitura por token
// funciona e classifica o processo de teste em um nível conhecido. Antes do
// fix a checagem do spawn era tok.IsElevated() (flag de UAC) — insuficiente:
// o que decide o UIPI é o rótulo de integridade do token.
func TestTokenIntegrityLevelCurrentProcess(t *testing.T) {
	il := platform.TokenIntegrityLevel(windows.GetCurrentProcessToken())
	if il == platform.IntegrityUnknown {
		t.Fatalf("integridade do processo corrente não pôde ser lida")
	}
	t.Logf("integridade do processo de teste: %s elevated=%t", il, platform.IsRunningElevated())
}

// TestHardenUserSessionTokenNeverReturnsMediumSilently é o guard-rail
// anti-regressão: hardenUserSessionToken JAMAIS pode devolver um token Medium
// sem erro. Com caller Medium (teste não elevado) e linked token indisponível
// ele deve FALHAR com erro explícito; com caller SYSTEM/High (ou linked
// disponível) deve devolver High/System. O bug original era o caminho
// silencioso: worker Medium no ar e ninguém sabia.
func TestHardenUserSessionTokenNeverReturnsMediumSilently(t *testing.T) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &tok); err != nil {
		t.Skipf("sem acesso ao token do processo: %v", err)
	}

	hard, err := hardenUserSessionToken(tok)
	// hard pode ser o próprio tok (não fechado) ou um novo token — feche ao sair.
	defer func() {
		if hard != 0 {
			hard.Close()
		}
	}()

	il := platform.TokenIntegrityLevel(hard)
	if err == nil && !platform.IntegritySupportsUipiInjection(il) {
		t.Fatalf("hardenUserSessionToken devolveu token %s SEM erro — worker Medium silencioso voltou a ser possível", il)
	}
	if err != nil {
		t.Logf("harden falhou como esperado neste contexto (caller IL=%s): %v", platform.TokenIntegrityLevel(hard), err)
	}
}
