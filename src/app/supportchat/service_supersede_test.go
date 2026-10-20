package supportchat

import (
	"context"
	"testing"
	"time"
)

// RELATO: "mando convite e a notificação não abre". Com um convite ANTERIOR
// ainda aguardando resposta na mesma máquina, um convite NOVO precisa
// SUBSTITUÍ-LO (não voltar "busy"), senão o usuário nunca recebe o pedido.
func TestPrepareInviteIfIdle_NovoConviteSubstituiAnteriorPendente(t *testing.T) {
	svc := New(Deps{
		Ctx:  func() context.Context { return context.Background() },
		Logf: func(string) {},
		Emit: func(string, ...any) {},
	})

	if d := svc.PrepareInviteIfIdle(Invite{SessionID: "sessao-1", ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}); !d.Accepted {
		t.Fatalf("primeiro convite deveria ser aceito: %+v", d)
	}
	second := svc.PrepareInviteIfIdle(Invite{SessionID: "sessao-2", ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
	if !second.Accepted {
		t.Fatalf("o convite novo deveria substituir o pendente; motivo=%q", second.Reason)
	}
	if second.SupersededSessionID != "sessao-1" {
		t.Fatalf("a sessão antiga deveria ser devolvida para recusa 'replaced'; obtido %q", second.SupersededSessionID)
	}
	if got := svc.State().SessionID; got != "sessao-2" {
		t.Fatalf("o estado deveria ser do convite novo; obtido %q", got)
	}
}
