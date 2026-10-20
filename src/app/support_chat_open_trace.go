package app

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Medida do caminho "aceite -> janela do chat visível" ────────────────────
//
// RELATO: "às vezes, quando mando abrir o chat, demora para abrir". Sem tempo
// por ETAPA não há como saber onde o tempo vai: POST do aceite, estado
// ativo, criação da janela (WebView2), navegação ou pintura.
//
// Cada etapa vira uma linha no log do agent e a abertura fecha com um resumo:
//
//	[supportchat] abertura: sessao=... aceite=...ms post=...ms estado=...ms
//	    janela=...ms pintura=...ms total=...ms (reuso=sim|nao, webview=novo)
//
// O trace é por SESSÃO (um convite por vez na máquina) e expira sozinho: uma
// abertura que não completa não pode vazar memória num processo de bandeja.
const supportChatOpenTraceTTL = 2 * time.Minute

// supportChatOpenTraceStep é uma etapa já cumprida da abertura.
type supportChatOpenTraceStep struct {
	name string
	at   time.Time
}

// supportChatOpenTrace acompanha uma abertura em andamento.
type supportChatOpenTrace struct {
	sessionID string
	start     time.Time
	startedAt time.Time

	mu       sync.Mutex
	steps    []supportChatOpenTraceStep
	reuse    bool
	webview  string
	finished bool
}

// start monta o trace de uma abertura (t0 = clique no cartão / pedido de abrir).
func newSupportChatOpenTrace(sessionID string, now time.Time) *supportChatOpenTrace {
	return &supportChatOpenTrace{
		sessionID: strings.TrimSpace(sessionID),
		start:     now,
		startedAt: now,
	}
}

// mark registra uma etapa (a primeira ocorrência de cada nome vence).
func (t *supportChatOpenTrace) mark(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, step := range t.steps {
		if step.name == name {
			return
		}
	}
	t.steps = append(t.steps, supportChatOpenTraceStep{name: name, at: time.Now()})
}

// offset devolve o tempo até a etapa (ms); -1 se a etapa não aconteceu.
func (t *supportChatOpenTrace) offset(name string) int64 {
	if t == nil {
		return -1
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, step := range t.steps {
		if step.name == name {
			return step.at.Sub(t.start).Milliseconds()
		}
	}
	return -1
}

// describe formata uma etapa "nome=<ms>".
func (t *supportChatOpenTrace) describe(name string) string {
	value := t.offset(name)
	if value < 0 {
		return name + "=-"
	}
	return name + "=" + strconv.FormatInt(value, 10) + "ms"
}

// finish devolve e remove o trace, com a linha final de log já montada.
func (t *supportChatOpenTrace) finish() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	reuse, webview := t.reuse, t.webview
	t.steps = append(t.steps, supportChatOpenTraceStep{name: "total", at: time.Now()})
	t.mu.Unlock()

	line := "[supportchat] abertura: sessao=" + t.sessionID +
		" " + t.describe("post") +
		" " + t.describe("estado") +
		" " + t.describe("janela") +
		" " + t.describe("conteudo") +
		" " + t.describe("pintura") +
		" " + t.describe("total") +
		" reuso=" + boolLabel(reuse)
	if webview != "" {
		line += " webview=" + webview
	}
	return line
}

// setReuse marca que a abertura reaproveitou a janela do convite.
func (t *supportChatOpenTrace) setReuse(reuse bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.reuse = reuse
	t.mu.Unlock()
}

// setWebview registra "novo" ou "reaproveitado" para a linha final.
func (t *supportChatOpenTrace) setWebview(kind string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.webview = kind
	t.mu.Unlock()
}

// finishSupportChatOpenTrace fecha e loga o resumo do trace corrente.
func (a *App) finishSupportChatOpenTrace() {
	if a == nil {
		return
	}
	a.supportChatTraceMu.Lock()
	trace := a.supportChatOpenTrace
	a.supportChatOpenTrace = nil
	a.supportChatTraceMu.Unlock()
	if line := trace.finish(); line != "" {
		a.Logs.Append(line)
	}
}

// supportChatTraceFor devolve o trace da sessão (criando quando não existe).
func (a *App) supportChatTraceFor(sessionID string) *supportChatOpenTrace {
	if a == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	now := time.Now()

	a.supportChatTraceMu.Lock()
	defer a.supportChatTraceMu.Unlock()
	if a.supportChatOpenTrace != nil &&
		!a.supportChatOpenTrace.startedAt.IsZero() &&
		now.Sub(a.supportChatOpenTrace.startedAt) > supportChatOpenTraceTTL {
		a.supportChatOpenTrace = nil
	}
	if a.supportChatOpenTrace == nil || (sessionID != "" && a.supportChatOpenTrace.sessionID != sessionID) {
		a.supportChatOpenTrace = newSupportChatOpenTrace(sessionID, now)
	}
	return a.supportChatOpenTrace
}

// markSupportChatOpenStep registra uma etapa da abertura no trace CORRENTE.
//
// BUG CORRIGIDO: esta função usava supportChatTraceFor, que CRIA o trace quando
// não existe. Depois de uma abertura concluída (finishSupportChatOpenTrace), o
// próximo sinal de janela (SupportChatWindowReady de um reload, a etapa
// "estado" do caminho fallback) ressuscitava um trace novo que nunca terminava —
// e a próxima pintura logava uma linha "abertura:" falsa, com tempos zerados.
// Sem trace em andamento não há o que medir: não cria.
func (a *App) markSupportChatOpenStep(sessionID, step string) {
	trace := a.currentSupportChatOpenTrace()
	if trace == nil {
		return
	}
	// Só conta etapas da sessão que iniciou a abertura (outra conversa pode ter
	// assumido no meio do caminho).
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" && trace.sessionID != "" && trace.sessionID != sessionID {
		return
	}
	trace.mark(step)
}

// currentSupportChatOpenTrace devolve o trace corrente sem criar.
func (a *App) currentSupportChatOpenTrace() *supportChatOpenTrace {
	if a == nil {
		return nil
	}
	a.supportChatTraceMu.Lock()
	defer a.supportChatTraceMu.Unlock()
	return a.supportChatOpenTrace
}

// currentSupportChatSessionID é o sessionID do convite/conversa corrente (para
// associar as etapas da abertura).
func (a *App) currentSupportChatSessionID() string {
	if a == nil {
		return ""
	}
	state := a.supportChatService().State()
	return strings.TrimSpace(state.SessionID)
}

