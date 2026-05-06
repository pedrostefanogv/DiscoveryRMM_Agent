package agentconn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/netutil"
	"discovery/internal/tlsutil"
)

const (
	heartbeatEvery   = 30 * time.Second
	reconnectBase    = 10 * time.Second
	reconnectJitter  = 5 * time.Second
	handshakeTimeout = 10 * time.Second
	maxOutputBytes   = 1 << 20

	commandResultDrainEvery = 15 * time.Second
	commandResultDrainLimit = 20
	commandResultRetryBase  = 15 * time.Second
	commandResultRetryMax   = 5 * time.Minute

	connectAttemptTimeout = 5 * time.Second

	commandFanoutDedupeDefaultTTL = 30 * time.Minute
	commandFanoutDedupeMaxTTL     = 24 * time.Hour
	commandFanoutDedupeMinTTL     = 1 * time.Minute
)

var guidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type natsCommandEnvelope struct {
	DispatchID     string `json:"dispatchId"`
	CommandID      string `json:"commandId"`
	CommandType    any    `json:"commandType"`
	TargetScope    string `json:"targetScope"`
	TargetClientID string `json:"targetClientId"`
	TargetSiteID   string `json:"targetSiteId"`
	IssuedAtUTC    string `json:"issuedAtUtc"`
	ExpiresAtUTC   string `json:"expiresAtUtc"`
	IdempotencyKey string `json:"idempotencyKey"`
	Payload        any    `json:"payload"`
}

// AgentHeartbeat is the standardized payload for heartbeats
// sent over NATS transports (nats:// and ws/wss).
type AgentHeartbeat struct {
	AgentId       string   `json:"agentId"`
	ClientId      string   `json:"clientId,omitempty"`
	SiteId        string   `json:"siteId,omitempty"`
	IpAddress     string   `json:"ipAddress,omitempty"`
	Hostname      string   `json:"hostname,omitempty"`
	AgentVersion  string   `json:"agentVersion,omitempty"`
	TimestampUtc  string   `json:"timestampUtc,omitempty"` // RFC3339
	CpuPercent    *float64 `json:"cpuPercent,omitempty"`
	MemoryPercent *float64 `json:"memoryPercent,omitempty"`
	MemoryTotalGb *float64 `json:"memoryTotalGb,omitempty"`
	MemoryUsedGb  *float64 `json:"memoryUsedGb,omitempty"`
	DiskPercent   *float64 `json:"diskPercent,omitempty"`
	DiskTotalGb   *float64 `json:"diskTotalGb,omitempty"`
	DiskUsedGb    *float64 `json:"diskUsedGb,omitempty"`
	P2pPeers      *int     `json:"p2pPeers,omitempty"`
	UptimeSeconds *int64   `json:"uptimeSeconds,omitempty"`
	ProcessCount  *int     `json:"processCount,omitempty"`
}

// AgentHeartbeatMetrics is a lightweight struct for collecting
// system metrics to include in heartbeats.
type AgentHeartbeatMetrics struct {
	Hostname      string
	CpuPercent    float64
	MemoryPercent float64
	MemoryTotalGb float64
	MemoryUsedGb  float64
	DiskPercent   float64
	DiskTotalGb   float64
	DiskUsedGb    float64
	P2pPeers      int
	UptimeSeconds int64
	ProcessCount  int
}

