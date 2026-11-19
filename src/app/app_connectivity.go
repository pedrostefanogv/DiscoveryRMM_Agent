package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/agentconfig"
	"discovery/app/core/agentconn"
	"discovery/app/core/inventory"
)

const (
	defaultHeartbeatIntervalSeconds = 15
	minHeartbeatIntervalSeconds     = 10
)

func (a *App) GetAgentConfiguration() agentconfig.AgentConfiguration {
	a.AgentConfigMu.RLock()
	cfg := a.AgentConfig
	a.AgentConfigMu.RUnlock()
	return cfg
}

func (a *App) isZeroTouchApprovalPending() bool {
	if a == nil {
		return false
	}
	return a.zeroTouchApprovalPending.Load()
}

func (a *App) setZeroTouchApprovalPending(pending bool) bool {
	if a == nil {
		return false
	}
	previous := a.zeroTouchApprovalPending.Load()
	if previous == pending {
		return false
	}
	a.zeroTouchApprovalPending.Store(pending)
	return true
}

func (a *App) featureEnabled(flag *bool) bool {
	if flag == nil {
		return true
	}
	return *flag
}

func heartbeatIntervalFromAgentConfig(agentCfg agentconfig.AgentConfiguration) int {
	if agentCfg.AgentHeartbeatIntervalSeconds != nil && *agentCfg.AgentHeartbeatIntervalSeconds > 0 {
		if *agentCfg.AgentHeartbeatIntervalSeconds < minHeartbeatIntervalSeconds {
			return minHeartbeatIntervalSeconds
		}
		return *agentCfg.AgentHeartbeatIntervalSeconds
	}
	return defaultHeartbeatIntervalSeconds
}

func (a *App) getHeartbeatMetrics() agentconn.AgentHeartbeatMetrics {
	hostname, _ := os.Hostname()
	metrics := agentconn.AgentHeartbeatMetrics{
		Hostname:              hostname,
		CpuPercent:            -1,
		MemoryPercent:         -1,
		DiskPercent:           -1,
		DiskReadPercent:       -1,
		DiskWritePercent:      -1,
		DiskResponseMs:        -1,
		CpuTemperatureCelsius: -1,
		UptimeSeconds:         int64(time.Since(a.startupTime).Seconds()),
		P2pPeers:              a.getKnownP2PPeers(),
	}

	// CollectHeartbeatMetrics usa APIs nativas no Windows (zero subprocessos)
	// e osquery socket no Linux/macOS. Todos os fallbacks de CPU/memória/disco
	// estão internalizados — não precisa de fallback adicional aqui.
	// Deriva do contexto de ciclo de vida quando disponível para que a coleta
	// aborte imediatamente durante o shutdown (não segura o encerramento).
	hbCtx := context.Background()
	if a.ctx != nil {
		hbCtx = a.ctx
	}
	ctx, cancel := context.WithTimeout(hbCtx, 5*time.Second)
	defer cancel()
	if m := inventory.CollectHeartbeatMetrics(ctx); m != nil {
		mergeHeartbeatMetrics(&metrics, m)
		metrics.P2pPeers = a.getKnownP2PPeers()
	}

	// Enriquecer com dados de endereçamento P2P (libp2p peer ID, addrs, port)
	if a.P2PCoord != nil {
		metrics.PeerID, metrics.Addrs, metrics.Port = a.P2PCoord.GetP2PAddressingInfo()
	}

	// UI online (PLANO_SEPARACAO_SERVICO_UI.md, Fase C2): no modo serviço,
	// informa ao servidor se há UI companion conectada via IPC. Fora do modo
	// serviço (UI standalone) o campo permanece nil (omitido no JSON).
	if a.RuntimeFlags.ServiceMode && a.ipcServer != nil {
		// UIAuthenticatedCount (e não ClientCount): o servidor precisa saber
		// quantas UIs AUTENTICADAS existem de fato. ClientCount conta conexões
		// do canal de leitura — incluindo probe/ferramenta não autenticada e
		// duas conexões da MESMA UI — o que fazia o console acreditar em
		// interface presente onde não havia ninguém (A3).
		count := a.ipcServer.UIAuthenticatedCount()
		online := count > 0
		metrics.UIOnline = &online
		metrics.UIOnlineCount = &count

		// Canal de gravação (P1.4/P1.7). É o estado REAL do listener admin deste
		// processo: quando ele não subiu, TODA mutação (chamado, comentário,
		// anexo, config, update) falha enquanto as leituras seguem ok — a
		// degradação silenciosa que o incidente de 2026-10-17 expôs. Este campo
		// é o que permite ao portal alertar "agente em somente-leitura".
		adminOk := a.ipcServer.AdminChannelActive()
		metrics.IpcAdminOk = &adminOk
		metrics.IpcAdminPipe = IPCPipeAdminName
	}

	return metrics
}

