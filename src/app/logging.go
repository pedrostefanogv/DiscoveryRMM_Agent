package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// logBuffer stores command output lines for the embedded terminal view.
type logBuffer struct {
	mu          sync.RWMutex
	lines       []string
	file        *os.File
	nextSubID   uint64
	subscribers map[uint64]func(string)
}

func (l *logBuffer) append(line string) {
	l.mu.Lock()
	var appended []string

	if strings.TrimSpace(line) == "" {
		appended = append(appended, l.appendLineLocked(""))
	} else {
		normalized := strings.ReplaceAll(line, "\r\n", "\n")
		normalized = strings.ReplaceAll(normalized, "\r", "\n")
		for _, part := range strings.Split(normalized, "\n") {
			appended = append(appended, l.appendLineLocked(part))
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

func (l *logBuffer) appendLineLocked(line string) string {
	const maxLineBytes = 8192
	if len(line) > maxLineBytes {
		line = line[:maxLineBytes] + "... (truncado)"
	}
	l.lines = append(l.lines, line)
	if len(l.lines) > 5000 {
		l.lines = l.lines[len(l.lines)-5000:]
	}
	if l.file != nil {
		_, _ = l.file.WriteString(time.Now().Format(time.RFC3339) + " " + line + "\n")
	}
	return line
}

func (l *logBuffer) snapshotSubscribersLocked() []func(string) {
	if len(l.subscribers) == 0 {
		return nil
	}
	out := make([]func(string), 0, len(l.subscribers))
	for _, fn := range l.subscribers {
		out = append(out, fn)
	}
	return out
}

func (l *logBuffer) subscribe(fn func(string)) func() {
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

func (l *logBuffer) getAll() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}

func (l *logBuffer) count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.lines)
}

func (l *logBuffer) exportFormatted(filterOrigin string) string {
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

func (l *logBuffer) clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = nil
}

func (l *logBuffer) enableFilePersistence(path string) error {
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

func (l *logBuffer) closeFile() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

func sanitizeToken(token string) string {
	t := strings.TrimSpace(token)
	if t == "" {
		return ""
	}
	if len(t) <= 8 {
		return "***"
	}
	return t[:4] + "..." + t[len(t)-4:]
}

func truncateLogBody(body []byte, max int) string {
	s := strings.TrimSpace(string(body))
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
