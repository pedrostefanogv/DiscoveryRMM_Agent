package supportchat

import "testing"

// ── Vocabulário de status: DOIS formatos circulam no agent ──────────────────
//
// BUG ENCONTRADO NA REVISÃO: normalizeStatus só reconhecia o formato do
// SERVIDOR (Pending/Active/Declined/Expired/Ended). O estado LOCAL e o que
// trafega por IPC já usam o vocabulário da JANELA (waiting/active/ended).
// Passar "waiting" devolvia StatusNone, o que fazia a adoção de estado via IPC
// ZERAR o convite em vez de adotá-lo — o cartão não aparecia e a janela não
// abria.
func TestNormalizeStatus_AceitaFormatoDoServidor(t *testing.T) {
	casos := map[string]string{
		"Pending":  StatusWaiting,
		"Active":   StatusActive,
		"Declined": StatusEnded,
		"Expired":  StatusEnded,
		"Ended":    StatusEnded,
	}
	for entrada, esperado := range casos {
		if got := normalizeStatus(entrada); got != esperado {
			t.Fatalf("normalizeStatus(%q) = %q; esperado %q", entrada, got, esperado)
		}
	}
}

// O vocabulário da JANELA precisa atravessar sem perder a informação.
func TestNormalizeStatus_AceitaFormatoDaJanela(t *testing.T) {
	casos := map[string]string{
		StatusWaiting: StatusWaiting,
		StatusActive:  StatusActive,
		StatusEnded:   StatusEnded,
		StatusNone:    StatusNone,
	}
	for entrada, esperado := range casos {
		if got := normalizeStatus(entrada); got != esperado {
			t.Fatalf("normalizeStatus(%q) = %q; esperado %q", entrada, got, esperado)
		}
	}
}

// Valor desconhecido/vazio nunca vira um estado operacional: cai em "none".
func TestNormalizeStatus_DesconhecidoViraNone(t *testing.T) {
	for _, entrada := range []string{"", "   ", "whatever"} {
		if got := normalizeStatus(entrada); got != StatusNone {
			t.Fatalf("normalizeStatus(%q) = %q; esperado %q", entrada, got, StatusNone)
		}
	}
}