func mergeHeartbeatMetrics(dst *agentconn.AgentHeartbeatMetrics, src *agentconn.AgentHeartbeatMetrics) {
	if dst == nil || src == nil {
		return
	}

	if host := strings.TrimSpace(src.Hostname); host != "" {
		dst.Hostname = host
	}
	if src.CpuPercent >= 0 {
		dst.CpuPercent = src.CpuPercent
	}
	if src.MemoryPercent >= 0 {
		dst.MemoryPercent = src.MemoryPercent
	}
	if src.MemoryTotalGb > 0 {
		dst.MemoryTotalGb = src.MemoryTotalGb
	}
	if src.MemoryUsedGb > 0 {
		dst.MemoryUsedGb = src.MemoryUsedGb
	}
	if src.DiskPercent >= 0 {
		dst.DiskPercent = src.DiskPercent
	}
	if src.DiskTotalGb > 0 {
		dst.DiskTotalGb = src.DiskTotalGb
	}
	if src.DiskUsedGb > 0 {
		dst.DiskUsedGb = src.DiskUsedGb
	}
	if src.DiskReadPercent >= 0 {
		dst.DiskReadPercent = src.DiskReadPercent
	}
	if src.DiskWritePercent >= 0 {
		dst.DiskWritePercent = src.DiskWritePercent
	}
	if src.DiskResponseMs >= 0 {
		dst.DiskResponseMs = src.DiskResponseMs
	}
	if src.CpuTemperatureCelsius >= 0 {
		dst.CpuTemperatureCelsius = src.CpuTemperatureCelsius
	}
	if src.UptimeSeconds > 0 {
		dst.UptimeSeconds = src.UptimeSeconds
	}
	if src.ProcessCount > 0 {
		dst.ProcessCount = src.ProcessCount
	}
	// Usuário logado (sessão interativa). Copiado junto com o início da sessão;
	// sem isto o heartbeat real NUNCA levava o campo (o struct é remontado em
	// getHeartbeatMetrics e este merge descartava o valor coletado).
	if user := strings.TrimSpace(src.LoggedUser); user != "" {
		dst.LoggedUser = user
		dst.LoggedUserSince = src.LoggedUserSince
	}
	if src.PeerID != "" {
		dst.PeerID = src.PeerID
	}
	if len(src.Addrs) > 0 {
		dst.Addrs = append([]string(nil), src.Addrs...)
	}
	if src.Port > 0 {
		dst.Port = src.Port
	}
}

func (a *App) getKnownP2PPeers() int {
	if a.P2PCoord == nil {
		return 0
	}
	return len(a.P2PCoord.GetPeers())
}

