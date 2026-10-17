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
	// Companion mode: o core roda NO SERVIÇO — que é o dono único da config
	// persistida. A UI envia o patch via RPC; o serviço valida, grava de forma
	// atômica e recarrega o agentConn. NÃO existe fallback de escrita local:
	// se o RPC falhar, devolvemos erro explícito (a UI mostra a mensagem) em
	// vez de gravar um debug_config.json divergente do serviço.
	// Standalone (sem IPC) aplica/persiste localmente como antes.
	if a.ipcClient != nil {
		raw, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("falha ao serializar config de debug: %w", err)
		}
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		resp, reqErr := a.ipcClient.Request(ctx, "debug:set", map[string]any{"config": json.RawMessage(raw)})
		cancel()
		if reqErr != nil {
			a.Logs.Append("[debug] falha ao propagar config (debug:set): " + reqErr.Error())
			return fmt.Errorf("não foi possível salvar a configuração no serviço: %w", reqErr)
		}
		if resp == nil {
			return fmt.Errorf("serviço não respondeu ao salvar a configuração")
		}
		if ok, _ := resp["ok"].(bool); !ok {
			errMsg, _ := resp["error"].(string)
			if strings.TrimSpace(errMsg) == "" {
				errMsg = "o serviço rejeitou a configuração"
			}
			return fmt.Errorf("%s", errMsg)
		}
		// Sucesso: adota a config efetiva devolvida pelo serviço (já
		// normalizada); se a resposta não trouxer, usa a config enviada.
		if effective, ok := decodeDebugConfigFromRPCResponse(resp); ok {
			a.DebugSvc.AdoptExternalConfig(effective)
			return nil
		}
		a.DebugSvc.AdoptExternalConfig(cfg)
		return nil
	}
	return a.DebugSvc.SetConfig(cfg)
}

// decodeDebugConfigFromRPCResponse extrai a config efetiva de respostas RPC
// (debug:set devolve data.config; config:set devolve os campos direto em data).
func decodeDebugConfigFromRPCResponse(resp map[string]any) (DebugConfig, bool) {
	data, _ := resp["data"].(map[string]any)
	if data == nil {
		return DebugConfig{}, false
	}
	rawBody := data["config"]
	if rawBody == nil {
		rawBody = data
	}
	raw, err := json.Marshal(rawBody)
	if err != nil {
		return DebugConfig{}, false
	}
	var cfg DebugConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return DebugConfig{}, false
	}
	return cfg, true
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
	return a.applyAPIHealth(a.getAgentStatusRaw())
}

// getAgentStatusRaw é o status antes do ajuste de saúde da API (NATS + pong).
func (a *App) getAgentStatusRaw() AgentStatus {
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
