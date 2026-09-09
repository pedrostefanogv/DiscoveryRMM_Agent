package app

// GetLogs returns the accumulated command log lines.
// Companion mode: os logs do CORE rodam no serviço — busca via RPC
// logs:tail (PLANO_SEPARACAO_SERVICO_UI.md §0.5). Standalone: buffer local.
func (a *App) GetLogs() []string {
	if lines, ok := a.getTailLogsCompanion(2000); ok {
		return lines
	}
	return a.Logs.GetAll()
}

func (a *App) GetLogCount() int {
	return a.Logs.Count()
}

// ExportLogs returns log content formatted for file export, optionally filtered.
func (a *App) ExportLogs(filterOrigin string) string {
	return a.Logs.ExportFormatted(filterOrigin)
}

// ClearLogs empties the log buffer.
func (a *App) ClearLogs() {
	a.Logs.Clear()
}
