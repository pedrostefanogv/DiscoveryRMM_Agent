package supportchat

import (
	"encoding/json"
	"testing"
)

// ── Evento durável SessionDeclined (recusa/expiração do convite) ────────────
//
// BUG CORRIGIDO: o switch de HandleEnvelope só tratava SessionOpened, Message e
// SessionEnded. Quando a sessão era RECUSADA (pelo usuário em outra máquina, ou
// pelo POST que não chegou) ou EXPIRAVA no servidor, nada saía para o agent
// nesse canal: ele ficava preso em "waiting" — cartão na tela e console
// "Aguardando confirmação" — até um polling REST.

func declinedEnvelope(t *testing.T, sessionID, reason string) []byte {
	t.Helper()
	payload, err := json.Marshal(Envelope{
		EventType: EventSessionDeclined,
		SessionID: sessionID,
		TicketID:  testTicketID,
		Reason:    reason,
	})
	if err != nil {
		t.Fatalf("marshal do SessionDeclined falhou: %v", err)
	}
	return payload
}

// Um convite em "waiting" que o servidor recusa sai do "waiting" na hora.
func TestHandleEnvelope_SessionDeclinedClearsWaiting(t *testing.T) {
	svc := newTestService()
	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID, TicketTitle: "Notebook lento", SupportName: "Ana"})
	if state := svc.State(); state.Status != StatusWaiting {
		t.Fatalf("pré-condição: convite deveria estar waiting; obtido %+v", state)
	}

	if err := svc.HandleEnvelope(declinedEnvelope(t, testSessionID, "user_denied")); err != nil {
		t.Fatalf("erro ao aplicar SessionDeclined: %v", err)
	}

	state := svc.State()
	if state.Status == StatusWaiting {
		t.Fatalf("convite recusado no servidor não pode continuar waiting: %+v", state)
	}
	if state.Status != StatusEnded {
		t.Fatalf("estado esperado ended, obtido %q", state.Status)
	}
	if state.HasSession {
		t.Fatalf("sessão recusada não pode continuar com hasSession=true: %+v", state)
	}
	if state.SessionID != testSessionID {
		t.Fatalf("a sessão do evento precisa ser preservada (para a UI mostrar o motivo): %q", state.SessionID)
	}
}

// Replay (at-least-once) do MESMO SessionDeclined não pode gerar transição
// nova nem limpar de volta um estado que já caminhou para active.
func TestHandleEnvelope_SessionDeclinedReplayDoesNotRegress(t *testing.T) {
	svc := newTestService()

	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})
	_ = svc.HandleEnvelope(declinedEnvelope(t, testSessionID, "user_denied"))

	// A conversa foi retomada com o MESMO id (novo convite aceito): um replay
	// atrasado do decline não pode rebaixar a sessão ativa.
	opened, _ := json.Marshal(Envelope{EventType: EventSessionOpened, SessionID: testSessionID, TicketID: testTicketID})
	_ = svc.HandleEnvelope(opened)
	if state := svc.State(); state.Status != StatusActive {
		t.Fatalf("pré-condição: sessão deveria estar active; obtido %+v", state)
	}

	_ = svc.HandleEnvelope(declinedEnvelope(t, testSessionID, "user_denied"))
	if state := svc.State(); state.Status != StatusActive || !state.HasSession {
		t.Fatalf("replay de decline não pode derrubar a conversa ativa: %+v", state)
	}
}

// SessionDeclined de OUTRA sessão (conversa anterior / outra máquina) é
// ignorado: não pode limpar o convite ou a conversa corrente.
func TestHandleEnvelope_SessionDeclinedOfOtherSessionIsIgnored(t *testing.T) {
	svc := newTestService()
	const current = "sessao-corrente"
	svc.PrepareInvite(Invite{SessionID: current, TicketID: testTicketID})

	if err := svc.HandleEnvelope(declinedEnvelope(t, testSessionID, "timeout")); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	state := svc.State()
	if state.Status != StatusWaiting || state.SessionID != current {
		t.Fatalf("decline de outra sessão limparia o convite corrente: %+v", state)
	}
}

// SessionEnded com motivo de expiração é terminal (o convite não fica waiting)
// e o motivo chega ao log; o estado final é o mesmo de qualquer encerramento.
func TestHandleEnvelope_SessionEndedExpiredLeavesWaiting(t *testing.T) {
	svc := newTestService()
	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})

	ended, _ := json.Marshal(Envelope{EventType: EventSessionEnded, SessionID: testSessionID, EndReason: "expired"})
	if err := svc.HandleEnvelope(ended); err != nil {
		t.Fatalf("erro ao aplicar SessionEnded: %v", err)
	}

	state := svc.State()
	if state.Status != StatusEnded || state.HasSession {
		t.Fatalf("sessão expirada precisa sair de waiting para ended: %+v", state)
	}
}

// Um EventType desconhecido continua sendo ACKado (o consumer durável não pode
// travar por um evento novo do servidor).
func TestHandleEnvelope_UnknownEventIsAcked(t *testing.T) {
	svc := newTestService()
	payload, _ := json.Marshal(Envelope{EventType: "AlgoNovo", SessionID: testSessionID})
	if err := svc.HandleEnvelope(payload); err != nil {
		t.Fatalf("evento desconhecido não pode falhar (o consumer reentregaria para sempre): %v", err)
	}
	if state := svc.State(); state.Status == StatusWaiting {
		t.Fatalf("evento desconhecido não pode mexer no estado: %+v", state)
	}
}
