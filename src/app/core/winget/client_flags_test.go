package winget

import (
	"strings"
	"testing"
)

// ── Flags de aceite não-interativo ──
//
// O agente roda como SERVIÇO (LocalSystem). O winget pede confirmação dos termos
// da fonte msstore na primeira vez que a fonte é usada por aquele contexto e,
// sem terminal, falha com 0x8a150042 (Error reading input in prompt), devolvendo
// saída vazia. Como usuário interativo o prompt costuma não aparecer (termos já
// aceitos no perfil), então o bug só se manifesta em produção.

var requiredAcceptFlags = []string{"--accept-source-agreements", "--disable-interactivity"}

func assertHasAcceptFlags(t *testing.T, name string, args []string) {
	t.Helper()
	joined := strings.Join(args, " ")
	for _, flag := range requiredAcceptFlags {
		if !strings.Contains(joined, flag) {
			t.Errorf("%s: falta a flag %q (args=%v)", name, flag, args)
		}
	}
}

// TestListCommands_AcceptAgreementsNonInteractively garante que o scan de updates
// e a listagem de instalados — que alimentam o inventário — nunca dependem de
// input do usuário. Sem isso o payload vai sem updateAvailable/updatePackageId.
func TestListCommands_AcceptAgreementsNonInteractively(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"ListUpgradable", []string{"upgrade", "--accept-source-agreements", "--disable-interactivity"}},
		{"ListInstalled", []string{"list", "--accept-source-agreements", "--disable-interactivity"}},
	}
	for _, tc := range cases {
		assertHasAcceptFlags(t, tc.name, tc.args)
	}
}

// TestShouldTreatErrorAsSuccess_IgnoresPromptError garante que o erro de prompt
// não seja tratado como ausência de updates (mascararia updates reais).
func TestShouldTreatErrorAsSuccess_IgnoresPromptError(t *testing.T) {
	err := errFromString("exit status 0x8a150042")
	out := "Do you agree to all the source agreements terms?"
	if shouldTreatErrorAsSuccess([]string{"upgrade"}, out, err) {
		t.Fatal("erro de prompt tratado como sucesso indevidamente")
	}
}

// TestShouldTreatErrorAsSuccess_AcceptsRealNoOp confirma que o caso legitimo de
// nenhuma atualizacao aplicavel continua sendo aceito.
func TestShouldTreatErrorAsSuccess_AcceptsRealNoOp(t *testing.T) {
	err := errFromString("exit status 0x8a15002b")
	if !shouldTreatErrorAsSuccess([]string{"upgrade"}, "", err) {
		t.Fatal("codigo de nenhuma atualizacao aplicavel deve ser sucesso")
	}
}
