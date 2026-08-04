package app

import "discovery/app/core/database"

// GetLocalMemories retorna as memorias/anotacoes locais persistidas.
// Esta API é exposta via Wails e MCP.
func (a *App) GetLocalMemories() ([]database.MemoryNote, error) {
	return a.memorySvc.GetLocalMemories()
}

// AddLocalMemory cria uma nova anotacao local.
func (a *App) AddLocalMemory(content string) (database.MemoryNote, error) {
	return a.memorySvc.AddLocalMemory(content)
}

// DeleteLocalMemory remove uma nota pelo seu ID.
func (a *App) DeleteLocalMemory(id int64) error {
	return a.memorySvc.DeleteLocalMemory(id)
}
