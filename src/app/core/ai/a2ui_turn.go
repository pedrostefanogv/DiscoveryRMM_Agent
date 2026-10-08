package ai

import (
	"fmt"
	"strings"
)

// A2uiActionSentinel é a mensagem interna que o frontend envia ao iniciar um
// turno disparado por clique/input em uma surface A2UI. NUNCA deve chegar ao
// LLM nem entrar no histórico: o conteúdo real do clique vai como tool result
// (a2ui_action).
const A2uiActionSentinel = "__a2ui_action__"

// resolveA2uiTurn decide, no INÍCIO de um turno, como a mensagem entrante se
// relaciona com a fila de ações A2UI. Devolve a ação a injetar (nil quando não
// há), se o turno é um turno-sentinela de A2UI e um erro quando o turno não
// pode prosseguir.
//
// Contrato (corrige dois bugs do ciclo de vida A2UI):
//
//  1. Sentinela COM ação pendente → consome a ação como tool result.
//  2. Sentinela SEM ação pendente → erro claro, em vez de mandar a string
//     "__a2ui_action__" como se fosse mensagem do usuário. Isso acontecia quando
//     o clique era perdido (agente descartava a ação antes do turno-sentinela).
//  3. Mensagem digitada → descarta ações órfãs remanescentes. Um clique cujo
//     turno não chegou a iniciar (frontend offline, erro de dispatch) não pode
//     sequestrar a próxima mensagem digitada nem ser enviado ao LLM.
func (s *Service) resolveA2uiTurn(userMessage string) (action *A2uiAction, isA2uiTurn bool, err error) {
	isA2uiTurn = strings.TrimSpace(userMessage) == A2uiActionSentinel

	if isA2uiTurn {
		action = s.takeA2uiAction()
		if action == nil {
			return nil, true, fmt.Errorf("acao A2UI expirada: nenhum clique pendente — clique novamente no botao da interface")
		}
		return action, true, nil
	}

	if n := s.discardPendingA2uiActions(); n > 0 {
		s.logf("[chat] %d ação(ões) A2UI órfã(s) descartada(s) ao iniciar turno de mensagem digitada", n)
	}
	return nil, false, nil
}
