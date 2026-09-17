//go:build windows

package app

// Remote session no modo companion (PLANO_AGENT_SERVICE_SYSTEM.md §2.2).
//
// A sessão 0 do serviço SYSTEM não tem desktop interativo: a captura de tela
// não produz frames e SendInput é bloqueado por UIPI ("Acesso negado", errno=5).
// Por isso o serviço encaminha os comandos de remote session via IPC
// (IPCMsgRemoteSession) para a UI companion, que os executa na sessão do
// usuário. O streaming de frames/input acontece por uma conexão NATS dedicada
// aberta pela própria UI (o agentConn do serviço continua sendo o dono do
// core: heartbeat, comandos, sync, P2P etc.).

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/core/agentconn"
	"discovery/app/netutil"
)

// companionNats guarda a conexão NATS dedicada de streaming da UI companion.
// Mutex porque o handler IPC pode disparar de múltiplas conexões/goroutines.
var companionNats struct {
	mu      sync.Mutex
	nc      *nats.Conn
	started bool
}

// handleCompanionRemoteSession processa um comando de remote session recebido
// via IPC do serviço, garantindo que o remoteSessionMgr local tenha uma
// conexão NATS configurada (aberta sob demanda, uma única vez).
func (a *App) handleCompanionRemoteSession(payload map[string]any) {
	if a == nil || a.RemoteSessionMgr == nil {
		return
	}
	if err := a.ensureCompanionNats(); err != nil {
		a.Logs.Append("[remote-session][companion] sem NATS de streaming: " + err.Error())
		return
	}
	a.Logs.Append("[remote-session][companion] executando comando na sessão do usuário")
	parsedPayload := parseAnyMap(payload)
	if parsedPayload == nil {
		a.Logs.Append("[remote-session][companion] payload inválido")
		return
	}
	handled, errMsg := a.RemoteSessionMgr.HandleCommand(a.ctx, parsedPayload)
	if handled && errMsg != "" {
		a.Logs.Append("[remote-session][companion] mensagem: " + errMsg)
	}
}

// ensureCompanionNats abre (uma vez) a conexão NATS dedicada da UI companion
// para o streaming de remote session, injetando-a no remoteSessionMgr local.
func (a *App) ensureCompanionNats() error {
	if a.RemoteSessionMgr == nil {
		return fmt.Errorf("remoteSessionMgr não inicializado")
	}
	companionNats.mu.Lock()
	defer companionNats.mu.Unlock()
	if companionNats.started && companionNats.nc != nil && companionNats.nc.Status() == nats.CONNECTED {
		return nil
	}

	cfg := a.GetDebugConfig()
	agentCfg := a.GetAgentConfiguration()

	agentID := strings.TrimSpace(cfg.AgentID)
	clientID := strings.TrimSpace(agentCfg.ClientID)
	siteID := strings.TrimSpace(agentCfg.SiteID)
	if clientID == "" || siteID == "" {
		if inst, _, err := loadInstallerConfig(); err == nil {
			if clientID == "" {
				clientID = strings.TrimSpace(inst.ClientID)
			}
			if siteID == "" {
				siteID = strings.TrimSpace(inst.SiteID)
			}
		}
	}
	if agentID == "" || clientID == "" || siteID == "" {
		return fmt.Errorf("identidade incompleta (agentId/clientId/siteId)")
	}

	token, err := netutil.NormalizeAgentToken(cfg.AuthToken)
	if err != nil {
		return fmt.Errorf("token inválido: %w", err)
	}

	// Tenta WSS primeiro (mesma ordem de preferência do agentconn em redes
	// onde a porta 4222 é bloqueada) e depois o NATS nativo derivado da API.
	//
	// FIX 2026-09-16 (mesma família do fix do worker): os candidatos iam crus
	// ao nats.Connect — wss sem porta explícita disca "host:0" (o nats.go não
	// aplica porta padrão para ws/wss) e o path do websocket precisa ir via
	// nats.ProxyPath ("invalid websocket connection" sem isso). A normalização
	// agora usa agentconn.NormalizeClientEndpoint, igual ao worker.
	var candidates []string
	if wss := strings.TrimSpace(cfg.NatsWsServer); wss != "" {
		candidates = append(candidates, wss)
	}
	if host := extractAPIHost(cfg.ApiServer); host != "" {
		candidates = append(candidates, "wss://"+host+"/nats/")
	}
	if nat := strings.TrimSpace(cfg.NatsServer); nat != "" {
		candidates = append(candidates, nat)
	}
	if host := extractAPIHost(cfg.ApiServer); host != "" {
		candidates = append(candidates, "nats://"+host+":4222")
	}

	type natsAttempt struct {
		url       string
		proxyPath string
	}
	var attempts []natsAttempt
	seen := map[string]struct{}{}
	for _, raw := range candidates {
		ep, err := agentconn.NormalizeClientEndpoint(raw)
		if err != nil {
			a.Logs.Append("[remote-session][companion] endpoint NATS ignorado (" + raw + "): " + err.Error())
			continue
		}
		if _, dup := seen[ep.URL]; dup {
			continue
		}
		seen[ep.URL] = struct{}{}
		attempts = append(attempts, natsAttempt{url: ep.URL, proxyPath: ep.ProxyPath})
	}

	var lastErr error
	for _, at := range attempts {
		opts := []nats.Option{
			nats.Name("discovery-companion-stream-" + agentID),
			nats.Token(token),
			nats.Timeout(8 * time.Second),
			nats.ReconnectWait(10 * time.Second),
			nats.MaxReconnects(-1),
		}
		if at.proxyPath != "" {
			opts = append(opts, nats.ProxyPath(at.proxyPath))
		}
		nc, err := nats.Connect(at.url, opts...)
		if err != nil {
			lastErr = err
			continue
		}
		companionNats.nc = nc
		companionNats.started = true
		a.RemoteSessionMgr.SetNatsConn(nc, clientID, siteID, agentID)
		a.Logs.Append("[remote-session][companion] NATS de streaming conectado em " + at.url)
		return nil
	}
	if lastErr != nil {
		return fmt.Errorf("nenhum endpoint NATS disponível: %w", lastErr)
	}
	return fmt.Errorf("nenhum endpoint NATS configurado")
}

// extractAPIHost extrai host[:port] de uma URL/hostname.
func extractAPIHost(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	server = strings.TrimPrefix(server, "https://")
	server = strings.TrimPrefix(server, "http://")
	if i := strings.IndexAny(server, "/?#"); i >= 0 {
		server = server[:i]
	}
	return server
}
