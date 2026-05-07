package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (db *DB) CacheGet(key string) ([]byte, error) {
	if db == nil || db.conn == nil {
		return nil, fmt.Errorf("database indisponivel")
	}

	var value string
	var expiresAt sql.NullInt64

	err := db.conn.QueryRow(
		"SELECT value, expires_at FROM cache WHERE key = ?",
		key,
	).Scan(&value, &expiresAt)

	if err == sql.ErrNoRows {
		return nil, nil // Não encontrado
	}
	if err != nil {
		return nil, err
	}

	// Verificar expiração
	if expiresAt.Valid && expiresAt.Int64 < time.Now().Unix() {
		db.conn.Exec("DELETE FROM cache WHERE key = ?", key)
		return nil, nil
	}

	return []byte(value), nil
}

// CacheSet armazena um valor no cache com TTL opcional (0 = sem expiração)
func (db *DB) CacheSet(key string, value []byte, ttl time.Duration) error {
	if db == nil || db.conn == nil {
		return fmt.Errorf("database indisponivel")
	}

	var expiresAt sql.NullInt64
	if ttl > 0 {
		expiresAt.Valid = true
		expiresAt.Int64 = time.Now().Add(ttl).Unix()
	}

	_, err := db.conn.Exec(
		"INSERT OR REPLACE INTO cache (key, value, expires_at) VALUES (?, ?, ?)",
		key, string(value), expiresAt,
	)
	return err
}

// CacheDelete remove um valor do cache
func (db *DB) CacheDelete(key string) error {
	if db == nil || db.conn == nil {
		return fmt.Errorf("database indisponivel")
	}

	_, err := db.conn.Exec("DELETE FROM cache WHERE key = ?", key)
	return err
}

// CacheSetJSON armazena um objeto JSON no cache
func (db *DB) CacheSetJSON(key string, obj interface{}, ttl time.Duration) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return db.CacheSet(key, data, ttl)
}

// CacheGetJSON recupera um objeto JSON do cache
func (db *DB) CacheGetJSON(key string, target interface{}) (bool, error) {
	data, err := db.CacheGet(key)
	if err != nil {
		return false, err
	}
	if data == nil {
		return false, nil // Não encontrado
	}
	return true, json.Unmarshal(data, target)
}

