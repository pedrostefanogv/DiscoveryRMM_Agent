package app

import (
	"log"
	"path/filepath"
	"sync"

	"discovery/app/core/buildinfo"
	"discovery/app/core/logger"
	"discovery/app/core/logstore"
	"discovery/app/core/platform"
)

// Retenção e teto de tamanho dos bancos de log
// (RELATORIO_VIABILIDADE_LOG_SQLITE.md §8: 7 dias, startup + 24 h, teto 128 MB).
const (
	LogRetention = logstore.DefaultRetention
	LogMaxBytes  = logstore.DefaultMaxBytes
)

var (
	processLogStoreMu sync.Mutex
	processLogStore   *logstore.Store
)

// ensureProcessLogStore abre o logs.db uma única vez por processo: o serviço
// abre antes de qualquer outra operação e o App reaproveita a mesma instância.
// Devolve nil quando o banco não pode ser aberto (o chamador cai para arquivo
// texto) — logging nunca é o ponto de falha do agente.
func ensureProcessLogStore(source, process string) *logstore.Store {
	processLogStoreMu.Lock()
	defer processLogStoreMu.Unlock()
	if processLogStore != nil {
		return processLogStore
	}
	if !logFilePersistenceEnabled() {
		return nil
	}
	dir := platform.LogDir()
	st, err := logstore.Open(logstore.Options{
		Path:      platform.LogDBPath(),
		Kind:      logstore.KindLogs,
		Source:    source,
		Process:   process,
		Retention: LogRetention,
		MaxBytes:  LogMaxBytes,
		LegacyDir: dir,
		OnError:   func(msg string) { log.Printf("[logstore] %s", msg) },
	})
	if err != nil {
		log.Printf("[startup] aviso: logs.db indisponível (%v) — mantendo log em arquivo", err)
		return nil
	}
	processLogStore = st

	// Sink do slog: cobre as linhas estruturadas (logger.Info/Error) que antes
	// iam para agent.log/agent-service.log.
	logger.SetStoreSink(logger.LogBufferAdapter(func(line string) {
		level, msg := logstore.ParseLevel(line)
		st.AppendLog(level, buildinfo.Revision(), msg)
	}))

	// Corte direto do modelo antigo (sem dual-write): só arquiva os arquivos de
	// texto quando o banco abriu, senão o fallback em uso seria movido.
	if moved := logstore.ArchiveLegacyLogs(dir); len(moved) > 0 {
		log.Printf("[startup] logs antigos movidos para %s: %v",
			filepath.Join(dir, logstore.LegacyDirName), moved)
	}
	// Purga no startup (idade + teto); a periódica roda a cada 24 h no Store.
	if err := st.Purge(); err != nil {
		log.Printf("[startup] aviso: purga inicial de logs: %v", err)
	}
	return st
}

// closeProcessLogStore fecha o banco do processo (idempotente).
func closeProcessLogStore() {
	processLogStoreMu.Lock()
	st := processLogStore
	processLogStore = nil
	processLogStoreMu.Unlock()
	if st != nil {
		_ = st.Close()
	}
}

// initLogPersistence liga a persistência de logs do App: banco unificado
// quando disponível; arquivo texto apenas como fallback.
func (a *App) initLogPersistence() {
	if !logFilePersistenceEnabled() {
		return
	}
	// A limpeza dos arquivos texto antigos (installer.log, fallback, _legacy/)
	// vale mesmo quando o banco não abre: é o único caso em que a retenção de
	// 7 dias dependeria do Store.
	logstore.PurgeStaleFiles(platform.LogDir(), LogRetention)

	source, process := "agent", "discovery-agent"
	if a.RuntimeFlags.ServiceMode {
		source, process = "service", "discovery-service"
	}
	if st := ensureProcessLogStore(source, process); st != nil {
		a.logStore = st
		a.Logs.EnableStore(st)
		a.Logs.Append("[startup] logs unificados em " + platform.LogDBPath())
		return
	}
	logPath := platform.LogFilePath()
	if a.RuntimeFlags.ServiceMode {
		logPath = platform.ServiceLogFilePath()
	}
	if logPath == "" {
		return
	}
	if err := a.Logs.EnableFilePersistence(logPath); err != nil {
		log.Printf("[startup] aviso: falha ao habilitar persistência de logs em arquivo: %v", err)
	} else {
		a.Logs.Append("[startup] persistência de logs habilitada em " + logPath)
	}
}
