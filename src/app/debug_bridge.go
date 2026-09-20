package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (a *App) GetDebugConfig() DebugConfig {
	if a == nil || a.DebugSvc == nil {
		return DebugConfig{}
	}
	return a.DebugSvc.GetConfig()
}

func (a *App) SetDebugConfig(cfg DebugConfig) error {
	if err := a.requireDebugSvc(); err != nil {
		return err
	}
	// Companion mode: o core roda NO serviço — a config é aplicada PRIMEIRO
	// lá (validação + persistência + reload da conexão do core) e só então
	// adotada na cópia local da UI. Assim uma rejeição do serviço (ex.:
	// agentId inválido para NATS) chega à UI como erro, em vez de aplicar
	// localmente e deixar os dois processos divergentes até reiniciar.
	// Standalone (sem IPC) aplica localmente como antes.
	if a.ipcClient != nil {
		if raw, err := json.Marshal(cfg); err == nil {
			ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
			resp, reqErr := a.ipcClient.Request(ctx, "debug:set", map[string]any{"config": json.RawMessage(raw)})
			cancel()
			if reqErr == nil {
				a.DebugSvc.AdoptExternalConfig(cfg)
				return nil
			}
			if resp != nil {
				// O serviço respondeu com erro (validação) — propaga para a UI.
				if errMsg, _ := resp["error"].(string); strings.TrimSpace(errMsg) != "" {
					return fmt.Errorf("%s", errMsg)
				}
			}
			// Serviço inacessível (sem resposta): cai no caminho local abaixo
			// para não bloquear o usuário quando o core está fora do ar.
			a.Logs.Append("[debug] serviço ausente ao propagar config (debug:set): " + reqErr.Error())
		}
	}
	return a.DebugSvc.SetConfig(cfg)
}

func (a *App) TestDebugConnection(cfg DebugConfig) (string, error) {
	if err := a.requireDebugSvc(); err != nil {
		return "", err
	}
	return a.DebugSvc.TestConnection(cfg)
}

func (a *App) GetRealtimeStatus() (RealtimeStatus, error) {
	if err := a.requireDebugSvc(); err != nil {
		return RealtimeStatus{}, err
	}
	return a.DebugSvc.GetRealtimeStatus()
}

func (a *App) GetAgentStatus() AgentStatus {
	if a == nil {
		return AgentStatus{}
	}
	// Companion mode: o core (agentConn) roda no serviço — o agentConn local
	// nunca conecta nesta UI, então GetAgentStatus() local retornaria sempre
	// offline (tray cinza/status offline mesmo com heartbeat no site). Usa o
	// último snapshot recebido via IPC (agent:status_snapshot) quando houver.
	if a.ipcClient != nil {
		a.companionStatusMu.Lock()
		snap := a.companionStatus
		a.companionStatusMu.Unlock()
		if snap != nil {
			return *snap
		}
		// Companion ainda sem snapshot (UI recém-aberta ou serviço ocupado):
		// o fallback local seria sempre offline aqui (o agentConn local não
		// roda na UI) e o indicador mostrava Offline até o primeiro evento do
		// serviço. Deixa o motivo explícito para diagnóstico nos logs.
		st := AgentStatus{}
		if a.DebugSvc != nil {
			st = a.resolveAgentConnectivity(a.DebugSvc.GetAgentStatus())
		}
		if st.OnlineReason == "" {
			st.OnlineReason = "companion: aguardando primeiro snapshot do serviço via IPC"
		}
		return st
	}
	if a.DebugSvc == nil {
		return AgentStatus{}
	}
	return a.resolveAgentConnectivity(a.DebugSvc.GetAgentStatus())
}
