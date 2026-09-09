package app

import (
	"discovery/app/logs"
)

// logBuffer foi movido para coreagent.LogBuffer (migração lote 2, §0.8);
// o campo Logs do App usa o tipo promovido do embed.

// sanitizeToken ofusca um token para logs.
func sanitizeToken(token string) string {
	return logs.SanitizeToken(token)
}

// truncateLogBody trunca um corpo de log.
func truncateLogBody(body []byte, max int) string {
	return logs.TruncateLogBody(body, max)
}

// captureStdLog redireciona o std log para o buffer e retorna um restore.
func captureStdLog(buf *logs.Buffer) func() {
	return logs.CaptureStdLog(buf)
}
