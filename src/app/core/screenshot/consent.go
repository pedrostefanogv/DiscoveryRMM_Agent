package screenshot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Decision é o estado de autorização da captura de tela pela IA.
type Decision string

const (
	// DecisionUndecided: nunca perguntado — a próxima captura solicitada pela
	// IA abre o diálogo de autorização.
	DecisionUndecided Decision = "undecided"
	// DecisionSession: autorizado até o app/agente reiniciar.
	DecisionSession Decision = "session"
	// DecisionAlways: autorizado de forma persistente (sobrevive a restart).
	DecisionAlways Decision = "always"
	// DecisionDenied: negado nesta sessão; a IA não pode capturar até o
	// usuário revogar/reautorizar na UI.
	DecisionDenied Decision = "denied"
)

// Prompter pergunta ao usuário e aguarda a resposta (no agent é o diálogo
// interativo do chat — App.AskUserContext).
type Prompter func(ctx context.Context, question string, options []string) (string, error)

// Persist armazena a decisão "sempre" em disco.
type Persist struct {
	Load func() (Decision, error)
	Save func(Decision) error
}

// AuditEntry registra uma captura autorizada (trilha de auditoria exibida na UI).
type AuditEntry struct {
	At       time.Time `json:"at"`
	Decision Decision  `json:"decision"`
	Mode     string    `json:"mode"`
	Detail   string    `json:"detail"`
	ByLLM    bool      `json:"byLlm"`
	Bytes    int       `json:"bytes"`
}

// ConsentOptions são as opções apresentadas no diálogo de autorização.
var ConsentOptions = []string{
	"Permitir nesta conversa",
	"Permitir sempre",
	"Negar",
}

const auditMax = 50

// ConsentManager controla se a IA pode capturar a tela e mantém a trilha de
// auditoria. É seguro para uso concorrente.
type ConsentManager struct {
	mu       sync.Mutex
	promptMu sync.Mutex
	decision Decision
	prompt   Prompter
	persist  Persist
	logf     func(string)
	audit    []AuditEntry
}

// NewConsentManager cria o gerenciador. prompt/persist/logf podem ser nil.
func NewConsentManager(prompt Prompter, persist Persist, logf func(string)) *ConsentManager {
	m := &ConsentManager{decision: DecisionUndecided, prompt: prompt, persist: persist, logf: logf}
	if persist.Load != nil {
		if d, err := persist.Load(); err == nil && d != "" {
			m.decision = d
		}
	}
	return m
}

func (m *ConsentManager) log(msg string) {
	if m != nil && m.logf != nil {
		m.logf(msg)
	}
}

