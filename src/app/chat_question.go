package app

import (
	"encoding/json"
	"fmt"
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
	pendingQuestion   *ChatQuestion
	pendingQuestionMu sync.Mutex
	questionAnswerCh  = make(chan ChatQuestionAnswer, 1)
)

// AskUser displays a question to the user and waits for their answer.
// This blocks the calling goroutine until the user responds or timeout is reached.
func (a *App) AskUser(question, optionsJSON, allowTextRaw string) (string, error) {
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
					options = append(options, s)
				}
			}
		}
	}
	if len(options) > 6 {
		options = options[:6]
	}

	allowText := false
	switch strings.ToLower(strings.TrimSpace(allowTextRaw)) {
	case "true", "1", "yes", "sim":
		allowText = true
	}

	id := fmt.Sprintf("q-%d", time.Now().UnixNano())

	q := &ChatQuestion{
		ID:        id,
		Question:  question,
		Options:   options,
		AllowText: allowText || len(options) == 0,
	}

	// Lock to prevent concurrent questions
	pendingQuestionMu.Lock()
	pendingQuestion = q
	pendingQuestionMu.Unlock()

	// Emit event to the frontend
	qJSON, _ := json.Marshal(q)
	a.EmitEvent("chat:question", string(qJSON))
	// Também publica via broker SSE, para que o evento chegue tanto ao
	// navegador (debug HTTP) quanto ao webview nativo quando este consome os
	// eventos de chat pelo endpoint SSE (ver chat-native-event-loss.md).
	a.PublishChatEvent("chat:question", string(qJSON))

	// Wait for answer with timeout
	select {
	case answer := <-questionAnswerCh:
		if answer.QuestionID != id {
			// Answer for a different question, put it back
			select {
			case questionAnswerCh <- answer:
			default:
			}
			return "", fmt.Errorf("resposta inválida para questionID %s", id)
		}
		pendingQuestionMu.Lock()
		pendingQuestion = nil
		pendingQuestionMu.Unlock()
		return answer.Answer, nil
	case <-time.After(120 * time.Second):
		pendingQuestionMu.Lock()
		pendingQuestion = nil
		pendingQuestionMu.Unlock()
		return "", fmt.Errorf("tempo esgotado aguardando resposta do usuário")
	case <-a.ctx.Done():
		pendingQuestionMu.Lock()
		pendingQuestion = nil
		pendingQuestionMu.Unlock()
		return "", fmt.Errorf("aplicação encerrada")
	}
}

// AnswerChatQuestion is the Wails binding called by the frontend when the user clicks an option.
func (a *App) AnswerChatQuestion(questionID, answer string) {
	select {
	case questionAnswerCh <- ChatQuestionAnswer{QuestionID: questionID, Answer: answer}:
	default:
		// Channel full, discard
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
		a.logs.append("[chat] AnswerA2uiAction: payload inválido: " + err.Error())
		return
	}
	if strings.TrimSpace(payload.Name) == "" {
		return
	}
	a.chatSvc.SubmitA2uiAction(payload.SurfaceID, payload.Name, payload.Context)
}
