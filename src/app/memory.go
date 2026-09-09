package app

import (
	"encoding/json"

	"discovery/app/core/database"
)

// GetLocalMemories retorna as memorias/anotacoes locais persistidas.
// Esta API é exposta via Wails e MCP.
// Em modo companion, consulta o serviço via IPC (DB único — decisão D3).
func (a *App) GetLocalMemories() ([]database.MemoryNote, error) {
	if notes, ok := a.getLocalMemoriesCompanion(); ok {
		return notes, nil
	}
	return a.MemorySvc.GetLocalMemories()
}

// AddLocalMemory cria uma nova anotacao local.
// Em modo companion, executa no serviço via IPC (decisão D3).
func (a *App) AddLocalMemory(content string) (database.MemoryNote, error) {
	if resp, ok := a.ipcRequest("memory:add", map[string]any{"content": content}); ok {
		data, _ := resp["data"].(map[string]any)
		raw, _ := json.Marshal(data["note"])
		var note database.MemoryNote
		if json.Unmarshal(raw, &note) == nil {
			return note, nil
		}
	}
	return a.MemorySvc.AddLocalMemory(content)
}

// DeleteLocalMemory remove uma nota pelo seu ID.
// Em modo companion, executa no serviço via IPC (decisão D3).
func (a *App) DeleteLocalMemory(id int64) error {
	if resp, ok := a.ipcRequest("memory:delete", map[string]any{"id": float64(id)}); ok {
		if okResp, _ := resp["ok"].(bool); okResp {
			return nil
		}
	}
	return a.MemorySvc.DeleteLocalMemory(id)
}