// Status retorna a decisão atual.
func (m *ConsentManager) Status() Decision {
	if m == nil {
		return DecisionDenied
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.decision
}

// Set define manualmente a decisão (usado pela UI: autorizar/revogar).
func (m *ConsentManager) Set(d Decision) error {
	if m == nil {
		return fmt.Errorf("consent manager indisponivel")
	}
	switch d {
	case DecisionSession, DecisionAlways, DecisionDenied, DecisionUndecided:
	default:
		return fmt.Errorf("decisao de captura invalida: %q", string(d))
	}
	m.mu.Lock()
	m.decision = d
	save := m.persist.Save
	m.mu.Unlock()
	if save != nil {
		if d == DecisionAlways {
			if err := save(d); err != nil {
				m.log("[screenshot] aviso: falha ao persistir autorizacao: " + err.Error())
			}
		} else {
			// Revogação/negar/sessão limpa a persistência anterior.
			if err := save(DecisionUndecided); err != nil {
				m.log("[screenshot] aviso: falha ao limpar autorizacao persistida: " + err.Error())
			}
		}
	}
	m.log("[screenshot] consentimento atualizado: " + string(d))
	return nil
}

// Revoke nega a captura e limpa a persistência.
func (m *ConsentManager) Revoke() error { return m.Set(DecisionDenied) }

// Authorized informa se a IA pode capturar sem novo diálogo.
func (m *ConsentManager) Authorized() bool {
	switch m.Status() {
	case DecisionSession, DecisionAlways:
		return true
	default:
		return false
	}
}

// Ensure garante autorização antes de uma captura solicitada pela IA.
//
//   - session/always → retorna imediatamente (sem diálogo);
//   - denied → retorna erro explicando que o usuário negou;
//   - undecided → abre o diálogo e classifica a resposta.
//
// O diálogo é serializado (promptMu): duas tools concorrentes nunca abrem
// perguntas duplicadas.
func (m *ConsentManager) Ensure(ctx context.Context, reason string) (Decision, error) {
	if m == nil {
		return DecisionDenied, fmt.Errorf("captura de tela indisponivel neste contexto")
	}
	if m.Authorized() {
		return m.Status(), nil
	}
	if m.Status() == DecisionDenied {
		return DecisionDenied, fmt.Errorf("o usuario negou a autorizacao de captura de tela")
	}

	m.promptMu.Lock()
	defer m.promptMu.Unlock()

	// Outra tool pode ter autorizado enquanto esperávamos o promptMu.
	if m.Authorized() {
		return m.Status(), nil
	}
	if m.Status() == DecisionDenied {
		return DecisionDenied, fmt.Errorf("o usuario negou a autorizacao de captura de tela")
	}

	if m.prompt == nil {
		return DecisionUndecided, fmt.Errorf("é preciso autorizar a captura de tela, mas nenhum diálogo está disponível")
	}

	question := BuildConsentQuestion(reason)
	answer, err := m.prompt(ctx, question, ConsentOptions)
	if err != nil {
		return DecisionUndecided, err
	}
	decision := ClassifyConsentAnswer(answer)
	if err := m.Set(decision); err != nil {
		return decision, err
	}
	if decision == DecisionDenied {
		return decision, fmt.Errorf("o usuario negou a autorizacao de captura de tela")
	}
	return decision, nil
}

// BuildConsentQuestion monta a pergunta de autorização exibida no chat.
func BuildConsentQuestion(reason string) string {
	var sb strings.Builder
	sb.WriteString("O assistente de IA pediu para capturar a tela deste computador")
	if r := strings.TrimSpace(reason); r != "" {
		sb.WriteString(" (" + r + ")")
	}
	sb.WriteString(". Permite a captura?")
	return sb.String()
}

// ClassifyConsentAnswer mapeia a resposta do usuário para uma decisão.
// Aceita as opções canônicas e texto livre ("sim", "pode", "não"...).
func ClassifyConsentAnswer(answer string) Decision {
	a := strings.ToLower(strings.TrimSpace(answer))
	if a == "" {
		return DecisionDenied
	}
	switch {
	case strings.Contains(a, "sempre"), strings.Contains(a, "always"), strings.Contains(a, "permanente"):
		return DecisionAlways
	case strings.Contains(a, "neg"), strings.Contains(a, "não"), strings.Contains(a, "nao"),
		strings.Contains(a, "no "), a == "no", strings.Contains(a, "recus"), strings.Contains(a, "cancel"):
		return DecisionDenied
	case strings.Contains(a, "permitir"), strings.Contains(a, "pode"), strings.Contains(a, "sim"),
		strings.Contains(a, "ok"), strings.Contains(a, "autoriz"), strings.Contains(a, "allow"):
		return DecisionSession
	default:
		// Resposta ambígua de texto livre: exige nova confirmação em vez de
		// assumir autorização permanente.
		return DecisionDenied
	}
}

// RecordCapture adiciona uma entrada de auditoria (ring buffer).
func (m *ConsentManager) RecordCapture(entry AuditEntry) {
	if m == nil {
		return
	}
	if entry.At.IsZero() {
		entry.At = time.Now()
	}
	m.mu.Lock()
	m.audit = append(m.audit, entry)
	if len(m.audit) > auditMax {
		m.audit = append([]AuditEntry(nil), m.audit[len(m.audit)-auditMax:]...)
	}
	m.mu.Unlock()
}

// Audit retorna uma cópia da trilha de auditoria (mais recente por último).
func (m *ConsentManager) Audit() []AuditEntry {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuditEntry, len(m.audit))
	copy(out, m.audit)
	return out
}