// onNatsConnected é chamado pelo agentconn após a conexão NATS ser estabelecida.
// Injeta a conexão NATS no remoteSessionMgr para habilitar o streaming de frames.
func (a *App) onNatsConnected(nc *nats.Conn, cfg agentconn.Config) {
	// Chat de suporte: assina o canal servidor -> agent com consumer durável.
	// Independente do remote session (roda mesmo com o manager ausente).
	a.supportChatService().OnNATSConnected(nc, cfg.ClientID, cfg.SiteID, cfg.AgentID)

	// Um convite pode ter se perdido enquanto o NATS estava fora (o canal de
	// comando é unicast, sem replay). Descobre sessões pendentes/ativas pelo
	// REST e abre a janela — sem roubar foco de uma janela já aberta.
	go a.resumeSupportChatIfActive(time.Second)

	if a.RemoteSessionMgr == nil {
		return
	}
	a.RemoteSessionMgr.SetNatsConn(nc, cfg.ClientID, cfg.SiteID, cfg.AgentID)
	a.Logs.Append(fmt.Sprintf("[remote-session] NATS conectado — streaming habilitado (tenant=%s, site=%s, agent=%s)",
		cfg.ClientID, cfg.SiteID, cfg.AgentID))
}

// onConnectivityChange é chamado pelo agentconn quando o estado online/offline
// muda. Atualiza o tray imediatamente e emite um evento para o frontend, para
// que a bolinha da barra, a página de Status e a consulta de versão reajam na
// hora — sem depender do polling.
func (a *App) onConnectivityChange(connected bool, transport string) {
	// O estado EFETIVO combina o transporte NATS com a saúde da API HTTP: o
	// agent só é "online" quando os dois respondem (NATS nativo, wss ou API
	// fora ⇒ offline).
	apiReachable := true
	if a.ApiClientSvc != nil {
		apiReachable = a.ApiClientSvc.APIReachable()
	}
	effective := connected && apiReachable
	state := "offline"
	if effective {
		state = "online"
	}
	// Idade do último pong global: correlaciona oscilações do indicador com
	// lacunas de entrega do servidor (watchdog de global pong / WSS).
	pongInfo := " pongAge=never"
	if a.SyncSvc != nil {
		if lastPongAt, _, _, _ := a.SyncSvc.GlobalPongStatus(); !lastPongAt.IsZero() {
			pongInfo = fmt.Sprintf(" pongAge=%s", time.Since(lastPongAt).Round(time.Second))
		}
	}
	if effective != connected {
		pongInfo += fmt.Sprintf(" apiReachable=%t", apiReachable)
	}
	a.Logs.Append(fmt.Sprintf("[connectivity] mudanca de estado para %s (transport=%s%s)", state, transport, pongInfo))
	// Suporte/Knowledge entram em somente-consulta junto com o transporte, sem
	// depender de uma operação falhar primeiro.
	if a.SupportSvc != nil {
		a.SupportSvc.NotifyConnectivity(effective)
	}
	a.EmitEvent("agent:connectivity", map[string]any{
		"connected":    effective,
		"transport":    transport,
		"apiReachable": apiReachable,
		"reason":       fmt.Sprintf("transport=%s%s", transport, pongInfo),
	})
	// Atualiza o ícone e o tooltip do tray imediatamente (safeTrayAction cobre
	// o caso de o tray ainda não ter sido iniciado).
	a.syncTrayVisualState()
	a.updateTrayTooltip()
	a.updateTrayMenu()
}

func (a *App) SendTestHeartbeat() string {
	if !a.QueuedForceHeartbeat.CompareAndSwap(false, true) {
		return "erro: heartbeat manual ja em andamento"
	}
	defer a.QueuedForceHeartbeat.Store(false)

	a.Logs.Append("[heartbeat][manual] enviando heartbeat manual...")
	if a.AgentConn == nil {
		a.Logs.Append("[heartbeat][manual] falha ao enviar heartbeat manual: agent runtime nao inicializado")
		return "erro: agent runtime nao inicializado"
	}
	if a.AgentConn.ForceHeartbeat() {
		a.Logs.Append("[heartbeat][manual] heartbeat manual enviado com sucesso")
		return "heartbeat manual enviado com sucesso"
	}
	a.Logs.Append("[heartbeat][manual] falha ao enviar heartbeat manual: timeout ou nenhuma conexão ativa")
	return "falha ao enviar heartbeat manual: timeout ou nenhuma conexão ativa"
}
