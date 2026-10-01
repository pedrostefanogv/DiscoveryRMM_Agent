package screenshot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Decision é o resultado de UMA solicitação de captura feita pela IA.
//
// NÃO existe autorização permanente: cada pedido da IA exige uma resposta
// explícita do usuário (decisão de produto 2026-10-01 — "o pedido de print ao
// usuário deve ocorrer TODAS as vezes que a IA pedir"). Não há "permitir
// sempre" nem "permitir nesta sessão".
type Decision string

const (
	// DecisionUndecided: ainda não respondido (o pedido está aberto).
	DecisionUndecided Decision = "undecided"
	// DecisionGranted: permitido APENAS para esta captura.
	DecisionGranted Decision = "granted"
	// DecisionDenied: negado APENAS para esta captura (a próxima pergunta de novo).
	DecisionDenied Decision = "denied"
)

// Prompter pergunta ao usuário e aguarda a resposta (no agent é o diálogo
// interativo do chat — App.AskUserContext).
type Prompter func(ctx context.Context, question string, options []string) (string, error)

// AuditEntry registra uma decisão de captura (trilha de auditoria da UI).
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
	Thumbnail []byte `json:"-"`
	// ThumbnailMIME acompanha Thumbnail (o encoder escolhe WebP lossy quando
	// disponível; usar "image/jpeg" fixo quebrava a renderização da miniatura).
	ThumbnailMIME string `json:"-"`
	HasThumbnail  bool   `json:"hasThumbnail"`
}

// consentDialogs é o texto do diálogo de autorização por idioma. O locale vem
// do binding GetPreferredLocale do agent.
var consentDialogs = map[string]struct {
	Question string
	Options  []string
}{
	"pt": {
		Question: "O assistente de IA pediu para capturar a tela deste computador%s. Permite esta captura?",
		Options:  []string{"Permitir esta captura", "Negar"},
	},
	"en": {
		Question: "The AI assistant requested a screen capture of this computer%s. Allow this capture?",
		Options:  []string{"Allow this capture", "Deny"},
	},
	"es": {
		Question: "El asistente de IA solicitó capturar la pantalla de este equipo%s. ¿Permitir esta captura?",
		Options:  []string{"Permitir esta captura", "Negar"},
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

// ClassifyConsentAnswer mapeia a resposta do usuário para permitir/negar ESTA
// captura. Texto livre é aceito ("sim", "pode", "não"...). Resposta ambígua
// nega (nunca autoriza por engano). "sempre"/"always" não criam autorização
// permanente — valem apenas para a captura em questão.
func ClassifyConsentAnswer(answer string) Decision {
	a := strings.ToLower(strings.TrimSpace(answer))
	if a == "" {
		return DecisionDenied
	}
	switch {
	case strings.Contains(a, "neg"), strings.Contains(a, "não"), strings.Contains(a, "nao"),
		strings.Contains(a, "no "), a == "no", strings.Contains(a, "recus"), strings.Contains(a, "cancel"),
		strings.Contains(a, "deny"), strings.Contains(a, "nunca"), strings.Contains(a, "jamais"),
		strings.Contains(a, "never"):
		return DecisionDenied
	case strings.Contains(a, "permitir"), strings.Contains(a, "permit"), strings.Contains(a, "pode"),
		strings.Contains(a, "sim"), a == "sí", strings.Contains(a, "ok"),
		strings.Contains(a, "autoriz"), strings.Contains(a, "allow"), strings.Contains(a, "sempre"),
		strings.Contains(a, "siempre"), strings.Contains(a, "always"):
		return DecisionGranted
	default:
		return DecisionDenied
	}
}

const auditMax = 50

// ConsentManager conduz a autorização POR CAPTURA e mantém a trilha de
// auditoria. Seguro para uso concorrente.
type ConsentManager struct {
	mu       sync.Mutex
	promptMu sync.Mutex
	prompt   Prompter
	logf     func(string)
	// locale devolve o idioma preferido (ex.: "pt-BR") para o diálogo.
	locale func() string
	audit  []AuditEntry
	nextID int64
}

// NewConsentManager cria o gerenciador. prompt/logf/locale podem ser nil.
func NewConsentManager(prompt Prompter, logf func(string), locale func() string) *ConsentManager {
	return &ConsentManager{prompt: prompt, logf: logf, locale: locale}
}

func (m *ConsentManager) log(msg string) {
	if m != nil && m.logf != nil {
		m.logf(msg)
	}
}

// Ensure pede autorização para ESTA captura, SEMPRE.
//
// O diálogo é serializado (promptMu): duas tools concorrentes nunca abrem
// perguntas duplicadas ao mesmo tempo — a segunda espera a resposta da
// primeira e então faz a SUA própria pergunta (o requisito é perguntar a cada
// pedido).
func (m *ConsentManager) Ensure(ctx context.Context, reason string) (Decision, error) {
	if m == nil {
		return DecisionDenied, fmt.Errorf("captura de tela indisponivel neste contexto")
	}
	m.promptMu.Lock()
	defer m.promptMu.Unlock()
	if m.prompt == nil {
		return DecisionUndecided, fmt.Errorf("é preciso autorizar a captura de tela, mas nenhum diálogo está disponível")
	}
	lang := ""
	if m.locale != nil {
		lang = m.locale()
	}
	answer, err := m.prompt(ctx, BuildConsentQuestion(reason, lang), ConsentOptions(lang))
	if err != nil {
		return DecisionUndecided, err
	}
	decision := ClassifyConsentAnswer(answer)
	m.log("[screenshot] autorizacao da captura respondida pelo usuario: " + string(decision))
	if decision != DecisionGranted {
		return decision, fmt.Errorf("o usuario negou a autorizacao para esta captura de tela")
	}
	return decision, nil
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
