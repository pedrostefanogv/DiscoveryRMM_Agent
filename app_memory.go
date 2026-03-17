package main

import (
	"fmt"
	"strings"

	"discovery/internal/database"
)

// GetLocalMemories retorna as memorias/anotacoes locais persistidas.
// Esta API é exposta via Wails e MCP.
func (a *App) GetLocalMemories() ([]database.MemoryNote, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database nao inicializado")
	}
	return a.db.ListMemoryNotes()
}

// AddLocalMemory cria uma nova anotacao local.
func (a *App) AddLocalMemory(content string) (database.MemoryNote, error) {
	if a.db == nil {
		return database.MemoryNote{}, fmt.Errorf("database nao inicializado")
	}
	if strings.TrimSpace(content) == "" {
		return database.MemoryNote{}, fmt.Errorf("conteudo da anotacao nao pode ser vazio")
	}
	return a.db.CreateMemoryNote(content)
}

// DeleteLocalMemory remove uma nota pelo seu ID.
func (a *App) DeleteLocalMemory(id int64) error {
	if a.db == nil {
		return fmt.Errorf("database nao inicializado")
	}
	return a.db.DeleteMemoryNote(id)
}
