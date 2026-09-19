package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// ChatQuestion represents an interactive question sent to the user.
type ChatQuestion struct {
	ID        string   `json:"id"`
	Question  string   `json:"question"`
	Options   []string `json:"options,omitempty"`
	AllowText bool     `json:"allowText"`
}

// ChatQuestionAnswer is the user's response to a ChatQuestion.
type ChatQuestionAnswer struct {
	QuestionID string `json:"questionId"`
	Answer     string `json:"answer"`
}

var (
	pendingQuestionsMu sync.Mutex
	pendingQuestions   = make(map[string]chan ChatQuestionAnswer)
)

// registerQuestionChannel cria e registra o canal de resposta de uma pergunta.
func registerQuestionChannel(id string) chan ChatQuestionAnswer {
	pendingQuestionsMu.Lock()
	defer pendingQuestionsMu.Unlock()
	ch := make(chan ChatQuestionAnswer, 1)
	pendingQuestions[id] = ch
	return ch
}

// takeQuestionChannel remove e retorna o canal da pergunta (nil se inexistente).
func takeQuestionChannel(id string) chan ChatQuestionAnswer {
	pendingQuestionsMu.Lock()
	defer pendingQuestionsMu.Unlock()
	ch, ok := pendingQuestions[id]
	if ok {
		delete(pendingQuestions, id)
	}
	return ch
}

// AskUser displays a question to the user and waits for their answer.
// This blocks the calling goroutine until the user responds or timeout is reached.
func (a *App) AskUser(question, optionsJSON, allowTextRaw string) (string, error) {
	return a.AskUserContext(nil, question, optionsJSON, allowTextRaw)
}

// AskUserContext exibe uma pergunta interativa ao usuário e aguarda a resposta
// SEM limite de tempo — o chat prossegue quando o usuário responder.
//
// O ctx (quando informado) permite interromper a espera pelo cancelamento do
// stream (botão Parar) ou pelo encerramento do app: nesse caso a pergunta
// pendente é removida e a UI é notificada via evento "chat:question_cancelled"
// para desabilitar a bolha (evita resposta órfã em pergunta já morta).
func (a *App) AskUserContext(ctx context.Context, question, optionsJSON, allowTextRaw string) (string, error) {
	if ctx == nil {
		ctx = a.ctx
	}

	// Parse options
	var options []string
	if strings.TrimSpace(optionsJSON) != "" {
		var raw []any
		if err := json.Unmarshal([]byte(optionsJSON), &raw); err != nil {
			// Try as comma-separated fallback
			for _, s := range strings.Split(optionsJSON, ",") {
				if s = strings.TrimSpace(s); s != "" {
					options = append(options, s)
				}
			}
		} else {
			for _, o := range raw {
				if s, ok := o.(string); ok {
					if s = strings.TrimSpace(s); s != "" {
						options = append(options, s)
					}
				}
			}
		}
	}
	if len(options) > 6 {
		options = options[:6]
	}

	allowText := false
	switch strings.ToLower(strings.TrimSpace(allowTextRaw)) {
	case "true", "1", "yes", "sim", "on":
		allowText = true
	}

	id := fmt.Sprintf("q-%d", time.Now().UnixNano())

	q := &ChatQuestion{
		ID:        id,
		Question:  question,
		Options:   options,
		AllowText: allowText || len(options) == 0,
	}

	answerCh := registerQuestionChannel(id)

	// Emit event to the frontend
	qJSON, _ := json.Marshal(q)
	a.EmitEvent("chat:question", string(qJSON))
	// Também publica via broker SSE, para que o evento chegue tanto ao
	// navegador (debug HTTP) quanto ao webview nativo quando este consome os
	// eventos de chat pelo endpoint SSE (ver chat-native-event-loss.md).
	a.PublishChatEvent("chat:question", string(qJSON))

	// Sem timeout: aguarda o usuário responder indefinidamente. O ctx (stream
	// do chat ou ciclo de vida do app) é a única forma de interromper.
	select {
	case answer := <-answerCh:
		takeQuestionChannel(id) // limpeza defensiva
		return answer.Answer, nil
	case <-ctx.Done():
		takeQuestionChannel(id)
		// Notifica a UI: a bolha da pergunta deve ser desabilitada para o
		// usuário não responder a uma pergunta que já não está mais ativa.
		if cancelJSON, err := json.Marshal(map[string]string{"id": id, "reason": "cancelled"}); err == nil {
			a.EmitEvent("chat:question_cancelled", string(cancelJSON))
			a.PublishChatEvent("chat:question_cancelled", string(cancelJSON))
		}
		if ctx.Err() == context.Canceled {
			return "", fmt.Errorf("pergunta cancelada: o processamento do chat foi interrompido")
		}
		return "", fmt.Errorf("aguardando resposta do usuário: %w", ctx.Err())
	}
}

// AnswerChatQuestion is the Wails binding called by the frontend when the user clicks an option.
func (a *App) AnswerChatQuestion(questionID, answer string) {
	ch := takeQuestionChannel(questionID)
	if ch == nil {
		// Pergunta desconhecida, cancelada ou já respondida — ignora sem
		// afetar outras perguntas, mas registra para diagnóstico.
		log.Printf("[chat] AnswerChatQuestion: pergunta %s não encontrada (cancelada ou já respondida)", questionID)
		return
	}
	select {
	case ch <- ChatQuestionAnswer{QuestionID: questionID, Answer: answer}:
	default:
	}
}

// A2uiActionPayload é a estrutura de uma ação do usuário em uma surface A2UI.
type A2uiActionPayload struct {
	SurfaceID string         `json:"surfaceId"`
	Name      string         `json:"name"`
	Context   map[string]any `json:"context"`
}

// AnswerA2uiAction é o binding Wails chamado pelo frontend quando o usuário
// interage com um componente A2UI (clique em botão, input, etc.).
//
// A ação é encaminhada ao serviço de chat, que a resolve como um tool result
// no próximo round do loop multi-round (o LLM recebe o resultado e continua).
// Se não houver stream ativo, a ação é descartada silenciosamente.
func (a *App) AnswerA2uiAction(payloadJSON string) {
	if a.chatSvc == nil {
		return
	}
	var payload A2uiActionPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		a.Logs.Append("[chat] AnswerA2uiAction: payload inválido: " + err.Error())
		return
	}
	if strings.TrimSpace(payload.Name) == "" {
		return
	}
	// B1: sem stream/turno ativo, a ação seria órfã — ficaria pendente e seria
	// consumida indevidamente pela próxima mensagem comum do usuário (convertida
	// em modo a2ui_action com a mensagem digitada descartada). Descarta com log.
	if !a.chatSvc.HasActiveStream() {
		a.Logs.Append("[chat] AnswerA2uiAction: ação descartada — nenhum turno de chat ativo (ação: " + payload.Name + ")")
		return
	}
	a.chatSvc.SubmitA2uiAction(payload.SurfaceID, payload.Name, payload.Context)
}
