// Package ai: logger JSONL dedicado para logs de chat com IA.
// Independente do modo debug, salva todas as interações em chat_logs.jsonl.
package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// M9: limites de privacidade/espaco do log de chat — as interacoes (incl.
// userMsg/assistant/tool args) eram acumuladas em texto plano sem limite.
const (
	// chatLogMaxSize rotaciona o arquivo ativo quando atinge ~10 MB.
	chatLogMaxSize = int64(10 << 20)
	// chatLogRetention remove arquivos rotacionados com mais de 7 dias.
	chatLogRetention = 7 * 24 * time.Hour
	// chatLogMaxBackups teto de arquivos rotacionados mantidos.
	chatLogMaxBackups = 7
)

// ChatLogEntry representa uma entrada de log de chat.
type ChatLogEntry struct {
	Timestamp    string   `json:"timestamp"`
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

// ChatLogger é um logger thread-safe que escreve entradas JSONL
// para o arquivo chat_logs.jsonl no diretório de dados do agente.
type ChatLogger struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	enabled  bool
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

// Enable ativa o logger e abre/rotaciona o arquivo de log.
func (cl *ChatLogger) Enable(logDir string) {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if cl.enabled {
		return
	}

	filePath := filepath.Join(logDir, "chat_logs.jsonl")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return
	}

	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}

	cl.filePath = filePath
	cl.file = f
	cl.enabled = true

	// M9: limpeza de rotacoes antigas (privacidade/espaco em maquina de usuario).
	cl.cleanupRotationsLocked()
}

// Disable desativa o logger e fecha o arquivo.
func (cl *ChatLogger) Disable() {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	cl.enabled = false
	if cl.file != nil {
		_ = cl.file.Close()
		cl.file = nil
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

// Log escreve uma entrada de log no arquivo JSONL.
func (cl *ChatLogger) Log(entry ChatLogEntry) {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if !cl.enabled || cl.file == nil {
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

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	_, _ = cl.file.WriteString(string(data) + "\n")

	// M9: rotacao por tamanho — arquivo cheio vai para chat_logs-<ts>.jsonl.
	cl.rotateIfNeededLocked()
}

// rotateIfNeededLocked renomeia o arquivo atual quando excede chatLogMaxSize e
// abre um novo. Caller segura cl.mu.
func (cl *ChatLogger) rotateIfNeededLocked() {
	info, err := cl.file.Stat()
	if err != nil || info.Size() < chatLogMaxSize {
		return
	}
	_ = cl.file.Close()
	stamp := time.Now().UTC().Format("20060102-150405")
	rotated := filepath.Join(filepath.Dir(cl.filePath), "chat_logs-"+stamp+".jsonl")
	if err := os.Rename(cl.filePath, rotated); err != nil {
		// Sem rename: continua escrevendo no mesmo arquivo (degrada graciosamente).
		return
	}
	f, err := os.OpenFile(cl.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		cl.enabled = false
		cl.file = nil
		return
	}
	cl.file = f
	cl.cleanupRotationsLocked()
}

// cleanupRotationsLocked remove arquivos rotacionados vencidos (chatLogRetention)
// e mantém no máximo chatLogMaxBackups. Caller segura cl.mu.
func (cl *ChatLogger) cleanupRotationsLocked() {
	dir := filepath.Dir(cl.filePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-chatLogRetention)
	var rotated []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "chat_logs-") || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		rotated = append(rotated, e.Name())
	}
	// Ordem cronológica pelo nome (timestamp no nome).
	// Mantém os chatLogMaxBackups mais recentes; remove os excedentes e os
	// que passaram da retenção.
	keep := len(rotated) - chatLogMaxBackups
	for i, name := range rotated {
		full := filepath.Join(dir, name)
		remove := i < keep
		if !remove {
			if info, err := os.Stat(full); err == nil && info.ModTime().Before(cutoff) {
				remove = true
			}
		}
		if remove {
			_ = os.Remove(full)
		}
	}
}

// TruncateForLog helper para truncar mensagens longas nos logs
func TruncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + fmt.Sprintf("... (truncado, total %d chars)", len(s))
}
