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
// A regra base continua sendo perguntar a CADA pedido (decisão de produto
// 2026-10-01). O usuário pode, porém, escolher "permitir sempre" — permissão
// PERMANENTE, desligada por padrão, persistida no arquivo da política (sobrevive
// a reinício/atualização do agente) e revogável a qualquer momento no painel de
// privacidade ou voltando ao padrão.
type Decision string

const (
	// DecisionUndecided: ainda não respondido (o pedido está aberto).
	DecisionUndecided Decision = "undecided"
	// DecisionGranted: permitido APENAS para esta captura.
	DecisionGranted Decision = "granted"
	// DecisionGrantedForSession: permitido para esta captura E para as demais
	// da sessão atual — o agente deixa de perguntar até o usuário revogar.
	DecisionGrantedForSession Decision = "granted_session"
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
	// SessionAuthorized indica que a captura foi coberta pela permissão
	// permanente ("permitir sempre"), sem pergunta individual.
	SessionAuthorized bool `json:"sessionAuthorized,omitempty"`
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
		Options:  []string{"Permitir esta captura", "Permitir sempre (até eu desativar)", "Negar"},
	},
	"en": {
		Question: "The AI assistant requested a screen capture of this computer%s. Allow this capture?",
		Options:  []string{"Allow this capture", "Always allow (until I turn it off)", "Deny"},
	},
	"es": {
		Question: "El asistente de IA solicitó capturar la pantalla de este equipo%s. ¿Permitir esta captura?",
		Options:  []string{"Permitir esta captura", "Permitir siempre (hasta que lo desactive)", "Negar"},
	},
}

// isPermanentGrantPhrase reconhece a opção "permitir sempre" (permissão
// PERMANENTE, persistida) nas três línguas — na redação atual ("até eu
// desativar" / "until I turn it off" / "hasta que lo desactive") e na antiga
// ("nesta sessão"). "Sempre" sozinho continua valendo só para a captura em
// questão, como antes.
func isPermanentGrantPhrase(a string) bool {
	always := strings.Contains(a, "sempre") || strings.Contains(a, "siempre") || strings.Contains(a, "always")
	if !always {
		return false
	}
	return strings.Contains(a, "sess") || strings.Contains(a, "sesi") ||
		strings.Contains(a, "desativ") || strings.Contains(a, "desactive") ||
		strings.Contains(a, "turn it off") || strings.Contains(a, "desligar")
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
// nega (nunca autoriza por engano). A negação é avaliada PRIMEIRO (fail-closed:
// "não, pode sempre" nega). "sempre" sozinho continua valendo só para a captura
// em questão; a autorização de sessão exige a menção explícita a "sessão"
// (option "Permitir sempre nesta sessão").
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
	case isPermanentGrantPhrase(a):
		return DecisionGrantedForSession
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
	// sessionGrant devolve true quando o usuário já liberou a sessão inteira.
	// É consultado SOB promptMu: se a liberação acontecer enquanto outra captura
	// aguardava na fila, a próxima NÃO abre uma segunda pergunta.
	//
	// Deve ser registrado UMA vez com SetSessionGrant, antes de o manager ser
	// publicado ao resto do app (é o que initScreenshotService faz) — por isso a
	// leitura não usa lock: é escrita única antes da publicação.
	sessionGrant func() bool
	audit        []AuditEntry
	nextID       int64
}

// NewConsentManager cria o gerenciador. prompt/logf/locale podem ser nil.
func NewConsentManager(prompt Prompter, logf func(string), locale func() string) *ConsentManager {
	return &ConsentManager{prompt: prompt, logf: logf, locale: locale}
}

// SetSessionGrant registra a consulta de autorização de SESSÃO (nil = sempre
// pergunta). Chame ANTES de publicar o manager para outras goroutines (o app
// faz isso dentro do init idempotente) — a leitura em Ensure não usa lock.
func (m *ConsentManager) SetSessionGrant(fn func() bool) {
	if m == nil {
		return
	}
	m.sessionGrant = fn
}

func (m *ConsentManager) log(msg string) {
	if m != nil && m.logf != nil {
		m.logf(msg)
	}
}

// Ensure pede autorização para ESTA captura, SEMPRE — a menos que o chamador já
// tenha a autorização de sessão ligada (esse curto-circuito é do app, no
// CaptureScreenshotForTool, para não duplicar perguntas).
//
// A resposta pode autorizar apenas esta captura (DecisionGranted) ou a sessão
// inteira (DecisionGrantedForSession). Nos dois casos não há erro: cabe ao
// chamador registrar a decisão de sessão (o manager não guarda estado de
// autorização — a trilha de auditoria é o que ele mantém).
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
	// Reconfere a autorização de SESSÃO já com o turno do diálogo adquirido:
	// duas capturas concorrentes não podem fazer o usuário responder "permitir
	// sempre nesta sessão" na primeira e ver uma segunda pergunta em seguida.
	if m.sessionGrant != nil && m.sessionGrant() {
		m.log("[screenshot] captura autorizada pela permissao permanente (sem nova pergunta)")
		return DecisionGrantedForSession, nil
	}
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
	if decision != DecisionGranted && decision != DecisionGrantedForSession {
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
