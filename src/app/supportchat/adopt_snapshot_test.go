package supportchat

import (
	"context"
	"testing"
)

// ── Adoção do estado do SERVIÇO pela UI (companion) ─────────────────────────
//
// BUG RELATADO: "mando abrir o chat e a notificação não vem", com o agent no
// tray. Causa: a conversa era encerrada pelo servidor e só o processo de
// SERVIÇO recebia o SessionEnded; a instância de chat da UI continuava em
// "active" e recusava o convite seguinte com "já existe um chat ativo".

func newAdoptService() *Service {
	return New(Deps{
		Ctx:  func() context.Context { return context.Background() },
		Logf: func(string) {},
		GetDebugConfig: func() DebugConfig {
			return DebugConfig{AgentID: "agente"}
		},
		Emit: func(string, ...any) {},
	})
}

// O estado "active" precisa dar lugar ao "ended" que o serviço já conhecia.
func TestAdoptSnapshot_ActiveViraEnded(t *testing.T) {
	svc := newAdoptService()
	svc.AdoptSnapshot(State{Status: StatusActive, SessionID: "s1"})
	if got := svc.State().Status; got != StatusActive {
		t.Fatalf("pré-condição: status = %q; esperado active", got)
	}

	svc.AdoptSnapshot(State{Status: StatusEnded, SessionID: "s1"})
	if got := svc.State().Status; got != StatusEnded {
		t.Fatalf("a UI precisa adotar o encerramento vindo do serviço; status = %q", got)
	}
}

// Depois de encerrada, um convite NOVO não pode ser recusado por "busy".
func TestAdoptSnapshot_ConviteAceitoDepoisDeEncerrada(t *testing.T) {
	svc := newAdoptService()
	svc.AdoptSnapshot(State{Status: StatusEnded, SessionID: "s1"})

	decision := svc.PrepareInviteIfIdle(Invite{SessionID: "s2"})
	if !decision.Accepted {
		t.Fatalf("convite novo precisa ser aceito após a conversa encerrada; reason=%q", decision.Reason)
	}
}

// A adoção NÃO pode apagar o transcript da mesma sessão (o usuário está lendo).
func TestAdoptSnapshot_PreservaTranscriptDaMesmaSessao(t *testing.T) {
	svc := newAdoptService()
	svc.mu.Lock()
	svc.state.Status = StatusActive
	svc.state.SessionID = "s1"
	svc.state.HasSession = true
	svc.state.Messages = []Message{{ID: "m1", Body: "oi"}}
	svc.mu.Unlock()

	svc.AdoptSnapshot(State{Status: StatusEnded, SessionID: "s1"})

	if got := len(svc.State().Messages); got != 1 {
		t.Fatalf("o transcript da MESMA sessão precisa permanecer; mensagens = %d", got)
	}
}

// Sessão diferente zera o transcript (as sequências recomeçam por sessão).
func TestAdoptSnapshot_SessaoDiferenteZeraTranscript(t *testing.T) {
	svc := newAdoptService()
	svc.mu.Lock()
	svc.state.Status = StatusActive
	svc.state.SessionID = "s1"
	svc.state.HasSession = true
	svc.state.Messages = []Message{{ID: "m1", Body: "oi"}}
	svc.mu.Unlock()

	svc.AdoptSnapshot(State{Status: StatusWaiting, SessionID: "s2"})

	if got := len(svc.State().Messages); got != 0 {
		t.Fatalf("sessão diferente precisa descartar o transcript anterior; mensagens = %d", got)
	}
}

// Snapshot sem status é ignorado (payload IPC incompleto não pode zerar nada).
func TestAdoptSnapshot_SemStatusIgnorado(t *testing.T) {
	svc := newAdoptService()
	svc.AdoptSnapshot(State{Status: StatusActive, SessionID: "s1"})
	svc.AdoptSnapshot(State{SessionID: "s2"})
	if got := svc.State().SessionID; got != "s1" {
		t.Fatalf("snapshot sem status não pode alterar o estado; sessão = %q", got)
	}
}

// Evento ATRASADO não pode desfazer o encerramento.
//
// A UI reconecta o IPC com frequência e o serviço reemite o estado. Um "active"
// que saiu antes do "ended" e chegou depois regredia a conversa e o falso
// "máquina ocupada" voltava — o defeito que a adoção existe para corrigir.
func TestAdoptSnapshot_EventoAtrasadoNaoReabreSessaoEncerrada(t *testing.T) {
	svc := newAdoptService()
	svc.AdoptSnapshot(State{Status: StatusActive, SessionID: "s1"})
	svc.AdoptSnapshot(State{Status: StatusEnded, SessionID: "s1"})

	// Evento antigo chegando fora de ordem.
	svc.AdoptSnapshot(State{Status: StatusActive, SessionID: "s1"})

	if got := svc.State().Status; got != StatusEnded {
		t.Fatalf("estado atrasado não pode reabrir a sessão; status = %q", got)
	}
}

// O mesmo vale para um "waiting" atrasado da sessão encerrada: o servidor só
// reenvia convites de sessões Pending, então isso nunca é legítimo.
func TestAdoptSnapshot_WaitingAtrasadoDaMesmaSessaoIgnorado(t *testing.T) {
	svc := newAdoptService()
	svc.AdoptSnapshot(State{Status: StatusEnded, SessionID: "s1"})
	svc.AdoptSnapshot(State{Status: StatusWaiting, SessionID: "s1"})

	if got := svc.State().Status; got != StatusEnded {
		t.Fatalf("waiting atrasado da sessão encerrada precisa ser ignorado; status = %q", got)
	}
}

// Sessão DIFERENTE continua sendo adotada normalmente depois de uma encerrada.
func TestAdoptSnapshot_SessaoDiferenteAdotadaDepoisDeEncerrada(t *testing.T) {
	svc := newAdoptService()
	svc.AdoptSnapshot(State{Status: StatusEnded, SessionID: "s1"})
	svc.AdoptSnapshot(State{Status: StatusWaiting, SessionID: "s2"})

	if got := svc.State().Status; got != StatusWaiting {
		t.Fatalf("convite de outra sessão precisa ser adotado; status = %q", got)
	}
}
