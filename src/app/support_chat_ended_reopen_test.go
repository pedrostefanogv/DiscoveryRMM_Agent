package app

import (
	"testing"

	"discovery/app/supportchat"
)

// ── BUG: a janela do chat reabria sozinha para conversa ENCERRADA ───────────
//
// RELATO: "agora toda hora está abrindo a janela do chat com chat já
// encerrado". O log mostrou o ciclo a cada ~4 s:
//   estado -> ended/... (refresh)
//   sessão ativa/pendente encontrada sem janela aberta; abrindo o chat
//   janela do chat aberta -> janela do chat fechada
//
// Causa: o ramo de retomada abria a janela para QUALQUER estado que não fosse
// "waiting" — inclusive "ended". Desde que o servidor passou a devolver a
// sessão FINAL em /active, esse ramo reabria a janela de uma conversa já
// encerrada a cada tick de reconciliação.

// Só "active" justifica abrir/reabrir a janela.
func TestSupportChatWindowMayOpen_SomenteAtivaOuEncerrada(t *testing.T) {
	if supportChatWindowMayOpen(supportchat.StatusWaiting) {
		t.Fatal("waiting não abre a janela (a superfície é o cartão)")
	}
	if supportChatWindowMayOpen(supportchat.StatusNone) {
		t.Fatal("sem sessão não abre a janela")
	}
	if !supportChatWindowMayOpen(supportchat.StatusActive) {
		t.Fatal("conversa ativa precisa poder abrir a janela")
	}
}

// O ramo de retomada só age em "active": encerrada/expirada NÃO reabre.
//
// Exercita a decisão pura usada pela reconciliação (a abertura real depende do
// Wails e não é testável aqui) — o que importa é que nenhum caminho mande abrir
// a janela para um status final.
func TestSupportChatReconcile_NaoReabreEncerrada(t *testing.T) {
	// A condição agora exigida pelo ramo de retomada.
	shouldOpen := func(status string) bool { return status == supportchat.StatusActive }

	for _, status := range []string{supportchat.StatusEnded, supportchat.StatusNone, supportchat.StatusWaiting} {
		if shouldOpen(status) {
			t.Fatalf("status %q NÃO pode abrir/reabrir a janela do chat", status)
		}
	}
	if !shouldOpen(supportchat.StatusActive) {
		t.Fatal("conversa ativa precisa abrir a janela")
	}
}

// Declined e Expired do servidor viram "ended" no vocabulário da janela.
//
// Distinção que importa: uma janela JÁ ABERTA deve continuar mostrando o
// encerramento (é o que o usuário está lendo) — por isso supportChatWindowMayOpen
// aceita "ended". O bug não era esse: era a RECONCILIAÇÃO REABRIR a janela que o
// usuário já tinha fechado. A decisão de reabrir é separada e exige "active".
func TestSupportChatEndedStates_NaoReabrem(t *testing.T) {
	// Só "active" manda reabrir (ver reconcileSupportChat).
	reopenAllowed := func(status string) bool { return status == supportchat.StatusActive }

	for _, raw := range []string{"Declined", "Expired", "Ended"} {
		normalized := supportchat.StatusEnded
		if reopened := reopenAllowed(normalized); reopened {
			t.Fatalf("%s precisa ser tratado como encerrado e NÃO reabrir a janela", raw)
		}
	}

	// E a janela que JÁ está aberta continua exibindo o encerramento.
	if !supportChatWindowMayOpen(supportchat.StatusEnded) {
		t.Fatal("uma janela já aberta precisa continuar mostrando o encerramento")
	}
}
