// Package ai: logger JSONL dedicado para logs de chat com IA.
// Independente do modo debug, salva todas as interações em chat_logs.jsonl.
package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ChatLogEntry representa uma entrada de log de chat.
type ChatLogEntry struct {
	Timestamp   string `json:"timestamp"`
	Type        string `json:"type"`
	Endpoint    string `json:"endpoint,omitempty"`
	Method      string `json:"method,omitempty"` // sync / stream / async
	MessageLen  int    `json:"messageLen,omitempty"`
	SessionID   string `json:"sessionId,omitempty"`
	StatusCode  int    `json:"statusCode,omitempty"`
	TokensUsed  int    `json:"tokensUsed,omitempty"`
	LatencyMs   int    `json:"latencyMs,omitempty"`
	Error       string `json:"error,omitempty"`
	ResponseLen int    `json:"responseLen,omitempty"`
	StreamDone  bool   `json:"streamDone,omitempty"`
	HasTokens   bool   `json:"hasTokens,omitempty"`
	UserMsg     string `json:"userMsg,omitempty"`
	Assistant   string `json:"assistant,omitempty"`
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

// Log escreve uma entrada de log no arquivo JSONL.
func (cl *ChatLogger) Log(entry ChatLogEntry) {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if !cl.enabled || cl.file == nil {
		return
	}

	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	_, _ = cl.file.WriteString(string(data) + "\n")
}

// TruncateForLog helper para truncar mensagens longas nos logs
func TruncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + fmt.Sprintf("... (truncado, total %d chars)", len(s))
}