// MemoryNote representa uma anotação/memória local persistida.
type MemoryNote struct {
	ID        int64     `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ListMemoryNotes retorna todas as notas ordenadas pela mais recente.
func (db *DB) ListMemoryNotes() ([]MemoryNote, error) {
	rows, err := db.conn.Query("SELECT id, content, created_at, updated_at FROM memory_notes ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MemoryNote, 0)
	for rows.Next() {
		var n MemoryNote
		var createdAt, updatedAt int64
		if err := rows.Scan(&n.ID, &n.Content, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		n.CreatedAt = time.Unix(createdAt, 0)
		n.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, n)
	}
	return out, rows.Err()
}

// CreateMemoryNote insere uma nova anotação e retorna o registro criado.
func (db *DB) CreateMemoryNote(content string) (MemoryNote, error) {
	now := time.Now().Unix()
	res, err := db.conn.Exec(
		"INSERT INTO memory_notes (content, created_at, updated_at) VALUES (?, ?, ?)",
		content, now, now,
	)
	if err != nil {
		return MemoryNote{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return MemoryNote{}, err
	}
	return MemoryNote{
		ID:        id,
		Content:   content,
		CreatedAt: time.Unix(now, 0),
		UpdatedAt: time.Unix(now, 0),
	}, nil
}

// DeleteMemoryNote remove uma anotação pelo ID.
func (db *DB) DeleteMemoryNote(id int64) error {
	_, err := db.conn.Exec("DELETE FROM memory_notes WHERE id = ?", id)
	return err
}

// SaveInventorySnapshot salva um snapshot de inventário para histórico
func (db *DB) SaveInventorySnapshot(agentID string, hardwareJSON, softwareJSON []byte) error {
	_, err := db.conn.Exec(
		"INSERT INTO inventory_history (agent_id, collected_at, hardware_json, software_json) VALUES (?, ?, ?, ?)",
		agentID, time.Now().Unix(), string(hardwareJSON), string(softwareJSON),
	)
	return err
}

// GetLatestInventory recupera o último snapshot de inventário para um agente
func (db *DB) GetLatestInventory(agentID string) (hardwareJSON, softwareJSON []byte, collectedAt time.Time, err error) {
	var hw, sw string
	var ts int64

	err = db.conn.QueryRow(
		"SELECT hardware_json, software_json, collected_at FROM inventory_history WHERE agent_id = ? ORDER BY collected_at DESC LIMIT 1",
		agentID,
	).Scan(&hw, &sw, &ts)

	if err == sql.ErrNoRows {
		return nil, nil, time.Time{}, nil
	}
	if err != nil {
		return nil, nil, time.Time{}, err
	}

	return []byte(hw), []byte(sw), time.Unix(ts, 0), nil
}

// CleanOldInventory remove snapshots de inventário mais antigos que X dias
func (db *DB) CleanOldInventory(days int) error {
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	_, err := db.conn.Exec("DELETE FROM inventory_history WHERE collected_at < ?", cutoff)
	return err
}

// GetLastSyncTime retorna quando foi o último sync bem-sucedido para uma chave
func (db *DB) GetLastSyncTime(key string) (time.Time, error) {
	var ts int64
	err := db.conn.QueryRow(
		"SELECT last_sync_at FROM sync_control WHERE key = ?",
		key,
	).Scan(&ts)

	if err == sql.ErrNoRows {
		return time.Time{}, nil // Nunca sincronizado
	}
	if err != nil {
		return time.Time{}, err
	}

	return time.Unix(ts, 0), nil
}

// UpdateLastSyncTime atualiza o timestamp do último sync bem-sucedido
func (db *DB) UpdateLastSyncTime(key string, metadata string) error {
	_, err := db.conn.Exec(
		"INSERT OR REPLACE INTO sync_control (key, last_sync_at, metadata) VALUES (?, ?, ?)",
		key, time.Now().Unix(), metadata,
	)
	return err
}

// ShouldSyncInventory verifica se deve enviar inventário para API com base em:
// - Foi modificado desde último sync OU
// - Passou mais de 24h desde último sync
func (db *DB) ShouldSyncInventory(agentID string, currentHardware, currentSoftware []byte) (bool, string, error) {
	// 1. Verificar quando foi o último sync
	lastSync, err := db.GetLastSyncTime("inventory_sync:" + agentID)
	if err != nil {
		return false, "", err
	}

	// Se nunca sincronizou, deve sincronizar
	if lastSync.IsZero() {
		return true, "primeiro sync", nil
	}

	// 2. Verificar se passou 24h
	if time.Since(lastSync) >= 24*time.Hour {
		return true, "passou 24h desde ultimo sync", nil
	}

	// 3. Comparar com último snapshot
	lastHW, lastSW, _, err := db.GetLatestInventory(agentID)
	if err != nil {
		return false, "", err
	}

	// Se não há snapshot anterior, deve sincronizar
	if lastHW == nil && lastSW == nil {
		return true, "sem snapshot anterior", nil
	}

	// 4. Detectar mudanças significativas
	hwChanged := string(lastHW) != string(currentHardware)
	swChanged := inventorySoftwareChanged(lastSW, currentSoftware)

	if hwChanged {
		return true, "hardware modificado", nil
	}
	if swChanged {
		return true, "software modificado significativamente", nil
	}

	return false, "sem mudancas significativas", nil
}

// inventorySoftwareChanged compara listas de software e retorna true se houver
// mudanças significativas (ignorando pequenas variações de versão patch)
func inventorySoftwareChanged(oldJSON, newJSON []byte) bool {
	if len(oldJSON) == 0 && len(newJSON) == 0 {
		return false
	}
	if len(oldJSON) == 0 || len(newJSON) == 0 {
		return true
	}

	oldNorm, oldErr := normalizeInventorySoftwareJSON(oldJSON)
	newNorm, newErr := normalizeInventorySoftwareJSON(newJSON)

	// Fallback seguro para formato inesperado.
	if oldErr != nil || newErr != nil {
		return string(oldJSON) != string(newJSON)
	}

	if len(oldNorm) != len(newNorm) {
		return true
	}

	for i := range oldNorm {
		if oldNorm[i] != newNorm[i] {
			return true
		}
	}
	return false
}

func normalizeInventorySoftwareJSON(raw []byte) ([]string, error) {
	var payload struct {
		Software []struct {
			Name          string `json:"name"`
			Version       string `json:"version"`
			Publisher     string `json:"publisher"`
			InstallID     string `json:"installId"`
			Source        string `json:"source"`
			InstallDate   string `json:"installDate"`
			InstallSource string `json:"installSource"`
		} `json:"software"`
	}

	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	items := make([]string, 0, len(payload.Software))
	for _, s := range payload.Software {
		key := strings.ToLower(strings.TrimSpace(s.Name)) + "|" +
			strings.TrimSpace(s.Version) + "|" +
			strings.ToLower(strings.TrimSpace(s.Publisher)) + "|" +
			strings.TrimSpace(s.InstallID) + "|" +
			strings.ToLower(strings.TrimSpace(s.Source)) + "|" +
			strings.ToLower(strings.TrimSpace(s.InstallSource))
		if strings.TrimSpace(key) == "|||||" {
			continue
		}
		items = append(items, key)
	}
	sort.Strings(items)
	return items, nil
}
