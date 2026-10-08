package ai

import (
	"fmt"
	"strings"
	"time"
)

// A2uiActionSentinel é a mensagem interna que o frontend envia ao iniciar um
// turno disparado por clique/input em uma surface A2UI. NUNCA deve chegar ao
// LLM nem entrar no histórico: o conteúdo real do clique vai como tool result
// (a2ui_action).
const A2uiActionSentinel = "__a2ui_action__"

// a2uiActionCallID gera o tool_call_id do tool result sintético que transporta
// o clique da interface até o LLM.
//
// Precisa ser ÚNICO por clique: o par (surface, name) se repete quando o usuário
// clica no mesmo botão em momentos diferentes, e repetir o tool_call_id na
// mesma sessão confunde o pareamento tool_call/tool_message exigido pelos
// provedores OpenAI-compatible (o segundo clique podia ser ignorado).
// a2uiProcessTag identifica a EXECUÇÃO do agent na composição do call id. O
// contador Seq reinicia a cada start do processo, então sem esta marca dois
// cliques em execuções diferentes poderiam repetir o mesmo tool_call_id dentro
// da mesma sessão do servidor (a sessão vive no servidor, não no agent).
var a2uiProcessTag = fmt.Sprintf("%x", uint32(time.Now().UnixNano()))

func a2uiActionCallID(action *A2uiAction) string {
	if action == nil {
		return "a2ui_action"
	}
	return fmt.Sprintf("a2ui_%s_%s_%d", action.SurfaceID, a2uiProcessTag, action.Seq)
}

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
