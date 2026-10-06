// Package ai: logger dedicado para as interações de chat com IA.
// Independente do modo debug, salva todas as interações em
// %ProgramData%\Discovery\logs\chat.db (SQLite — separado de logs.db por
// privacidade: coletar logs do agente não expõe conversas).
package ai

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"discovery/app/core/logstore"
)

// ChatLogEntry representa uma entrada de log de chat.
type ChatLogEntry struct {
	Timestamp string `json:"timestamp"`
	// CodeRev identifica a revisão de código (hash curto do commit + "+mod"
	// quando o worktree estava sujo) que produziu esta entrada — diagnosticar
	// um log antigo passa a dizer QUAL versão do código o gerou. Preenchido
	// automaticamente em Log() a partir do VCS stamping do build.
	CodeRev      string   `json:"codeRev,omitempty"`
	Type         string   `json:"type"`
	Endpoint     string   `json:"endpoint,omitempty"`
	Method       string   `json:"method,omitempty"` // sync / stream / multi_round / tool_exec
	MessageLen   int      `json:"messageLen,omitempty"`
	SessionID    string   `json:"sessionId,omitempty"`
	StatusCode   int      `json:"statusCode,omitempty"`
	TokensUsed   int      `json:"tokensUsed,omitempty"`
	LatencyMs    int      `json:"latencyMs,omitempty"`
	Error        string   `json:"error,omitempty"`
	ResponseLen  int      `json:"responseLen,omitempty"`
	StreamDone   bool     `json:"streamDone,omitempty"`
	HasTokens    bool     `json:"hasTokens,omitempty"`
	UserMsg      string   `json:"userMsg,omitempty"`
	Assistant    string   `json:"assistant,omitempty"`
	Round        int      `json:"round,omitempty"`        // multi-round: número do round (0-based)
	ToolCount    int      `json:"toolCount,omitempty"`    // multi-round: quantas tools enviadas no request
	HasToolCalls bool     `json:"hasToolCalls,omitempty"` // multi-round: true se LLM retornou tool_calls
	ToolCalls    []string `json:"toolCalls,omitempty"`    // multi-round: nomes das tools chamadas pelo LLM
	ToolArgs     []string `json:"toolArgs,omitempty"`     // multi-round: argumentos das tools chamadas (truncados 300 chars)
	ToolResults  []string `json:"toolResults,omitempty"`  // multi-round: resultados das execuções (truncados)
}

// ChatLogger persiste as interações de chat em chat.db. A retenção (7 dias),
// o teto de tamanho (128 MB) e a purga (startup + 24 h) são responsabilidade
// do logstore — o logger só redige segredos e enfileira.
type ChatLogger struct {
	mu      sync.Mutex
	store   *logstore.Store
	enabled bool
}

// NewChatLogger cria um novo logger de chat. Se logDir for vazio,
// o logger é criado desabilitado até que Enable seja chamado.
func NewChatLogger(logDir string) *ChatLogger {
	cl := &ChatLogger{}
	if logDir != "" {
		cl.Enable(logDir)
	}
	return cl
}

// Enable ativa o logger abrindo (ou reutilizando) chat.db no diretório dado.
// Falha de abertura deixa o logger desabilitado — nunca é fatal.
func (cl *ChatLogger) Enable(logDir string) {
	logDir = strings.TrimSpace(logDir)
	if logDir == "" {
		return
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.enabled {
		return
	}
	store, err := logstore.Open(logstore.Options{
		Path:      filepath.Join(logDir, "chat.db"),
		Kind:      logstore.KindChat,
		Retention: logstore.DefaultRetention,
		MaxBytes:  logstore.DefaultMaxBytes,
	})
	if err != nil {
		return
	}
	cl.store = store
	cl.enabled = true
	// Purga no startup: idade (7 dias) + teto de tamanho.
	_ = store.Purge()
}

// Disable desativa o logger, drenando a fila e fechando o banco.
func (cl *ChatLogger) Disable() {
	cl.mu.Lock()
	store := cl.store
	cl.store = nil
	cl.enabled = false
	cl.mu.Unlock()
	if store != nil {
		_ = store.Close()
	}
}

// IsEnabled retorna true se o logger está ativo.
func (cl *ChatLogger) IsEnabled() bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.enabled
}

// Padrões sensíveis redigidos antes de gravar (M8, privacidade): tokens de
// agente (mdz_...), Bearer/Authorization e chaves sk-.
var sensitiveRedactions = []*regexp.Regexp{
	regexp.MustCompile(`mdz_[A-Za-z0-9_\-]{8,}`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)\S+`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]+`),
	regexp.MustCompile(`sk\-[A-Za-z0-9]{16,}`),
}

// redactSensitive substitui padrões de segredo por "[redacted]".
func redactSensitive(s string) string {
	if s == "" {
		return s
	}
	for _, re := range sensitiveRedactions {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	return s
}

// Log grava uma entrada de log no chat.db. É thread-safe e não bloqueia.
func (cl *ChatLogger) Log(entry ChatLogEntry) {
	cl.mu.Lock()
	store := cl.store
	enabled := cl.enabled
	cl.mu.Unlock()
	if !enabled || store == nil {
		return
	}

	// M8: redige segredos antes de gravar em disco.
	entry.UserMsg = redactSensitive(entry.UserMsg)
	entry.Assistant = redactSensitive(entry.Assistant)
	entry.Error = redactSensitive(entry.Error)
	for i, v := range entry.ToolArgs {
		entry.ToolArgs[i] = redactSensitive(v)
	}
	for i, v := range entry.ToolResults {
		entry.ToolResults[i] = redactSensitive(v)
	}

	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	// Revisão de código em toda linha: diagnóstico/analise sabem qual versão
	// do código gerou o log (cached em codeRevision).
	if entry.CodeRev == "" {
		entry.CodeRev = codeRevision()
	}

	ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		ts = time.Now().UTC()
	}

	store.AppendChat(logstore.ChatEntry{
		Timestamp:    ts,
		CodeRev:      entry.CodeRev,
		Type:         entry.Type,
		Endpoint:     entry.Endpoint,
		Method:       entry.Method,
		SessionID:    entry.SessionID,
		StatusCode:   entry.StatusCode,
		LatencyMs:    entry.LatencyMs,
		TokensUsed:   entry.TokensUsed,
		MessageLen:   entry.MessageLen,
		ResponseLen:  entry.ResponseLen,
		Round:        entry.Round,
		ToolCount:    entry.ToolCount,
		StreamDone:   entry.StreamDone,
		HasTokens:    entry.HasTokens,
		HasToolCalls: entry.HasToolCalls,
		Error:        entry.Error,
		UserMsg:      entry.UserMsg,
		Assistant:    entry.Assistant,
		ToolCalls:    entry.ToolCalls,
		ToolArgs:     entry.ToolArgs,
		ToolResults:  entry.ToolResults,
	})
}

// TruncateForLog helper para truncar mensagens longas nos logs
func TruncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + fmt.Sprintf("... (truncado, total %d chars)", len(s))
}
