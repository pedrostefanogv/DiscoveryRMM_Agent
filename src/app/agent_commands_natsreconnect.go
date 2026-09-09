package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"discovery/app/core/agentconn"
)

// natsReconnectPayload implementa o contrato do comando `nats.reconnect`
// (docs_planejamento/CONTRATO_AGENT_RECONNECT_COMMAND.md no backend).
// Enviado pelo servidor apÃ³s uma transferÃªncia de site do agent, no subject
// ANTIGO (Ãºnico que o agent ainda consegue receber antes de re-autenticar).
type natsReconnectPayload struct {
	Version     int    `json:"version"`
	Reason      string `json:"reason"`
	NewSiteID   string `json:"newSiteId"`
	NewClientID string `json:"newClientId"`
	Revision    string `json:"revision"`
}

// handleNatsReconnectCommand trata o comando `nats.reconnect`:
//  1. Re-busca a configuraÃ§Ã£o HTTP (novos siteId/clientId + policies resolvidas
//     site > cliente > servidor). O setAgentConfiguration detecta a divergÃªncia
//     de contexto NATS e reconecta sozinho (auth callout emite JWT do site novo).
//  2. Enfileira re-sync dos recursos dependentes de escopo (automationpolicy,
//     appstore, agentupdate) via coordinator, com dedupe por eventId.
//
// Defesa em profundidade: mesmo sem este comando, o ciclo de polling de
// configuration (~5 min) dispara o mesmo fluxo via setAgentConfiguration.
func (a *App) handleNatsReconnectCommand(parent context.Context, payload any) (bool, int, string, string) {
	p, err := parseNatsReconnectPayload(payload)
	if err != nil {
		// Contrato: payloads invÃ¡lidos/versÃµes desconhecidas sÃ£o ignorados
		// silenciosamente (backward compatibility).
		a.Logs.Append("[nats-reconnect] payload ignorado: " + err.Error())
		return true, 0, "nats.reconnect ignored: " + err.Error(), ""
	}
	if p.Version != 1 {
		a.Logs.Append(fmt.Sprintf("[nats-reconnect] versÃ£o nÃ£o suportada (%d) â€” ignorando", p.Version))
		return true, 0, fmt.Sprintf("nats.reconnect ignored: unsupported version %d", p.Version), ""
	}

	a.Logs.Append(fmt.Sprintf(
		"[nats-reconnect] comando recebido: reason=%s newSiteId=%s newClientId=%s revision=%s",
		p.Reason, p.NewSiteID, p.NewClientID, p.Revision))

	// 1) Recarrega a configuraÃ§Ã£o do servidor. Se site/cliente mudaram,
	// setAgentConfiguration chama agentConn.Reload() internamente.
	if err := a.refreshAgentConfiguration(parent); err != nil {
		return true, 1, "", fmt.Sprintf("nats.reconnect: falha ao recarregar configuraÃ§Ã£o: %v", err)
	}

	// 2) Re-sincroniza recursos dependentes do escopo.
	a.enqueueScopeResync(p.Revision)

	return true, 0, "nats.reconnect processed: configuration reloaded and resync enfileirado", ""
}

// enqueueScopeResync enfileira triggers de sync para os recursos cujo conteÃºdo
// depende do site/cliente do agent. Usa o mesmo caminho dos sync pings (dedupe
// por eventId + debounce do coordinator).
func (a *App) enqueueScopeResync(revision string) {
	if a.SyncSvc == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, resource := range []string{"automationpolicy", "appstore", "agentupdate"} {
		a.SyncSvc.HandlePing(agentconn.SyncPing{
			EventID:   "nats-reconnect:" + resource + ":" + now + ":" + uuid.NewString(),
			EventType: "sync.invalidated",
			Resource:  resource,
			Revision:  revision,
			Reason:    "agent-transferred",
		})
	}
}

func parseNatsReconnectPayload(raw any) (natsReconnectPayload, error) {
	var p natsReconnectPayload
	if raw == nil {
		return p, fmt.Errorf("payload ausente")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return p, fmt.Errorf("payload invÃ¡lido: %w", err)
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("payload invÃ¡lido: %w", err)
	}
	return p, nil
}
