package ai

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Guardrails de entrega de A2UI ao frontend.
//
// O caminho de streaming (chat_multi_round) já tinha teto por turno e descarte
// de payloads duplicados; o caminho síncrono (SendWithA2ui) e o fallback não —
// um modelo exagerado podia inundar a UI de cards e o mesmo card podia ser
// renderizado duas vezes. Este guard centraliza as três regras (normalizar,
// deduplicar e limitar) para TODOS os caminhos de entrega.

// maxA2uiMessagesPerTurn é o teto de mensagens de interface por turno.
const maxA2uiMessagesPerTurn = 6

type a2uiDropReason int

const (
	a2uiAccept a2uiDropReason = iota
	a2uiDropEmpty
	a2uiDropDuplicate
	a2uiDropLimit
)

// a2uiClientGuard acumula o estado de um turno (mensagens já vistas e quantas
// foram entregues). Não é seguro para uso concorrente — cada turno cria o seu.
type a2uiClientGuard struct {
	seen  map[string]bool
	count int
	max   int
}

// newA2uiClientGuard cria o guard do turno. max <= 0 usa o teto padrão.
func newA2uiClientGuard(max int) *a2uiClientGuard {
	if max <= 0 {
		max = maxA2uiMessagesPerTurn
	}
	return &a2uiClientGuard{seen: make(map[string]bool, 4), max: max}
}

// Offer normaliza a mensagem e decide se ela deve ser entregue ao frontend.
// Devolve a mensagem normalizada e o motivo; o chamador só repassa quando o
// motivo é a2uiAccept.
//
// O dedup usa a forma CANÔNICA (chaves ordenadas) do payload JÁ normalizado:
// assim o mesmo card reenviado com chaves em outra ordem ou com formatação
// diferente conta como duplicado, independentemente de a primeira cópia ter
// precisado de normalização.
func (g *a2uiClientGuard) Offer(raw string) (string, a2uiDropReason) {
	m := normalizeA2uiForClient(raw)
	if m == "" {
		return "", a2uiDropEmpty
	}
	key := a2uiCanonicalKey(m)
	if g.seen[key] {
		return m, a2uiDropDuplicate
	}
	if g.count >= g.max {
		return m, a2uiDropLimit
	}
	g.seen[key] = true
	g.count++
	return m, a2uiAccept
}

// a2uiCanonicalKey devolve uma representação estável do JSON (chaves de objeto
// ordenadas) para comparação. Mensagens não parseáveis caem no próprio texto.
func a2uiCanonicalKey(msg string) string {
	var parsed any
	if err := json.Unmarshal([]byte(msg), &parsed); err != nil {
		return msg
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(parsed); err != nil {
		return msg
	}
	return strings.TrimRight(buf.String(), "\n")
}
