package app

// companion_bridges.go — bridges da UI companion que consultam o core do
// serviço via IPC RPC (PLANO_SEPARACAO_SERVICO_UI.md §0.4 — RPCs pendentes).
//
// Padrão: em companion mode (ipcClient != nil) o método roteia ao serviço;
// retorna (nil, false) quando standalone — o caller executa o caminho local.

import (
	"encoding/json"

	"discovery/app/core/models"
)

// getTailLogsCompanion busca as últimas linhas de log do serviço.
// Retorna (nil, false) quando standalone.
func (a *App) getTailLogsCompanion(count int) ([]string, bool) {
	resp, ok := a.ipcRequest("logs:tail", map[string]any{"count": float64(count)})
	if !ok {
		return nil, false
	}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["lines"])
	if err != nil {
		return nil, false
	}
	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		return nil, false
	}
	return lines, true
}

// getAutomationStateCompanion busca o estado de automação do serviço.
// Retorna (nil, false) quando standalone.
func (a *App) getAutomationStateCompanion() (json.RawMessage, bool) {
	resp, ok := a.ipcRequest("automation:state", nil)
	if !ok {
		return nil, false
	}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["state"])
	if err != nil {
		return nil, false
	}
	return json.RawMessage(raw), true
}

// getPendingUpdatesCompanion roda a checagem de updates NO serviço (D2).
// Retorna (nil, false) quando standalone.
func (a *App) getPendingUpdatesCompanion() ([]models.UpgradeItem, bool) {
	resp, ok := a.ipcRequest("updates:scan", nil)
	if !ok {
		return nil, false
	}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["updates"])
	if err != nil {
		return nil, false
	}
	var items []models.UpgradeItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	return items, true
}
