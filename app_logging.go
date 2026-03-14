package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// logBuffer stores command output lines for the embedded terminal view.
type logBuffer struct {
	mu    sync.RWMutex
	lines []string
	file  *os.File
}

func (l *logBuffer) append(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if strings.TrimSpace(line) == "" {
		l.appendLineLocked("")
		return
	}

	normalized := strings.ReplaceAll(line, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	for _, part := range strings.Split(normalized, "\n") {
		l.appendLineLocked(part)
	}
}

func (l *logBuffer) appendLineLocked(line string) {
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
}

func (l *logBuffer) getAll() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
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
