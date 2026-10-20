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
//
// BUG CORRIGIDO (revisão): só o vocabulário do SERVIDOR era reconhecido. O
// estado local e o que trafega por IPC já usam o vocabulário da JANELA
// (waiting/active/ended). Passar "waiting" por aqui devolvia StatusNone — foi o
// que fazia a adoção de estado via IPC zerar o convite em vez de adotá-lo (o
// convite seguia sem cartão e a janela não abria).
func normalizeStatus(raw string) string {
	// As constantes StatusWaiting/StatusActive/StatusEnded JÁ são as formas
	// minúsculas ("waiting"/"active"/"ended"), então cobrem os dois vocabulários
	// no mesmo switch.
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pending", StatusWaiting:
		return StatusWaiting
	case StatusActive:
		return StatusActive
	case "declined", "expired", StatusEnded:
		return StatusEnded
	default:
		return StatusNone
	}
}

// sortMessages mantém a ordem canônica por sequência e deixa as bolhas
// OTIMISTAS (sem sequência, ainda não confirmadas pelo servidor) no FIM.
//
// BUG LATENTE CORRIGIDO: o comparador antigo devolvia "false" sempre que um dos
// lados não tinha sequência. Para o sort.SliceStable isso NÃO é uma ordenação
// válida (viola transitividade): o resultado depende da ordem de entrada e, numa
// mistura de otimistas com canônicas, a mensagem recém-enviada podia ficar no
// meio da conversa. Particionar é determinístico e casa com o critério do
// frontend (que já joga as otimistas para o fim).
func sortMessages(messages []Message) {
	if len(messages) < 2 {
		return
	}

	canonical := make([]Message, 0, len(messages))
	optimistic := make([]Message, 0, 4)
	for _, message := range messages {
		if message.SequenceNumber > 0 {
			canonical = append(canonical, message)
			continue
		}
		optimistic = append(optimistic, message)
	}

	sort.SliceStable(canonical, func(i, j int) bool {
		return canonical[i].SequenceNumber < canonical[j].SequenceNumber
	})

	copy(messages, append(canonical, optimistic...))
}
