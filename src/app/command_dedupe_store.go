package app

import (
	"sort"
	"strings"
	"sync"
	"time"

	"discovery/app/core/agentconn"
	"discovery/app/core/database"
)

// commandDedupeCacheKey é a chave (tabela cache do SQLite local) onde o dedupe
// de comandos do agent é persistido.
const commandDedupeCacheKey = "agentconn:command_dedupe"

// commandDedupeMaxEntries limita o crescimento do registro. Cada entrada vive no
// máximo 24h (mesmo TTL do dedupe em memória); o teto é folga para rajadas.
const commandDedupeMaxEntries = 500

// commandDedupeWire é o formato persistido de uma entrada de dedupe.
type commandDedupeWire struct {
	Key       string                   `json:"key"`
	ExpiresAt int64                    `json:"expiresAt"` // unix seconds
	Result    *commandDedupeResultWire `json:"result,omitempty"`
}

type commandDedupeResultWire struct {
	DispatchID   string `json:"dispatchId,omitempty"`
	CommandID    string `json:"commandId,omitempty"`
	AgentID      string `json:"agentId,omitempty"`
	ExitCode     int    `json:"exitCode"`
	Output       string `json:"output,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// commandDedupeStore mantém o dedupe de comandos em memória e espelha no cache
// do banco local, para sobreviver ao reboot no meio de um comando de energia.
type commandDedupeStore struct {
	mu      sync.Mutex
	loaded  bool
	entries map[string]commandDedupeWire
}

func newCommandDedupeStore() *commandDedupeStore {
	return &commandDedupeStore{entries: make(map[string]commandDedupeWire)}
}

// loadCommandDedupe entrega ao agentconn as entradas persistidas ainda válidas.
func (a *App) loadCommandDedupe() []agentconn.CommandDedupeEntry {
	if a == nil || a.commandDedupe == nil {
		return nil
	}

	store := a.commandDedupe
	db := a.DB()

	store.mu.Lock()
	defer store.mu.Unlock()

	store.loadLocked(db, a)

	now := time.Now()
	entries := make([]agentconn.CommandDedupeEntry, 0, len(store.entries))
	for _, wire := range store.entries {
		expiresAt := time.Unix(wire.ExpiresAt, 0)
		if !expiresAt.After(now) {
			continue
		}
		entry := agentconn.CommandDedupeEntry{Key: wire.Key, ExpiresAt: expiresAt}
		if wire.Result != nil {
			entry.Result = &agentconn.CommandResultSnapshot{
				DispatchID:   wire.Result.DispatchID,
				CommandID:    wire.Result.CommandID,
				AgentID:      wire.Result.AgentID,
				ExitCode:     wire.Result.ExitCode,
				Output:       wire.Result.Output,
				ErrorMessage: wire.Result.ErrorMessage,
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// persistCommandDedupe grava/renova uma entrada de dedupe no cache local.
func (a *App) persistCommandDedupe(entry agentconn.CommandDedupeEntry) error {
	if a == nil || a.commandDedupe == nil || strings.TrimSpace(entry.Key) == "" {
		return nil
	}

	store := a.commandDedupe
	db := a.DB()

	wire := commandDedupeWire{Key: entry.Key, ExpiresAt: entry.ExpiresAt.Unix()}
	if entry.Result != nil {
		wire.Result = &commandDedupeResultWire{
			DispatchID:   entry.Result.DispatchID,
			CommandID:    entry.Result.CommandID,
			AgentID:      entry.Result.AgentID,
			ExitCode:     entry.Result.ExitCode,
			Output:       entry.Result.Output,
			ErrorMessage: entry.Result.ErrorMessage,
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if db == nil {
		// Sem banco: mantém apenas em memória (comportamento anterior).
		store.entries[wire.Key] = wire
		return nil
	}

	store.loadLocked(db, a)
	store.entries[wire.Key] = wire
	store.pruneLocked(time.Now())

	list := make([]commandDedupeWire, 0, len(store.entries))
	for _, item := range store.entries {
		list = append(list, item)
	}
	// Ordem estável: o JSON do cache não muda por causa da iteração de map.
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })

	return db.CacheSetJSON(commandDedupeCacheKey, list, 0)
}

// loadLocked lê o cache local uma única vez. Chamado com o mutex travado.
func (s *commandDedupeStore) loadLocked(db *database.DB, a *App) {
	if s.loaded || db == nil {
		return
	}
	s.loaded = true

	var list []commandDedupeWire
	found, err := db.CacheGetJSON(commandDedupeCacheKey, &list)
	if err != nil {
		if a != nil {
			a.Logs.Append("[agent] dedupe de comandos: falha ao ler cache local: " + err.Error())
		}
		return
	}
	if !found {
		return
	}

	now := time.Now()
	for _, item := range list {
		if strings.TrimSpace(item.Key) == "" {
			continue
		}
		if !time.Unix(item.ExpiresAt, 0).After(now) {
			continue
		}
		s.entries[item.Key] = item
	}
}

// pruneLocked remove expirados e aplica o teto de entradas. Chamado com o mutex
// travado.
func (s *commandDedupeStore) pruneLocked(now time.Time) {
	for key, item := range s.entries {
		if !time.Unix(item.ExpiresAt, 0).After(now) {
			delete(s.entries, key)
		}
	}
	if len(s.entries) <= commandDedupeMaxEntries {
		return
	}

	keys := make([]string, 0, len(s.entries))
	for key := range s.entries {
		keys = append(keys, key)
	}
	// Descarta as entradas que expiram primeiro.
	sort.Slice(keys, func(i, j int) bool {
		return s.entries[keys[i]].ExpiresAt < s.entries[keys[j]].ExpiresAt
	})
	for _, key := range keys[:len(keys)-commandDedupeMaxEntries] {
		delete(s.entries, key)
	}
}
