package app

// support_companion.go — bridges da UI companion para Suporte e Base de
// Conhecimento (PLANO_SEPARACAO_SERVICO_UI.md, decisão D3).
//
// A UI companion NÃO abre o SQLite do serviço: em modo companion os métodos
// Wails-bound roteiam o RPC ao serviço, que é o dono do DB, da identidade
// durável (agent_info_stale) e dos snapshots offline (tickets:backup:* /
// knowledge:backup:*). Standalone e modo serviço executam o caminho local.
//
// Sem esta ponte os dois caches offline ficavam inalcançáveis na tela: a UI
// só tinha o caminho HTTP e falhava com o servidor fora do ar.

import (
	"context"
	"encoding/json"
	"time"
)

const (
	// supportRPCReadTimeout cobre o caminho offline do serviço (identidade
	// durável ~4s + snapshot: chamados ~8s, knowledge ~6s) com folga para o
	// fetch frio online da base de conhecimento. O serviço despacha RPC em
	// goroutine, então um handler longo não trava o status da UI.
	supportRPCReadTimeout = 30 * time.Second
	// supportRPCWriteTimeout cobre mutações (create/close/comment): o POST tem
	// retry idempotente de até 2×15s no serviço.
	supportRPCWriteTimeout = 30 * time.Second
)

// supportCompanionCall executa um RPC de suporte no serviço.
// handled=false quando não há IPC client (modo serviço ou standalone) — o
// caller usa o caminho local.
func (a *App) supportCompanionCall(method string, payload map[string]any, timeout time.Duration) (json.RawMessage, error, bool) {
	if a == nil || a.ipcClient == nil {
		return nil, nil, false
	}
	base := a.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, timeout)
	defer cancel()

	resp, err := a.ipcClient.Request(ctx, method, payload)
	if err != nil {
		return nil, err, true
	}
	data, _ := resp["data"].(map[string]any)
	raw, mErr := json.Marshal(data["result"])
	if mErr != nil {
		return nil, mErr, true
	}
	return raw, nil, true
}

// supportCompanionOrLocal tenta o serviço em modo companion e cai no caminho
// local caso contrário. Erros do RPC são propagados (a UI companion não tem DB
// para um fallback local melhor que o erro real do serviço).
func supportCompanionOrLocal[T any](a *App, method string, payload map[string]any, timeout time.Duration, local func() (T, error)) (T, error) {
	if a != nil && a.ipcClient != nil {
		raw, err, handled := a.supportCompanionCall(method, payload, timeout)
		if handled {
			var zero T
			if err != nil {
				return zero, err
			}
			var out T
			if uErr := json.Unmarshal(raw, &out); uErr != nil {
				return zero, uErr
			}
			return out, nil
		}
	}
	return local()
}

// supportReadOrLocal é o atalho para consultas (somente leitura).
func supportReadOrLocal[T any](a *App, method string, payload map[string]any, local func() (T, error)) (T, error) {
	return supportCompanionOrLocal(a, method, payload, supportRPCReadTimeout, local)
}

// supportWriteOrLocal é o atalho para mutações.
func supportWriteOrLocal[T any](a *App, method string, payload map[string]any, local func() (T, error)) (T, error) {
	return supportCompanionOrLocal(a, method, payload, supportRPCWriteTimeout, local)
}
