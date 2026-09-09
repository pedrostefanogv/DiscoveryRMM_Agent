package app

// companion_rpc.go — bridge da UI companion para o core do serviço via IPC
// RPC (PLANO_SEPARACAO_SERVICO_UI.md, Fase C).
//
// Padrão de uso nos bridges: quando a App está em modo companion
// (ipcClient != nil), os métodos que consultam o DB local do core são
// roteados ao serviço via ipcRequest; no standalone, executam localmente
// (decisão D3 — DB único, dono = serviço; a UI companion nunca abre o SQLite
// do serviço).

import (
	"context"
	"encoding/json"
	"time"

	"discovery/app/core/database"
)

// ipcRequest executa um RPC ao serviço quando em modo companion.
// ok=false quando não há IPC client (standalone) — o caller cai no caminho local.
func (a *App) ipcRequest(method string, payload map[string]any) (map[string]any, bool) {
	if a == nil || a.ipcClient == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	resp, err := a.ipcClient.Request(ctx, method, payload)
	if err != nil {
		a.Logs.Append("[ipc-rpc] " + method + ": " + err.Error())
		return nil, false
	}
	return resp, true
}

// GetLocalMemoriesCompanion consulta Memory Notes via IPC quando companion.
// Retorna (nil, false) quando standalone (usar caminho local).
func (a *App) getLocalMemoriesCompanion() ([]database.MemoryNote, bool) {
	resp, ok := a.ipcRequest("memory:list", nil)
	if !ok {
		return nil, false
	}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["notes"])
	if err != nil {
		return nil, false
	}
	var notes []database.MemoryNote
	if err := json.Unmarshal(raw, &notes); err != nil {
		return nil, false
	}
	return notes, true
}
