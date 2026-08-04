package app

import (
	"discovery/app/logs"
)

// logBuffer é um wrapper sobre logs.Buffer que re-expõe os métodos com nomes
// minúsculos, mantendo compatibilidade com as chamadas existentes
// (a.logs.append, a.logs.getAll, etc.).
type logBuffer struct {
	*logs.Buffer
}

func (l *logBuffer) append(line string) {
	if l == nil || l.Buffer == nil {
		return
	}
	l.Buffer.Append(line)
}

func (l *logBuffer) subscribe(fn func(string)) func() {
	if l == nil || l.Buffer == nil {
		return func() {}
	}
	return l.Buffer.Subscribe(fn)
}

func (l *logBuffer) snapshotAndSubscribe(fn func(string)) func() {
	if l == nil || l.Buffer == nil {
		return func() {}
	}
	return l.Buffer.SnapshotAndSubscribe(fn)
}

func (l *logBuffer) getAll() []string {
	if l == nil || l.Buffer == nil {
		return nil
	}
	return l.Buffer.GetAll()
}

func (l *logBuffer) count() int {
	if l == nil || l.Buffer == nil {
		return 0
	}
	return l.Buffer.Count()
}

func (l *logBuffer) exportFormatted(filterOrigin string) string {
	if l == nil || l.Buffer == nil {
		return ""
	}
	return l.Buffer.ExportFormatted(filterOrigin)
}

func (l *logBuffer) clear() {
	if l == nil || l.Buffer == nil {
		return
	}
	l.Buffer.Clear()
}

func (l *logBuffer) enableFilePersistence(path string) error {
	if l == nil || l.Buffer == nil {
		return nil
	}
	return l.Buffer.EnableFilePersistence(path)
}

func (l *logBuffer) closeFile() {
	if l == nil || l.Buffer == nil {
		return
	}
	l.Buffer.CloseFile()
}

// sanitizeToken ofusca um token para logs.
func sanitizeToken(token string) string {
	return logs.SanitizeToken(token)
}

// truncateLogBody trunca um corpo de log.
func truncateLogBody(body []byte, max int) string {
	return logs.TruncateLogBody(body, max)
}

// captureStdLog redireciona o std log para o buffer e retorna um restore.
func captureStdLog(buf *logBuffer) func() {
	return logs.CaptureStdLog(buf.Buffer)
}
