// Package logs encapsula o buffer de logs em memória + persistência em arquivo
// usado pelo terminal embutido do agente, separado do App.
package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"discovery/app/core/buildinfo"
	"discovery/app/core/logger"
	"discovery/app/core/logstore"
)

// Buffer stores command output lines for the embedded terminal view.
type Buffer struct {
	mu          sync.RWMutex
	lines       []string
	file        *os.File
	store       *logstore.Store
	nextSubID   uint64
	subscribers map[uint64]func(string)
}

// New cria um Buffer de logs.
func New() *Buffer {
	return &Buffer{}
}

// Append adiciona uma linha ao buffer e a persiste (arquivo + banco unificado).
func (l *Buffer) Append(line string) {
	l.append(line, true)
}

// appendCaptured adiciona uma linha vinda do sink do logger SEM gravar no banco
// unificado: quem grava essas linhas é o storeSink do logger
// (logger.SetStoreSink, registrado em app/logstore.go). Gravar nos dois caminhos
// inseria a MESMA linha duas vezes em logs.db (ids consecutivos, mesmo ts_ms) —
// cada log.Printf/log.Println aparecia duplicado no banco de logs.
//
// A divisão é por origem, não por ordem de inicialização:
//   - linhas capturadas do logger → storeSink (único gravador no banco);
//   - linhas de a.Logs.Append(...) → este caminho (\"a.Logs\" não passa pelo logger).
func (l *Buffer) appendCaptured(line string) {
	l.append(line, false)
}

// append é o núcleo de Append/appendCaptured.
func (l *Buffer) append(line string, persistStore bool) {
	l.mu.Lock()
	var appended []string

	if strings.TrimSpace(line) == "" {
		appended = append(appended, l.appendLineLocked("", persistStore))
	} else {
		normalized := strings.ReplaceAll(line, "\r\n", "\n")
		normalized = strings.ReplaceAll(normalized, "\r", "\n")
		for _, part := range strings.Split(normalized, "\n") {
			appended = append(appended, l.appendLineLocked(part, persistStore))
		}
	}
	subscribers := l.snapshotSubscribersLocked()
	l.mu.Unlock()

	for _, item := range appended {
		for _, sub := range subscribers {
			sub(item)
		}
	}
}

func (l *Buffer) appendLineLocked(line string, persistStore bool) string {
	const maxLineBytes = 8192
	if len(line) > maxLineBytes {
		line = line[:maxLineBytes] + "... (truncado)"
	}
	l.lines = append(l.lines, line)
	if len(l.lines) > 5000 {
		l.lines = l.lines[len(l.lines)-5000:]
	}
	if l.file != nil {
		// Assinatura de revisão: toda linha persistida declara qual versão do
		// código a gerou (hash curto do commit), junto da data — mesmo contrato
		// do chat_logs.jsonl. Diagnosticar um log antigo diz a versão exata.
		_, _ = l.file.WriteString(time.Now().Format(time.RFC3339) + " [" + buildinfo.Revision() + "] " + line + "\n")
	}
	if persistStore && l.store != nil {
		level, msg := logstore.ParseLevel(line)
		l.store.AppendLog(level, buildinfo.Revision(), msg)
	}
	return line
}

func (l *Buffer) snapshotSubscribersLocked() []func(string) {
	if len(l.subscribers) == 0 {
		return nil
	}
	out := make([]func(string), 0, len(l.subscribers))
	for _, fn := range l.subscribers {
		out = append(out, fn)
	}
	return out
}

// Subscribe registra um subscriber e retorna uma função de cancelamento.
func (l *Buffer) Subscribe(fn func(string)) func() {
	if fn == nil {
		return func() {}
	}

	l.mu.Lock()
	if l.subscribers == nil {
		l.subscribers = make(map[uint64]func(string))
	}
	l.nextSubID++
	id := l.nextSubID
	l.subscribers[id] = fn
	l.mu.Unlock()

	return func() {
		l.mu.Lock()
		delete(l.subscribers, id)
		l.mu.Unlock()
	}
}

// SnapshotAndSubscribe retorna o snapshot atual e registra um subscriber.
func (l *Buffer) SnapshotAndSubscribe(fn func(string)) func() {
	if fn == nil {
		return func() {}
	}

	l.mu.Lock()
	snapshot := make([]string, len(l.lines))
	copy(snapshot, l.lines)

	if l.subscribers == nil {
		l.subscribers = make(map[uint64]func(string))
	}
	l.nextSubID++
	id := l.nextSubID
	l.subscribers[id] = fn
	l.mu.Unlock()

	for _, line := range snapshot {
		fn(line)
	}

	return func() {
		l.mu.Lock()
		delete(l.subscribers, id)
		l.mu.Unlock()
	}
}

// GetAll retorna todas as linhas do buffer.
func (l *Buffer) GetAll() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}

// Count retorna o total de linhas no buffer.
func (l *Buffer) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.lines)
}

// ExportFormatted retorna o conteúdo formatado para exportação, opcionalmente filtrado.
func (l *Buffer) ExportFormatted(filterOrigin string) string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	var buf strings.Builder
	buf.WriteString("=== Discovery Agent Logs ===\n")
	buf.WriteString(fmt.Sprintf("Exportado em: %s\n", time.Now().Format(time.RFC3339)))
	buf.WriteString(fmt.Sprintf("Total de linhas: %d\n\n", len(l.lines)))

	for _, line := range l.lines {
		if filterOrigin != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(filterOrigin)) {
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	return buf.String()
}

// Clear esvazia o buffer.
func (l *Buffer) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = nil
}

// EnableFilePersistence habilita a persistência em arquivo.
func (l *Buffer) EnableFilePersistence(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
	}
	l.file = f
	return nil
}

// EnableStore encaminha cada linha para o banco unificado (logs.db).
// A chamada é não bloqueante por natureza (o Store enfileira e descarta se a
// fila encher), então pode ser feita com o mutex do buffer preso.
func (l *Buffer) EnableStore(s *logstore.Store) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store = s
}

// CloseFile fecha o arquivo de persistência.
func (l *Buffer) CloseFile() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// SanitizeToken ofusca um token para logs.
func SanitizeToken(token string) string {
	t := strings.TrimSpace(token)
	if t == "" {
		return ""
	}
	if len(t) <= 8 {
		return "***"
	}
	return t[:4] + "..." + t[len(t)-4:]
}

// TruncateLogBody trunca um corpo de log.
func TruncateLogBody(body []byte, max int) string {
	s := strings.TrimSpace(string(body))
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// CaptureStdLog redireciona o std log para o buffer e retorna um restore.
// Usa appendCaptured (e não Append): as linhas do logger já são persistidas no
// banco pelo storeSink — ver comentário de appendCaptured.
func CaptureStdLog(buf *Buffer) func() {
	logger.SetSink(logger.LogBufferAdapter(buf.appendCaptured))
	return func() {
		logger.SetSink(nil)
	}
}
