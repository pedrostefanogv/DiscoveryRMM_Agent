package app

// GetLogs returns the accumulated command log lines.
func (a *App) GetLogs() []string {
	return a.logs.getAll()
}

// GetLogCount returns the total number of buffered log lines.
func (a *App) GetLogCount() int {
	return a.logs.count()
}

// ExportLogs returns log content formatted for file export, optionally filtered.
func (a *App) ExportLogs(filterOrigin string) string {
	return a.logs.exportFormatted(filterOrigin)
}

// ClearLogs empties the log buffer.
func (a *App) ClearLogs() {
	a.logs.clear()
}
