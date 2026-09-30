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
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Decision Decision  `json:"decision"`
	Mode     string    `json:"mode"`
	Detail   string    `json:"detail"`
	ByLLM    bool      `json:"byLlm"`
	Bytes    int       `json:"bytes"`
	// Thumbnail é a miniatura do que foi capturado. NÃO é serializada no
	// binding de status/tool (payload e privacidade); a UI de privacidade busca
	// as miniaturas pelo binding GetScreenshotAuditPanel.
	Thumbnail    []byte `json:"-"`
	HasThumbnail bool   `json:"hasThumbnail"`
}

// consentDialogs é o texto do diálogo de autorização por idioma. O locale vem
// do binding GetPreferredLocale do agent.
var consentDialogs = map[string]struct {
	Question string
	Options  []string
}{
	"pt": {
		Question: "O assistente de IA pediu para capturar a tela deste computador%s. Permite a captura?",
		Options:  []string{"Permitir nesta conversa", "Permitir sempre", "Negar"},
	},
	"en": {
		Question: "The AI assistant requested a screen capture of this computer%s. Allow the capture?",
		Options:  []string{"Allow this conversation", "Always allow", "Deny"},
	},
	"es": {
		Question: "El asistente de IA solicitó capturar la pantalla de este equipo%s. ¿Permitir la captura?",
		Options:  []string{"Permitir esta conversación", "Permitir siempre", "Negar"},
	},
}

// ConsentLanguage mapeia um locale ("pt-BR", "en-US", "es-ES") para a chave de
// idioma do diálogo. Locale desconhecido → inglês; locale vazio (sem informação)
// mantém pt-BR, o comportamento original.
func ConsentLanguage(locale string) string {
	v := strings.ToLower(strings.TrimSpace(locale))
	switch {
	case v == "":
		return "pt"
	case strings.HasPrefix(v, "pt"):
		return "pt"
	case strings.HasPrefix(v, "es"):
		return "es"
	default:
		return "en"
	}
}

// ConsentOptions retorna as opções do diálogo no idioma informado.
func ConsentOptions(locale string) []string {
	dialog := consentDialogs[ConsentLanguage(locale)]
	out := make([]string, len(dialog.Options))
	copy(out, dialog.Options)
	return out
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
	// locale devolve o idioma preferido (ex.: "pt-BR") para o diálogo.
	locale func() string
	audit  []AuditEntry
	nextID int64
}

// NewConsentManager cria o gerenciador. prompt/persist/logf/locale podem ser nil.
func NewConsentManager(prompt Prompter, persist Persist, logf func(string), locale func() string) *ConsentManager {
	m := &ConsentManager{decision: DecisionUndecided, prompt: prompt, persist: persist, logf: logf, locale: locale}
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

	lang := ""
	if m.locale != nil {
		lang = m.locale()
	}
	question := BuildConsentQuestion(reason, lang)
	answer, err := m.prompt(ctx, question, ConsentOptions(lang))
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

// BuildConsentQuestion monta a pergunta de autorização exibida no chat, no
// idioma do locale informado.
func BuildConsentQuestion(reason, locale string) string {
	dialog := consentDialogs[ConsentLanguage(locale)]
	suffix := ""
	if r := strings.TrimSpace(reason); r != "" {
		suffix = " (" + r + ")"
	}
	return fmt.Sprintf(dialog.Question, suffix)
}

// ClassifyConsentAnswer mapeia a resposta do usuário para uma decisão.
// Aceita as opções canônicas e texto livre ("sim", "pode", "não"...).
func ClassifyConsentAnswer(answer string) Decision {
	a := strings.ToLower(strings.TrimSpace(answer))
	if a == "" {
		return DecisionDenied
	}
	switch {
	case strings.Contains(a, "sempre"), strings.Contains(a, "siempre"), strings.Contains(a, "always"),
		strings.Contains(a, "permanente"), strings.Contains(a, "permanent"):
		return DecisionAlways
	case strings.Contains(a, "neg"), strings.Contains(a, "não"), strings.Contains(a, "nao"),
		strings.Contains(a, "no "), a == "no", strings.Contains(a, "recus"), strings.Contains(a, "cancel"),
		strings.Contains(a, "deny"), strings.Contains(a, "nunca"), strings.Contains(a, "jamais"),
		strings.Contains(a, "never"):
		return DecisionDenied
	case strings.Contains(a, "permitir"), strings.Contains(a, "permit"), strings.Contains(a, "pode"),
		strings.Contains(a, "sim"), a == "sí",
		strings.Contains(a, "ok"), strings.Contains(a, "autoriz"), strings.Contains(a, "allow"):
		return DecisionSession
	default:
		// Resposta ambígua de texto livre: exige nova confirmação em vez de
		// assumir autorização permanente.
		return DecisionDenied
	}
}

// RecordCapture adiciona uma entrada de auditoria (ring buffer) e devolve a
// entrada armazenada — com ID atribuído — para o chamador correlacionar com a
// imagem cheia mantida em memória (lightbox do chat).
func (m *ConsentManager) RecordCapture(entry AuditEntry) AuditEntry {
	if m == nil {
		return entry
	}
	if entry.At.IsZero() {
		entry.At = time.Now()
	}
	m.mu.Lock()
	m.nextID++
	entry.ID = m.nextID
	entry.HasThumbnail = len(entry.Thumbnail) > 0
	m.audit = append(m.audit, entry)
	if len(m.audit) > auditMax {
		m.audit = append([]AuditEntry(nil), m.audit[len(m.audit)-auditMax:]...)
	}
	m.mu.Unlock()
	return entry
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
