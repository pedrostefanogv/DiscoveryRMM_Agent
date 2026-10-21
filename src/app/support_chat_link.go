package app

import (
	"sync"
	"time"

	"discovery/app/supportchat"
)

// ── Estado REAL do canal do chat (serviço <-> servidor) ─────────────────────
//
// BUG RELATADO ("abri o chat e apareceu 'Sem conexão em tempo real'"): a janela
// derivava "conectado" do campo Connected do Estado do PRÓPRIO processo de UI.
// No modo companion o NATS é do SERVIÇO: a UI nunca assina o canal e o campo
// dela é sempre false — qualquer evento de estado sem sessão (corrida do
// aceite x /active, evento atrasado) virava o aviso de reconexão numa conversa
// que estava aberta.
//
// Aqui o link é publicado pelo processo que REALMENTE fala com o servidor:
//   - serviço: natsReady de OnNATSConnected/OnNATSDisconnected;
//   - UI companion: o que o serviço publicou (snapshot de status do IPC e o
//     evento supportchat:link).
//
// O valor só é enviado à janela depois de supportChatLinkGrace: um flap curto
// do NATS não pinta o aviso (em rede degradada o serviço reconecta em segundos).
const supportChatLinkGrace = 3 * time.Second

// supportChatLink é o estado de link do chat, com o instante da transição.
type supportChatLink struct {
	active  bool
	since   time.Time
	changed time.Time
}

// supportChatLinkState guarda o link corrente do processo.
type supportChatLinkState struct {
	mu    sync.Mutex
	known bool
	link  supportChatLink
}

// set registra o link e devolve true quando HOUVE mudança de valor.
// Idempotente: o mesmo valor não reinicia o instante da transição.
func (s *supportChatLinkState) set(active bool) bool {
	if s == nil {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.known {
		if s.link.active == active {
			return false
		}
		s.link = supportChatLink{active: active, since: now, changed: now}
		return true
	}
	// Primeiro valor informado: não conta como transição (sem carência).
	s.known = true
	s.link = supportChatLink{active: active, since: now}
	return true
}

// snapshot devolve o link corrente e se ele já passou da carência (settled).
func (s *supportChatLinkState) snapshot(now time.Time) (supportChatLink, bool) {
	if s == nil {
		return supportChatLink{active: true, since: now}, true
	}
	s.mu.Lock()
	known, link := s.known, s.link
	s.mu.Unlock()
	if !known {
		// Nunca informado: assume conectado — o snapshot REST decide a verdade.
		return supportChatLink{active: true, since: now}, true
	}
	if link.changed.IsZero() {
		// Valor inicial informado: link estável desde o primeiro aviso.
		return link, true
	}
	return link, now.Sub(link.changed) >= supportChatLinkGrace
}

// supportChatLinkPayload monta o campo "realtime" do evento supportchat:state
// enviado à JANELA do chat. active=false é o único sinal que o frontend usa para
// deixar de considerar o canal vivo.
func (a *App) supportChatLinkPayload() map[string]any {
	now := time.Now()
	link, settled := a.supportChatLink.snapshot(now)
	active := link.active || !settled
	payload := map[string]any{
		"active":       active,
		"settled":      settled,
		"sinceUtc":     link.since.UTC().Format(time.RFC3339),
		"downSinceUtc": "",
	}
	if !link.active {
		payload["downSinceUtc"] = link.since.UTC().Format(time.RFC3339)
	}
	return payload
}

// setSupportChatLink atualiza o link e, quando MUDA, avisa a janela/UI.
//
// Chamado em OnNATSConnected/OnNATSDisconnected (serviço) e quando a UI adota o
// estado publicado pelo serviço (snapshot de status do IPC).
func (a *App) setSupportChatLink(active bool) {
	if a == nil || !a.supportChatLink.set(active) {
		return
	}
	a.Logs.Append("[supportchat] realtime do servico: " + supportChatLinkLabel(active))
	a.EmitEvent("supportchat:link", a.supportChatLinkPayload())
}

func supportChatLinkLabel(active bool) string {
	if active {
		return "ativo"
	}
	return "inativo (mensagens vão pelo REST até reconectar)"
}

// supportChatLinkCurrent devolve o último link conhecido por este processo
// (publicado pelo serviço quando em modo companion).
func (a *App) supportChatLinkCurrent() bool {
	if a == nil {
		return false
	}
	link, _ := a.supportChatLink.snapshot(time.Now())
	return link.active
}

// supportChatStateEventPayload monta o argumento de supportchat:state para a
// janela: um ÚNICO mapa com os campos do Estado + o link real do canal.
//
// BUG CORRIGIDO: a versão anterior emitia DOIS argumentos (Estado + extras). No
// Wails v3, mais de um argumento faz Event.Data ser o SLICE e o frontend recebe
// um array; no modo companion o buildIPCEventPayload também só entende pares
// chave/valor (string, valor) — um supportchat.State não é string, então o
// payload IPC saía SEM os campos. Resultado nos dois caminhos: applyState(Array)
// lia status undefined -> "none" e a UI caía para "sem conversa" a cada evento,
// piscando a statusbar e DESABILITANDO o campo de digitação no meio da conversa.
//
// Um mapa único preserva o contrato do frontend (data.status, data.messages,
// data.realtime) e passa intacto pelo IPC (que espalha os campos do mapa).
func (a *App) supportChatStateEventPayload(state supportchat.State) []any {
	payload := map[string]any{
		"hasSession":  state.HasSession,
		"sessionId":   state.SessionID,
		"ticketId":    state.TicketID,
		"ticketTitle": state.TicketTitle,
		"supportName": state.SupportName,
		"status":      state.Status,
		"expiresAt":   state.ExpiresAt,
		"connected":   state.Connected,
		"lastError":   state.LastError,
		"messages":    state.Messages,
		"realtime":    a.supportChatLinkPayload(),
	}
	return []any{payload}
}

// supportChatLinkFromIPC extrai o link publicado pelo serviço em um payload de
// IPC (campo supportChatLinkActive ou realtime.active).
func supportChatLinkFromIPC(payload map[string]any) (active bool, known bool) {
	if payload == nil {
		return false, false
	}
	source := payload
	if raw, ok := payload[ipcEventRawKey].(map[string]any); ok {
		source = raw
	}
	if value, ok := source["supportChatLinkActive"].(bool); ok {
		return value, true
	}
	if realtime, ok := source["realtime"].(map[string]any); ok {
		if value, ok := realtime["active"].(bool); ok {
			return value, true
		}
	}
	return false, false
}
