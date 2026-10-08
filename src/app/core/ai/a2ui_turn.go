package ai

import (
	"encoding/json"
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

// ─── Cache da última definição A2UI por surface ───
//
// Por que existe: a resposta do assistente é persistida no servidor SEM o bloco
// a2ui (AiChatOutputPipeline extrai e remove antes de gravar), então no turno
// seguinte o histórico do LLM não tem a árvore de componentes da surface. Num
// clique de NAVEGAÇÃO (step_next/voltar/aba) o modelo precisa reemitir a MESMA
// surface com o novo estado, mas sem a definição anterior ele só conseguia
// responder em prosa ("Funcionou! ... veja ao vivo") e a interface ficava presa
// no passo anterior (caso do stepper, 2026-10-08). O snapshot vai junto do
// tool result sintético a2ui_action.
const maxA2uiSurfaceCache = 8

type a2uiSurfaceRef struct {
	SurfaceID string `json:"surfaceId"`
}

type a2uiEnvelope struct {
	CreateSurface    *a2uiSurfaceRef `json:"createSurface"`
	UpdateComponents *a2uiSurfaceRef `json:"updateComponents"`
	UpdateDataModel  *a2uiSurfaceRef `json:"updateDataModel"`
	DeleteSurface    *a2uiSurfaceRef `json:"deleteSurface"`
}

// rememberA2uiSurface guarda a última mensagem updateComponents de cada surface
// (a única que contém a árvore completa) e esquece surfaces deletadas.
func (s *Service) rememberA2uiSurface(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	var env a2uiEnvelope
	if err := json.Unmarshal([]byte(msg), &env); err != nil {
		return
	}

	s.a2uiSurfaceMu.Lock()
	defer s.a2uiSurfaceMu.Unlock()

	if env.DeleteSurface != nil {
		s.dropA2uiSurfaceLocked(env.DeleteSurface.SurfaceID)
		return
	}

	var surfaceID string
	isDefinition := false
	switch {
	case env.UpdateComponents != nil:
		surfaceID = env.UpdateComponents.SurfaceID
		isDefinition = true
	case env.UpdateDataModel != nil:
		surfaceID = env.UpdateDataModel.SurfaceID
	case env.CreateSurface != nil:
		surfaceID = env.CreateSurface.SurfaceID
	default:
		return
	}
	if surfaceID == "" {
		return
	}
	if s.a2uiSurface == nil {
		s.a2uiSurface = make(map[string]string, maxA2uiSurfaceCache)
	}
	if _, exists := s.a2uiSurface[surfaceID]; !exists {
		s.a2uiSurfaceOrder = append(s.a2uiSurfaceOrder, surfaceID)
	}
	// createSurface/updateDataModel não trazem a árvore: não sobrescrevem a
	// última definição conhecida da surface.
	if isDefinition || s.a2uiSurface[surfaceID] == "" {
		s.a2uiSurface[surfaceID] = msg
	}
	for len(s.a2uiSurfaceOrder) > maxA2uiSurfaceCache {
		oldest := s.a2uiSurfaceOrder[0]
		s.a2uiSurfaceOrder = s.a2uiSurfaceOrder[1:]
		delete(s.a2uiSurface, oldest)
	}
}

// dropA2uiSurfaceLocked remove uma surface do cache. Exige a2uiSurfaceMu preso.
func (s *Service) dropA2uiSurfaceLocked(surfaceID string) {
	if surfaceID == "" {
		return
	}
	delete(s.a2uiSurface, surfaceID)
	for i, id := range s.a2uiSurfaceOrder {
		if id == surfaceID {
			s.a2uiSurfaceOrder = append(s.a2uiSurfaceOrder[:i], s.a2uiSurfaceOrder[i+1:]...)
			break
		}
	}
}

// a2uiSurfaceSnapshot devolve a última definição conhecida da surface ("" se
// nunca foi vista por este processo).
func (s *Service) a2uiSurfaceSnapshot(surfaceID string) string {
	if surfaceID == "" {
		return ""
	}
	s.a2uiSurfaceMu.Lock()
	defer s.a2uiSurfaceMu.Unlock()
	return s.a2uiSurface[surfaceID]
}

// a2uiActionInstruction é o contrato entregue ao LLM junto do clique. Cobre os
// DOIS tipos de ação: tool real (executa a MCP homônima) e ação de estado da
// própria interface (reemite a superfície). Sem a segunda parte, "Avançar"
// virava uma resposta em prosa e o card não mudava de passo.
const a2uiActionInstruction = "Acao clicada numa interface A2UI. " +
	"1) Se existir uma tool MCP com este name, execute-a com o context informado — nao peca confirmacao. " +
	"2) Se for uma acao de NAVEGACAO/estado da propria interface (ex.: step_next, step_prev, next, avancar, voltar, tab_select) e nao existir tool homonima, responda com um bloco a2ui contendo updateComponents para a MESMA surfaceId, reemitindo a arvore de componentes com o novo estado (o campo surface traz a ultima definicao, use-a como base). " +
	"NUNCA diga que a interface mudou (ex.: veja ao vivo, atualizei a interface) sem emitir esse bloco — sem ele nada muda na tela."

type a2uiActionToolResult struct {
	SurfaceID   string          `json:"surfaceId"`
	Name        string          `json:"name"`
	Context     map[string]any  `json:"context"`
	Instruction string          `json:"instruction"`
	Surface     json.RawMessage `json:"surface,omitempty"`
}

// buildA2uiActionToolResult monta o payload {callId,name,result} do clique.
// surfaceSnapshot (opcional) é a última definição da surface, reenviada ao LLM
// como objeto JSON (não como string) para ele reemitir o update.
func buildA2uiActionToolResult(action *A2uiAction, surfaceSnapshot string) string {
	res := a2uiActionToolResult{Instruction: a2uiActionInstruction}
	if action != nil {
		res.SurfaceID = action.SurfaceID
		res.Name = action.Name
		res.Context = action.Context
	}
	if res.Context == nil {
		res.Context = map[string]any{}
	}
	if snap := strings.TrimSpace(surfaceSnapshot); snap != "" && json.Valid([]byte(snap)) {
		res.Surface = json.RawMessage(snap)
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "{}"
	}
	return string(out)
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