type natsResultEnvelope struct {
	DispatchID   string `json:"dispatchId,omitempty"`
	CommandID    string `json:"commandId,omitempty"`
	AgentID      string `json:"agentId,omitempty"`
	ExitCode     int    `json:"exitCode"`
	Output       string `json:"output,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type natsDashboardEvent struct {
	EventType    string         `json:"eventType"`
	Data         map[string]any `json:"data,omitempty"`
	TimestampUtc string         `json:"timestampUtc"`
	ClientId     string         `json:"clientId,omitempty"`
	SiteId       string         `json:"siteId,omitempty"`
}

type P2PDiscoveryPeer struct {
	AgentID string   `json:"agentId"`
	PeerID  string   `json:"peerId"`
	Addrs   []string `json:"addrs"`
	Port    int      `json:"port"`
}

type P2PDiscoverySnapshot struct {
	Sequence   uint64             `json:"sequence"`
	TTLSeconds int                `json:"ttlSeconds"`
	Peers      []P2PDiscoveryPeer `json:"peers"`
	ReceivedAt time.Time          `json:"-"`
}

// SyncPing representa um evento de invalidação de sync recebido pelo agent.
type SyncPing struct {
	EventID          string `json:"eventId"`
	AgentID          string `json:"agentId"`
	EventType        string `json:"eventType"`
	Resource         string `json:"resource"`
	ScopeType        string `json:"scopeType"`
	ScopeID          string `json:"scopeId"`
	InstallationType string `json:"installationType"`
	Revision         string `json:"revision"`
	Reason           string `json:"reason"`
	ChangedAtUTC     string `json:"changedAtUtc"`
	CorrelationID    string `json:"correlationId"`
}

// GlobalPongMessage representa sinalizacao global de liveness do servidor.
type GlobalPongMessage struct {
	EventType        string `json:"eventType"`
	ServerTimeUTC    string `json:"serverTimeUtc"`
	ServerOverloaded *bool  `json:"serverOverloaded"`
	ReceivedAt       time.Time
}

// Config is the backend communication configuration sourced from Debug settings.
type Config struct {
	ApiScheme                string
	ApiServer                string
	NatsServer               string
	NatsWsServer             string
	NatsServerHost           string
	NatsUseWssExternal       bool
	EnforceTLSHashValidation bool
	HandshakeEnabled         bool
	ApiTLSCertHash           string
	NatsTLSCertHash          string
	AuthToken                string
	AgentID                  string
	AgentVersion             string
	ClientID                 string
	SiteID                   string
	HeartbeatInterval        int // segundos; 0 = usar padrão (30s)
}

type natsSubjects struct {
	CommandAgent        string
	CommandSiteFanout   string
	CommandClientFanout string
	CommandGlobalFanout string
	GlobalPong          string
	Heartbeat           string
	Result              string
	Hardware            string
	RemoteDebugLog      string
	SyncPing            string
	P2PDiscovery        string
}

// Options defines dependencies injected by the app layer.
type Options struct {
	LoadConfig             func() Config
	Logf                   func(format string, args ...any)
	OnSyncPing             func(SyncPing)
	OnGlobalPong           func(GlobalPongMessage)
	OnP2PDiscoverySnapshot func(P2PDiscoverySnapshot)
	HandleCommand          func(parent context.Context, cmdType string, payload any) (handled bool, exitCode int, output string, errText string)
	OnCommandOutput        func(cmdType string, output string, errText string)

	GetHeartbeatMetrics           func() AgentHeartbeatMetrics
	EnqueueCommandResultOutbox    func(transport, dispatchID, commandID string, exitCode int, output, errText, sendError string) error
	ListDueCommandResultOutbox    func(transport string, now time.Time, limit int) ([]CommandResultOutboxItem, error)
	MarkSentCommandResultOutbox   func(id int64) error
	RescheduleCommandResultOutbox func(id int64, attempts int, nextAttemptAt time.Time, lastError string) error
}

type CommandResultOutboxItem struct {
	ID           int64
	DispatchID   string
	CommandID    string
	ExitCode     int
	Output       string
	ErrorMessage string
	Attempts     int
}

// Status is a point-in-time snapshot of the agent connection state.
type Status struct {
	Connected bool   `json:"connected"`
	AgentID   string `json:"agentId"`
	Server    string `json:"server"`
	LastEvent string `json:"lastEvent"`
	Transport string `json:"transport,omitempty"`
}

type fanoutDedupeRecord struct {
	ExpiresAt time.Time
	Result    *natsResultEnvelope
}

// Runtime manages the long-lived agent connection and command processing loop.
type Runtime struct {
	opts Options

	statMu   sync.RWMutex
	statSnap Status

	dedupeMu     sync.Mutex
	fanoutDedupe map[string]fanoutDedupeRecord

	// forceHeartbeatCh is a channel used by ForceHeartbeat() to trigger
	// an immediate heartbeat send in the active connection event loop.
	forceHeartbeatCh chan chan struct{}
	// reloadCh requests the active session loop to reconnect with fresh config.
	reloadCh chan struct{}
}

func NewRuntime(opts Options) *Runtime {
	return &Runtime{
		opts:             opts,
		forceHeartbeatCh: make(chan chan struct{}, 4),
		reloadCh:         make(chan struct{}, 1),
		fanoutDedupe:     make(map[string]fanoutDedupeRecord),
	}
}

// ForceHeartbeat triggers an immediate heartbeat send in the active
// connection event loop. It waits for the send to complete (or timeout).
// Returns true if the heartbeat was sent, false if no connection is active.
func (r *Runtime) ForceHeartbeat() bool {
	done := make(chan struct{}, 1)
	select {
	case r.forceHeartbeatCh <- done:
		select {
		case <-done:
			return true
		case <-time.After(10 * time.Second):
			r.logf("[heartbeat][force] timeout aguardando envio do heartbeat forçado (10s)")
			return false
		}
	default:
		r.logf("[heartbeat][force] sessao inativa: nenhuma conexao consumindo heartbeats — agent desconectado")
		return false
	}
}

// GetStatus returns a snapshot of the current connection state.
func (r *Runtime) GetStatus() Status {
	r.statMu.RLock()
	defer r.statMu.RUnlock()
	return r.statSnap
}

func (r *Runtime) setStatus(connected bool, event string) {
	r.statMu.Lock()
	defer r.statMu.Unlock()
	wasConnected := r.statSnap.Connected
	r.statSnap.Connected = connected
	r.statSnap.LastEvent = event
	if !connected {
		r.statSnap.Transport = ""
		if wasConnected {
			r.logf("[heartbeat][status] conexao perdida: %s — heartbeats serao suspensos ate reconexao", event)
		}
	}
}

func (r *Runtime) setStatusConnected(agentID, server, transport string) {
	r.statMu.Lock()
	defer r.statMu.Unlock()
	r.statSnap.Connected = true
	r.statSnap.AgentID = agentID
	r.statSnap.Server = server
	r.statSnap.LastEvent = "conectado"
	r.statSnap.Transport = transport
}

// collectHeartbeat assembles a standardized AgentHeartbeat payload using
// the current config, IP address, and optional system metrics callback.
func (r *Runtime) collectHeartbeat(cfg Config, ipAddr string) AgentHeartbeat {
	hb := AgentHeartbeat{
		AgentId:      cfg.AgentID,
		ClientId:     cfg.ClientID,
		SiteId:       cfg.SiteID,
		IpAddress:    ipAddr,
		AgentVersion: cfg.AgentVersion,
		TimestampUtc: time.Now().UTC().Format(time.RFC3339),
	}
	if r.opts.GetHeartbeatMetrics != nil {
		m := r.opts.GetHeartbeatMetrics()
		if m.Hostname != "" {
			hb.Hostname = m.Hostname
		}
		hb.CpuPercent = nonNegFloatPtr(m.CpuPercent)
		hb.MemoryPercent = nonNegFloatPtr(m.MemoryPercent)
		hb.MemoryTotalGb = positiveFloatPtr(m.MemoryTotalGb)
		hb.MemoryUsedGb = positiveFloatPtr(m.MemoryUsedGb)
		hb.DiskPercent = nonNegFloatPtr(m.DiskPercent)
		hb.DiskTotalGb = positiveFloatPtr(m.DiskTotalGb)
		hb.DiskUsedGb = positiveFloatPtr(m.DiskUsedGb)
		hb.P2pPeers = &m.P2pPeers
		hb.UptimeSeconds = &m.UptimeSeconds
		hb.ProcessCount = &m.ProcessCount
	}
	return hb
}

// heartbeatLogPayload serializa o payload para facilitar auditoria no log.
func heartbeatLogPayload(hb AgentHeartbeat) string {
	b, err := json.Marshal(hb)
	if err != nil {
		return fmt.Sprintf("{\"marshalError\":%q}", err.Error())
	}
	return string(b)
}

// nonNegFloatPtr retorna ponteiro para v se v >= 0; nil (omitir do JSON)
// se v < 0 (sentinel usado para indicar métrica não coletada).
func nonNegFloatPtr(v float64) *float64 {
	if v >= 0 {
		return &v
	}
	return nil
}

// positiveFloatPtr retorna um ponteiro para v se v > 0; nil caso contrário.
func positiveFloatPtr(v float64) *float64 {
	if v > 0 {
		return &v
	}
	return nil
}

func isCanonicalDashboardEventType(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "AgentHeartbeat", "AgentStatusChanged", "CommandCompleted", "AgentHardwareReported", "AgentConnected", "AgentDisconnected":
		return true
	default:
		return false
	}
}

func dashboardFieldString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, ok := data[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func (r *Runtime) logContractViolation(field, expected, received, source string) {
	r.logf("[CONTRACT_VIOLATION] component=Agent field=%s expected='%s' received='%s' source=%s", strings.TrimSpace(field), strings.TrimSpace(expected), strings.TrimSpace(received), strings.TrimSpace(source))
}

func (r *Runtime) newNATSDashboardEvent(cfg Config, eventType string, data map[string]any) (natsDashboardEvent, bool) {
	eventType = strings.TrimSpace(eventType)
	if !isCanonicalDashboardEventType(eventType) {
		r.logContractViolation("eventType", "enum(6)", eventType, "nats")
		return natsDashboardEvent{}, false
	}

	clientID := strings.TrimSpace(cfg.ClientID)
	if clientID == "" {
		r.logContractViolation("clientId", "present", "missing", "nats")
		return natsDashboardEvent{}, false
	}

	siteID := strings.TrimSpace(cfg.SiteID)
	if siteID == "" {
		r.logContractViolation("siteId", "present", "missing", "nats")
		return natsDashboardEvent{}, false
	}

	if strings.TrimSpace(dashboardFieldString(data, "agentId")) == "" {
		r.logContractViolation("data.agentId", "Guid", "missing", "nats")
		return natsDashboardEvent{}, false
	}

	if (eventType == "AgentConnected" || eventType == "AgentDisconnected") && strings.TrimSpace(dashboardFieldString(data, "transport")) == "" {
		r.logContractViolation("data.transport", "nats|nats-ws|nats-wss", "missing", "nats")
	}

	return natsDashboardEvent{
		EventType:    eventType,
		Data:         data,
		TimestampUtc: time.Now().UTC().Format(time.RFC3339),
		ClientId:     clientID,
		SiteId:       siteID,
	}, true
}

func (r *Runtime) publishDashboardEventNATS(nc *nats.Conn, subject string, cfg Config, eventType string, data map[string]any) bool {
	event, ok := r.newNATSDashboardEvent(cfg, eventType, data)
	if !ok {
		return false
	}
	if err := publishJSON(nc, subject, event); err != nil {
		r.logf("falha ao publicar dashboard event NATS (subject=%s eventType=%s): %v", subject, strings.TrimSpace(eventType), err)
		return false
	}
	return true
}

// Reload asks the active session loop to reconnect with updated config.
func (r *Runtime) Reload() {
	select {
	case r.reloadCh <- struct{}{}:
	default:
	}
}

// Run starts the resilient connection loop and blocks until ctx is canceled.
func (r *Runtime) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		cfg := r.opts.LoadConfig()
		cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
		cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
		cfg.NatsServer = strings.TrimSpace(cfg.NatsServer)
		cfg.NatsWsServer = strings.TrimSpace(cfg.NatsWsServer)
		cfg.NatsServerHost = strings.TrimSpace(cfg.NatsServerHost)
		cfg.ApiTLSCertHash = normalizeTLSCertHash(cfg.ApiTLSCertHash)
		cfg.NatsTLSCertHash = normalizeTLSCertHash(cfg.NatsTLSCertHash)
		cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)
		cfg.AgentID = strings.TrimSpace(cfg.AgentID)
		cfg.ClientID = strings.TrimSpace(cfg.ClientID)
		cfg.SiteID = strings.TrimSpace(cfg.SiteID)

		// Aplica NatsServerHost como override no host do endpoint NATS nativo.
		if cfg.NatsServerHost != "" {
			if overridden, err := rewriteNATSHost(cfg.NatsServer, cfg.NatsServerHost); err != nil {
				r.logf("[transport][nats] host override invalido (natsServerHost=%s): %v", cfg.NatsServerHost, err)
			} else if overridden != "" && overridden != cfg.NatsServer {
				r.logf("[transport][nats] host override aplicado para nats://")
				cfg.NatsServer = overridden
			}
		}

		if derivedNATS, derivedWSS := autoDeriveNATSEndpoints(&cfg); derivedNATS || derivedWSS {
			if derivedNATS {
				r.logf("[transport][nats] auto-derivado: %s", cfg.NatsServer)
			}
			if derivedWSS {
				r.logf("[transport][nats-wss] auto-derivado: %s", cfg.NatsWsServer)
			}
		}

		if cfg.ApiServer != "" && cfg.ApiScheme != "http" && cfg.ApiScheme != "https" {
			r.logf("configuracao de agente ignorada: apiScheme invalido — heartbeats suspensos")
			cfg.ApiServer = ""
		}

		if err := validateTransportSecurity(cfg); err != nil {
			r.logf("configuracao de agente insegura: %v — heartbeats suspensos ate nova tentativa", err)
			r.waitOrStop(ctx, reconnectBase)
			continue
		}

		if cfg.ApiServer == "" && cfg.NatsServer == "" {
			r.logf("configuracao de agente ausente: nenhum servidor configurado — heartbeats suspensos ate nova tentativa")
			r.waitOrStop(ctx, reconnectBase)
			continue
		}

		r.logf("tentando conexao (fallback) com agentId=%s", cfg.AgentID)

		err := r.runSession(ctx, cfg)
		if err != nil && ctx.Err() == nil {
			r.logf("sessao encerrada: %v", err)
			r.setStatus(false, "sessao encerrada: "+err.Error())
		} else if ctx.Err() != nil {
			r.setStatus(false, "contexto cancelado")
		}
		r.waitReconnectWithJitter(ctx)
	}
}

func (r *Runtime) runSession(ctx context.Context, cfg Config) error {
	// Ordem de fallback:
	//   1. NATS nativo (nats://)
	//   2. NATS sobre WebSocket (ws/wss)
	//   3. Fallbacks derivados de apiServer (nats://:4222 e wss://:443/nats/)
	var attempts []func() error
	var labels []string
	seen := map[string]struct{}{}

	// Extrai host da API para usar como fallback nos endpoints NATS.
	apiHost := extractHostFromServer(cfg.ApiServer)

	addNATSAttempt := func(server, label string) {
		server = strings.TrimSpace(server)
		if server == "" {
			return
		}
		normalized, err := normalizeNATSURL(server)
		if err != nil {
			r.logf("[transport][nats] endpoint ignorado (%s): %v", label, err)
			return
		}
		if _, exists := seen[normalized]; exists {
			return
		}
		seen[normalized] = struct{}{}
		labels = append(labels, label)
		transport := natsTransportLabel(normalized)
		attempts = append(attempts, func() error {
			return r.runNATSSession(ctx, cfg, normalized, transport, connectAttemptTimeout)
		})
	}

	addNATSAttempt(cfg.NatsServer, "nats")
	addNATSAttempt(cfg.NatsWsServer, "nats-ws/wss")

	if apiHost != "" {
		fallbackNats := "nats://" + net.JoinHostPort(apiHost, "4222")
		addNATSAttempt(fallbackNats, "nats (api-fallback)")
		r.logf("[transport][nats] fallback via apiServer: %s", fallbackNats)

		if fallbackWSS, err := buildExternalNATSWSSURL(apiHost); err == nil {
			addNATSAttempt(fallbackWSS, "nats-wss (api-fallback)")
			r.logf("[transport][nats-wss] fallback via apiServer: %s", fallbackWSS)
		}
	}

	if len(attempts) == 0 {
		r.logf("[heartbeat][session] nenhum transporte configurado — heartbeats nao serao enviados ate que uma configuracao valida seja recebida")
		return fmt.Errorf("nenhum transporte configurado")
	}

	var lastErr error
	for i, attempt := range attempts {
		err := attempt()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = err
		r.logf("[transport][fallback] %s falhou: %v", labels[i], err)
	}
	return lastErr
}

func natsTransportLabel(server string) string {
	normalized, err := normalizeNATSURL(server)
	if err != nil {
		return "nats"
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return "nats"
	}
	switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
	case "ws":
		return "nats-ws"
	case "wss":
		return "nats-wss"
	default:
		return "nats"
	}
}

// extractHostFromServer extrai apenas o host (sem porta) de um endereco de servidor.
// Ex: "192.168.1.142" → "192.168.1.142"
// Ex: "192-168-1-142.nip.io:443" → "192-168-1-142.nip.io"
func extractHostFromServer(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	// Remove scheme se presente
	if strings.Contains(server, "://") {
		if u, err := url.Parse(server); err == nil {
			server = strings.TrimSpace(u.Host)
		}
	}
	// Remove porta se presente
	if h, _, err := net.SplitHostPort(server); err == nil {
		return strings.TrimSpace(h)
	}
	return strings.Trim(strings.TrimSpace(server), "[]")
}

// autoDeriveNATSEndpoints derives NATS endpoints from NatsServerHost.
func autoDeriveNATSEndpoints(cfg *Config) (derivedNATS bool, derivedWSS bool) {
	if cfg == nil {
		return false, false
	}
	host := strings.TrimSpace(cfg.NatsServerHost)
	if host == "" {
		return false, false
	}

	if cfg.NatsWsServer == "" {
		if wssURL, err := buildExternalNATSWSSURL(host); err == nil {
			cfg.NatsWsServer = wssURL
			derivedWSS = true
		}
	}

	if cfg.NatsServer == "" && !cfg.NatsUseWssExternal && isLocalTarget(host) {
		if nativeURL := deriveNativeNATSServerFromHost(host); nativeURL != "" {
			cfg.NatsServer = nativeURL
			derivedNATS = true
		}
	}

	return derivedNATS, derivedWSS
}

func deriveNativeNATSServerFromHost(rawHost string) string {
	host := strings.TrimSpace(rawHost)
	if host == "" {
		return ""
	}
	if strings.Contains(host, "://") {
		if parsed, err := url.Parse(host); err == nil && strings.TrimSpace(parsed.Host) != "" {
			host = strings.TrimSpace(parsed.Host)
		}
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = strings.TrimSpace(h)
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return ""
	}
	return "nats://" + net.JoinHostPort(host, "4222")
}

// sendHeartbeatNATS sends a single heartbeat on the NATS connection.
func (r *Runtime) sendHeartbeatNATS(nc *nats.Conn, subject string, cfg Config, ipAddr string) {
	hb := r.collectHeartbeat(cfg, ipAddr)
	hbLog := heartbeatLogPayload(hb)
	if err := publishJSON(nc, subject, hb); err != nil {
		r.logf("[heartbeat][nats] falha ao publicar heartbeat (subject=%s): %v payload=%s", subject, err, hbLog)
		return
	}
	r.logf("[heartbeat][nats] heartbeat publicado com sucesso (subject=%s) payload=%s", subject, hbLog)
}

// runNATSSession está definida em runtime_nats.go
// resolveNATSSubjects, validateCanonicalNATSContext, canonicalSubjectSegment e publishJSON também.

func (r *Runtime) reportTLSMismatch(cfg Config, target, observedHash string) {
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	authToken := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)
	if apiServer == "" || authToken == "" || agentID == "" {
		r.logf("[security][%s] tls-mismatch nao enviado: contexto incompleto", strings.TrimSpace(target))
		return
	}
	if apiScheme != "http" && apiScheme != "https" {
		r.logf("[security][%s] tls-mismatch nao enviado: apiScheme invalido", strings.TrimSpace(target))
		return
	}

	body, _ := json.Marshal(map[string]string{
		"target":       strings.TrimSpace(target),
		"observedHash": normalizeTLSCertHash(observedHash),
	})
	endpoint := apiScheme + "://" + apiServer + "/api/v1/agent-auth/me/tls-mismatch"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		r.logf("[security][%s] falha ao montar tls-mismatch: %v", strings.TrimSpace(target), err)
		return
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, authToken, agentID); err != nil {
		r.logf("[security][%s] tls-mismatch nao enviado: %v", strings.TrimSpace(target), err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	r.logf("[security][%s] enviando tls-mismatch para %s", strings.TrimSpace(target), endpoint)
	resp, err := tlsutil.NewHTTPClient(5 * time.Second).Do(req)
	if err != nil {
		r.logf("[security][%s] falha ao enviar tls-mismatch: %v", strings.TrimSpace(target), err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.logf("[security][%s] tls-mismatch retornou HTTP %s", strings.TrimSpace(target), resp.Status)
		return
	}
	r.logf("[security][%s] tls-mismatch enviado com sucesso", strings.TrimSpace(target))
}

func FetchNATSInfo(server string, timeout time.Duration, authToken string) (string, error) {
	natsURL, err := normalizeNATSURL(server)
	if err != nil {
		return "", err
	}

	u, err := url.Parse(natsURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "ws" || u.Scheme == "wss" {
		opts := []nats.Option{
			nats.Timeout(timeout),
		}
		if strings.EqualFold(strings.TrimSpace(u.Scheme), "wss") {
			if tlsCfg := tlsutil.InsecureTLSConfig(); tlsCfg != nil {
				opts = append(opts, nats.Secure(tlsCfg))
			}
		}
		if strings.TrimSpace(authToken) != "" {
			opts = append(opts, nats.Token(strings.TrimSpace(authToken)))
		}
		nc, err := nats.Connect(natsURL, opts...)
		if err != nil {
			return "", fmt.Errorf("falha ao conectar no NATS WS: %w", err)
		}
		defer nc.Close()
		return fmt.Sprintf("conectado via %s", nc.ConnectedUrl()), nil
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":4222"
	}

	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return "", fmt.Errorf("falha ao conectar em %s: %w", host, err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("falha ao ler INFO do NATS: %w", err)
	}
	line := strings.TrimSpace(string(buf[:n]))
	if line == "" {
		return "", fmt.Errorf("resposta INFO vazia do NATS")
	}

	if strings.HasPrefix(line, "INFO ") {
		jsonPart := strings.TrimSpace(strings.TrimPrefix(line, "INFO "))
		var info any
		if json.Unmarshal([]byte(jsonPart), &info) == nil {
			pretty, _ := json.MarshalIndent(info, "", "  ")
			return string(pretty), nil
		}
	}

	return line, nil
}

func (r *Runtime) executeCommand(parent context.Context, cmdType string, payload any) (int, string, string) {
	cmdType = normalizeCommandType(cmdType)
	if r.opts.HandleCommand != nil {
		handled, exitCode, output, errText := r.opts.HandleCommand(parent, cmdType, payload)
		if handled {
			r.emitCommandOutput(cmdType, output, errText)
			return exitCode, output, errText
		}
	}

	exitCode, output, errText := executeCommand(parent, cmdType, payload)
	r.emitCommandOutput(cmdType, output, errText)
	return exitCode, output, errText
}

func (r *Runtime) emitCommandOutput(cmdType, output, errText string) {
	if r.opts.OnCommandOutput == nil {
		return
	}
	r.opts.OnCommandOutput(normalizeCommandType(cmdType), output, errText)
}

func (r *Runtime) enqueueCommandResultOutbox(transport, dispatchID, cmdID string, exitCode int, output, errText string, sendErr error) {
	if r.opts.EnqueueCommandResultOutbox == nil {
		return
	}
	errMsg := ""
	if sendErr != nil {
		errMsg = sendErr.Error()
	}
	if err := r.opts.EnqueueCommandResultOutbox(strings.TrimSpace(transport), strings.TrimSpace(dispatchID), strings.TrimSpace(cmdID), exitCode, output, errText, errMsg); err != nil {
		r.logf("falha ao enfileirar CommandResult offline transport=%s dispatchId=%s cmdId=%s: %v", strings.TrimSpace(transport), strings.TrimSpace(dispatchID), strings.TrimSpace(cmdID), err)
		return
	}
	r.logf("CommandResult enfileirado para retry offline transport=%s dispatchId=%s cmdId=%s", strings.TrimSpace(transport), strings.TrimSpace(dispatchID), strings.TrimSpace(cmdID))
}

func (r *Runtime) drainCommandResultOutbox(ctx context.Context, transport string, sendFn func(item CommandResultOutboxItem) error) {
	if r.opts.ListDueCommandResultOutbox == nil || r.opts.MarkSentCommandResultOutbox == nil || r.opts.RescheduleCommandResultOutbox == nil {
		return
	}
	items, err := r.opts.ListDueCommandResultOutbox(strings.TrimSpace(transport), time.Now(), commandResultDrainLimit)
	if err != nil {
		r.logf("falha ao listar outbox command_result transport=%s: %v", strings.TrimSpace(transport), err)
		return
	}
	for _, item := range items {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := sendFn(item); err != nil {
			attempt := item.Attempts + 1
			nextAttemptAt := time.Now().Add(commandResultRetryBackoff(attempt))
			if resErr := r.opts.RescheduleCommandResultOutbox(item.ID, attempt, nextAttemptAt, err.Error()); resErr != nil {
				r.logf("falha ao reagendar outbox command_result id=%d transport=%s: %v", item.ID, strings.TrimSpace(transport), resErr)
				continue
			}
			r.logf("retry command_result reagendado id=%d transport=%s tentativa=%d erro=%v", item.ID, strings.TrimSpace(transport), attempt, err)
			continue
		}

		if err := r.opts.MarkSentCommandResultOutbox(item.ID); err != nil {
			r.logf("falha ao marcar envio outbox command_result id=%d transport=%s: %v", item.ID, strings.TrimSpace(transport), err)
			continue
		}
		r.logf("command_result reenviado com sucesso id=%d transport=%s cmdId=%s", item.ID, strings.TrimSpace(transport), item.CommandID)
	}
}

func commandResultRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	backoff := commandResultRetryBase
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff >= commandResultRetryMax {
			backoff = commandResultRetryMax
			break
		}
	}
	jitter := time.Duration(rand.Intn(2000)) * time.Millisecond
	return backoff + jitter
}

func (r *Runtime) waitReconnectWithJitter(ctx context.Context) {
	d := reconnectBase + time.Duration(rand.Intn(int(reconnectJitter.Milliseconds()+1)))*time.Millisecond
	r.logf("reconectando em %s", d.Round(time.Millisecond))
	r.waitOrStop(ctx, d)
}

func (r *Runtime) waitOrStop(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (r *Runtime) logf(format string, args ...any) {
	if r.opts.Logf != nil {
		r.opts.Logf(format, args...)
	}
}

func (r *Runtime) emitSyncPing(ping SyncPing) {
	if strings.TrimSpace(ping.Resource) == "" {
		return
	}
	r.logf("sync ping recebido: resource=%s installationType=%s revision=%s eventId=%s", ping.Resource, ping.InstallationType, ping.Revision, ping.EventID)
	if r.opts.OnSyncPing != nil {
		r.opts.OnSyncPing(ping)
	}
}

func (r *Runtime) emitGlobalPong(pong GlobalPongMessage) {
	state := "null"
	if pong.ServerOverloaded != nil {
		if *pong.ServerOverloaded {
			state = "true"
		} else {
			state = "false"
		}
	}
	r.logf("global pong recebido: eventType=%s serverOverloaded=%s serverTimeUtc=%s", strings.TrimSpace(pong.EventType), state, strings.TrimSpace(pong.ServerTimeUTC))
	if r.opts.OnGlobalPong != nil {
		r.opts.OnGlobalPong(pong)
	}
}

// validateTransportSecurity enforces secure transport in non-local endpoints.
// A local dev override exists via DISCOVERY_ALLOW_INSECURE_TRANSPORT.
