package memory

import (
	"fmt"
	"strings"

	"discovery/app/core/database"
)

// Deps são as dependências injetadas no MemoryService.
type Deps struct {
	// DB retorna o banco de dados (pode ser nil se não inicializado).
	DB func() *database.DB
}

// Service encapsula a lógica de memórias/anotações locais.
type Service struct {
	db func() *database.DB
}

// New cria um MemoryService.
func New(deps Deps) *Service {
	return &Service{db: deps.DB}
}

// GetLocalMemories retorna as memorias/anotacoes locais persistidas.
func (s *Service) GetLocalMemories() ([]database.MemoryNote, error) {
	db := s.db()
	if db == nil {
		return nil, fmt.Errorf("database não inicializado")
	}
	return db.ListMemoryNotes()
}

// AddLocalMemory cria uma nova anotacao local.
func (s *Service) AddLocalMemory(content string) (database.MemoryNote, error) {
	db := s.db()
	if db == nil {
		return database.MemoryNote{}, fmt.Errorf("database nao inicializado")
	}
	if strings.TrimSpace(content) == "" {
		return database.MemoryNote{}, fmt.Errorf("conteudo da anotacao nao pode ser vazio")
	}
	return db.CreateMemoryNote(content)
}

// DeleteLocalMemory remove uma nota pelo seu ID.
func (s *Service) DeleteLocalMemory(id int64) error {
	db := s.db()
	if db == nil {
		return fmt.Errorf("database nao inicializado")
	}
	return db.DeleteMemoryNote(id)
}
