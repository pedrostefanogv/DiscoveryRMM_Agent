package logstore

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LegacyDirName é a subpasta que guarda os arquivos de log antigos após o
// corte para o SQLite (preserva o histórico por uma janela, sem poluir a raiz).
const LegacyDirName = "_legacy"

// legacyNames são os arquivos do modelo antigo (pré-SQLite).
var legacyNames = map[string]bool{
	"agent.log":         true,
	"agent-service.log": true,
	"chat_logs.jsonl":   true,
}

// ArchiveLegacyLogs move os arquivos de log do modelo antigo para
// <logDir>/_legacy/. Deve ser chamado APENAS quando o banco abriu com sucesso
// — senão o fallback em texto (agent.log) seria arquivado em uso.
// Devolve os nomes movidos. Nunca é fatal.
func ArchiveLegacyLogs(logDir string) []string {
	logDir = strings.TrimSpace(logDir)
	if logDir == "" {
		return nil
	}
	var moved []string
	destDir := filepath.Join(logDir, LegacyDirName)
	for _, name := range listLegacyFiles(logDir) {
		src := filepath.Join(logDir, name)
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			continue
		}
		dst := filepath.Join(destDir, name)
		// Windows: os.Rename falha se o destino existir.
		if _, err := os.Stat(dst); err == nil {
			_ = os.Remove(dst)
		}
		if err := os.Rename(src, dst); err == nil {
			moved = append(moved, name)
		}
	}
	return moved
}

// listLegacyFiles devolve os nomes de arquivo (não diretórios) que pertencem
// ao modelo antigo de logs: agent.log, agent-service.log, chat_logs.jsonl e as
// rotações chat_logs-<timestamp>.jsonl.
func listLegacyFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		switch {
		case legacyNames[lower]:
			names = append(names, name)
		case strings.HasPrefix(lower, "chat_logs-") && strings.HasSuffix(lower, ".jsonl"):
			names = append(names, name)
		}
	}
	return names
}

// PurgeStaleFiles remove, no diretório de logs, arquivos que NÃO são bancos
// vivos mais antigos que a retenção (installer.log, fallback em texto, rotações
// legadas) e também os arquivos de _legacy/. Nunca remove .db/-journal/-wal/-shm.
func PurgeStaleFiles(logDir string, retention time.Duration) int {
	logDir = strings.TrimSpace(logDir)
	if logDir == "" || retention <= 0 {
		return 0
	}
	cutoff := time.Now().Add(-retention)
	removed := 0

	removeOld := func(dir string, include func(string) bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if include != nil && !include(name) {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, name)); err == nil {
				removed++
			}
		}
	}

	removeOld(logDir, func(name string) bool { return !isLiveDatabaseFile(name) })
	removeOld(filepath.Join(logDir, LegacyDirName), nil)
	return removed
}

// isLiveDatabaseFile reconhece os arquivos que o SQLite mantém (ou mantém
// transitoriamente) e que nunca devem ser apagados pela retenção.
func isLiveDatabaseFile(name string) bool {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".db"),
		strings.HasSuffix(lower, ".db-journal"),
		strings.HasSuffix(lower, ".db-wal"),
		strings.HasSuffix(lower, ".db-shm"):
		return true
	}
	return false
}
