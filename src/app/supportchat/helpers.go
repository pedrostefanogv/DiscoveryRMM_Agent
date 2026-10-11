package supportchat

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"discovery/app/netutil"
)

// newID gera um UUID v4 em texto (chave de idempotência dos envelopes).
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand indisponível: fallback monotônico evita colisão óbvia.
		return fmt.Sprintf("fallback-%d", nowUnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// setAgentAuthHeaders aplica o esquema de autenticação do agent (Bearer + X-Agent-ID).
func setAgentAuthHeaders(req *http.Request, cfg DebugConfig) error {
	return netutil.SetAgentAuthHeadersWithAgentID(req, strings.TrimSpace(cfg.AuthToken), strings.TrimSpace(cfg.AgentID))
}

// normalizeStatus converte o status canônico do servidor (PascalCase) para o
// vocabulário da janela do agent.
func normalizeStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pending":
		return StatusWaiting
	case "active":
		return StatusActive
	case "ended", "declined", "expired":
		return StatusEnded
	default:
		return StatusNone
	}
}

// sortMessages mantém a ordem canônica por sequência (estável para mensagens
// ainda sem sequência, como a bolha otimista do usuário).
func sortMessages(messages []Message) {
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].SequenceNumber == 0 || messages[j].SequenceNumber == 0 {
			return false
		}
		return messages[i].SequenceNumber < messages[j].SequenceNumber
	})
}
