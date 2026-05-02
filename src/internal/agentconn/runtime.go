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

	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"

	"discovery/internal/tlsutil"
)

const (
	hubPath          = "/hubs/agent"
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
)

var guidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type natsCommandEnvelope struct {
	CommandID   string `json:"commandId"`
	CommandType any    `json:"commandType"`
	Payload     any    `json:"payload"`
}

type natsHeartbeatEnvelope struct {
	IPAddress    string `json:"ipAddress,omitempty"`
	AgentVersion string `json:"agentVersion,omitempty"`
}

type natsResultEnvelope struct {
	CommandID    string `json:"commandId"`
	ExitCode     int    `json:"exitCode"`
	Output       string `json:"output,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type natsDashboardEvent struct {
	EventType string `json:"eventType"`
	Data      any    `json:"data"`
	Timestamp string `json:"timestamp"`
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
	ClientID                 string
	SiteID                   string
}

type natsSubjects struct {
	Command   string
	Heartbeat string
	Result    string
	Hardware  string
	SyncPing  string
	Dashboard string
}

// Options defines dependencies injected by the app layer.
type Options struct {
	LoadConfig      func() Config
	Logf            func(format string, args ...any)
	OnSyncPing      func(SyncPing)
	HandleCommand   func(parent context.Context, cmdType string, payload any) (handled bool, exitCode int, output string, errText string)
	OnCommandOutput func(cmdType string, output string, errText string)

	EnqueueCommandResultOutbox    func(transport, commandID string, exitCode int, output, errText, sendError string) error
	ListDueCommandResultOutbox    func(transport string, now time.Time, limit int) ([]CommandResultOutboxItem, error)
	MarkSentCommandResultOutbox   func(id int64) error
	RescheduleCommandResultOutbox func(id int64, attempts int, nextAttemptAt time.Time, lastError string) error
}

type CommandResultOutboxItem struct {
	ID           int64
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

// Runtime manages the long-lived agent connection and command processing loop.
type Runtime struct {
	opts Options

	mu       sync.Mutex
	conn     *websocket.Conn
	statMu   sync.RWMutex
	statSnap Status
}

func NewRuntime(opts Options) *Runtime {
	return &Runtime{opts: opts}
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
	r.statSnap.Connected = connected
	r.statSnap.LastEvent = event
	if !connected {
		r.statSnap.Transport = ""
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

// Reload forces the active connection to close, causing a reconnect with updated config.
func (r *Runtime) Reload() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn != nil {
		_ = r.conn.Close()
		r.conn = nil
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
		// Respeita handshakeEnabled vindo do servidor (antes era forcado true).
		// O secure handshake so e executado se o backend enviar HandshakeChallenge.

		if cfg.NatsUseWssExternal {
			if cfg.NatsServerHost == "" {
				r.logf("[security][nats-wss] natsUseWssExternal=true sem natsServerHost")
			} else if externalWSS, err := buildExternalNATSWSSURL(cfg.NatsServerHost); err != nil {
				r.logf("[security][nats-wss] host externo invalido (natsServerHost=%s): %v", cfg.NatsServerHost, err)
			} else {
				cfg.NatsWsServer = externalWSS
				cfg.NatsServer = ""
				r.logf("[security][nats-wss] endpoint externo aplicado: %s", cfg.NatsWsServer)
			}
		}

		if cfg.NatsServerHost != "" {
			if overridden, err := rewriteNATSHost(cfg.NatsServer, cfg.NatsServerHost); err != nil {
				r.logf("[security][nats] host override invalido (natsServerHost=%s): %v", cfg.NatsServerHost, err)
			} else if overridden != "" && overridden != cfg.NatsServer {
				r.logf("[security][nats] host override aplicado para nats://")
				cfg.NatsServer = overridden
			}
			if overridden, err := rewriteNATSHost(cfg.NatsWsServer, cfg.NatsServerHost); err != nil {
				r.logf("[security][nats-wss] host override invalido (natsServerHost=%s): %v", cfg.NatsServerHost, err)
			} else if overridden != "" && overridden != cfg.NatsWsServer {
				r.logf("[security][nats-wss] host override aplicado para wss://")
				cfg.NatsWsServer = overridden
			}
		}

		// Auto-derivar endpoints NATS a partir de NatsServerHost quando configurado.
		// NATS nativo: nats://<host>:4222 (porta padrao)
		// NATS WSS:    wss://<host>/nats/ (via Nginx proxy)
		if cfg.NatsServerHost != "" {
			if cfg.NatsServer == "" {
				derived := "nats://" + cfg.NatsServerHost + ":4222"
				cfg.NatsServer = derived
				r.logf("[transport][nats] auto-derivado: %s", cfg.NatsServer)
			}
			if cfg.NatsWsServer == "" {
				if externalWSS, err := buildExternalNATSWSSURL(cfg.NatsServerHost); err == nil {
					cfg.NatsWsServer = externalWSS
					r.logf("[transport][nats-wss] auto-derivado: %s", cfg.NatsWsServer)
				}
			}
		}

		if cfg.ApiServer != "" && cfg.ApiScheme != "http" && cfg.ApiScheme != "https" {
			r.logf("configuracao de agente ignorada: apiScheme invalido")
			cfg.ApiServer = ""
		}

		if err := validateTransportSecurity(cfg); err != nil {
			r.logf("configuracao de agente insegura: %v", err)
			r.waitOrStop(ctx, reconnectBase)
			continue
		}

		if cfg.ApiServer == "" && cfg.NatsServer == "" && cfg.NatsWsServer == "" {
			r.logf("configuracao de agente ausente: nenhum servidor configurado")
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
	//   1. NATS nativo (natsServerHost → apiServer fallback)
	//   2. NATS WSS    (natsServerHost → apiServer fallback)
	//   3. SignalR     (ultimo recurso)
	var attempts []func() error
	var labels []string

	// Extrai host da API para usar como fallback nos endpoints NATS.
	apiHost := extractHostFromServer(cfg.ApiServer)
	canonical := validateCanonicalNATSContext(cfg) == nil

	// ── NATS nativo ──
	if canonical {
		if cfg.NatsServer != "" {
			labels = append(labels, "nats")
			server := cfg.NatsServer
			attempts = append(attempts, func() error {
				return r.runNATSSession(ctx, cfg, server, "nats", connectAttemptTimeout)
			})
		}
		// Fallback: usa host da API + porta 4222
		if apiHost != "" && (cfg.NatsServer == "" || cfg.NatsServerHost == "" || cfg.NatsServerHost != apiHost) {
			fallbackNats := "nats://" + apiHost + ":4222"
			if fallbackNats != cfg.NatsServer {
				labels = append(labels, "nats (api-fallback)")
				server := fallbackNats
				attempts = append(attempts, func() error {
					return r.runNATSSession(ctx, cfg, server, "nats", connectAttemptTimeout)
				})
				r.logf("[transport][nats] fallback via apiServer: %s", fallbackNats)
			}
		}
	}

	// ── NATS WSS ──
	if canonical {
		if cfg.NatsWsServer != "" {
			labels = append(labels, "nats-wss")
			server := cfg.NatsWsServer
			attempts = append(attempts, func() error {
				return r.runNATSSession(ctx, cfg, server, "nats-wss", connectAttemptTimeout)
			})
		}
		// Fallback: usa host da API + path /nats/
		if apiHost != "" {
			if fallbackWSS, err := buildExternalNATSWSSURL(apiHost); err == nil && fallbackWSS != cfg.NatsWsServer {
				labels = append(labels, "nats-wss (api-fallback)")
				server := fallbackWSS
				attempts = append(attempts, func() error {
					return r.runNATSSession(ctx, cfg, server, "nats-wss", connectAttemptTimeout)
				})
				r.logf("[transport][nats-wss] fallback via apiServer: %s", fallbackWSS)
			}
		}
	}

	// ── SignalR ──
	if cfg.ApiServer != "" && (cfg.ApiScheme == "http" || cfg.ApiScheme == "https") {
		labels = append(labels, "signalr")
		attempts = append(attempts, func() error {
			if strings.TrimSpace(cfg.AuthToken) == "" || strings.TrimSpace(cfg.AgentID) == "" {
				return fmt.Errorf("token/agentId ausentes para SignalR")
			}
			return r.runSignalRSession(ctx, cfg, connectAttemptTimeout)
		})
	}

	if len(attempts) == 0 {
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

func (r *Runtime) runSignalRSession(ctx context.Context, cfg Config, connectTimeout time.Duration) error {
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	conn, observedTLSHash, err := r.connectSignalR(connectCtx, cfg, connectTimeout)
	cancel()
	if err != nil {
		return err
	}
	defer conn.Close()

	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.conn == conn {
			r.conn = nil
		}
		r.mu.Unlock()
	}()

	if err := r.sendHandshake(conn); err != nil {
		r.logf("[security][signalr] falha de handshake: envio inicial: %v", err)
		return err
	}
	if err := r.waitHandshakeAck(conn, connectTimeout); err != nil {
		r.logf("[security][signalr] falha de handshake: ack inicial: %v", err)
		return err
	}
	if err := r.completeSecureHandshake(ctx, conn, observedTLSHash, connectTimeout); err != nil {
		r.logf("[security][signalr] falha de handshake seguro: %v", err)
		return err
	}

	ipAddr := detectLocalIP()
	if err := r.invoke(conn, "RegisterAgent", cfg.AgentID, ipAddr); err != nil {
		r.setStatus(false, "RegisterAgent falhou: "+err.Error())
		return fmt.Errorf("RegisterAgent falhou: %w", err)
	}
	r.setStatusConnected(cfg.AgentID, cfg.ApiServer, "signalr")
	r.logf("RegisterAgent enviado (agentId=%s, ip=%s)", cfg.AgentID, ipAddr)

	// Goroutine dedicada de leitura: bloqueia em ReadMessage independentemente
	// do ticker, garantindo que o heartbeat dispare no tempo certo mesmo com
	// EcoQoS / IDLE_PRIORITY_CLASS ativo no background.
	type wsMsg struct {
		data []byte
		err  error
	}
	msgCh := make(chan wsMsg, 8)
	readerDone := make(chan struct{})
	defer close(readerDone)

	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			select {
			case msgCh <- wsMsg{msg, err}:
			case <-readerDone:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	heartbeatTicker := time.NewTicker(heartbeatEvery)
	defer heartbeatTicker.Stop()
	drainTicker := time.NewTicker(commandResultDrainEvery)
	defer drainTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeatTicker.C:
			if err := r.invoke(conn, "Heartbeat", cfg.AgentID, ipAddr); err != nil {
				return fmt.Errorf("heartbeat falhou: %w", err)
			}
		case m := <-msgCh:
			if m.err != nil {
				return m.err
			}
			if err := r.handleSignalRPayload(ctx, conn, m.data); err != nil {
				r.logf("falha ao tratar mensagem do hub: %v", err)
			}
		case <-drainTicker.C:
			r.drainCommandResultOutbox(ctx, "signalr", func(item CommandResultOutboxItem) error {
				return r.invoke(conn, "CommandResult", item.CommandID, item.ExitCode, item.Output, item.ErrorMessage)
			})
		}
	}
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
	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("X-Agent-ID", agentID)
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

func (r *Runtime) connectSignalR(ctx context.Context, cfg Config, connectTimeout time.Duration) (*websocket.Conn, string, error) {
	wsScheme := "ws"
	if cfg.ApiScheme == "https" {
		wsScheme = "wss"
	}

	wsURL := url.URL{
		Scheme: wsScheme,
		Host:   cfg.ApiServer,
		Path:   hubPath,
	}
	query := wsURL.Query()
	query.Set("access_token", strings.TrimSpace(cfg.AuthToken))
	wsURL.RawQuery = query.Encode()

	header := http.Header{}
	header.Set("X-Agent-ID", cfg.AgentID)

	if connectTimeout <= 0 {
		connectTimeout = handshakeTimeout
	}

	observedTLSHash := ""
	if strings.EqualFold(strings.TrimSpace(cfg.ApiScheme), "https") {
		hash, err := observeTLSPeerCertHash(ctx, cfg.ApiServer, connectTimeout)
		if err != nil {
			r.logf("[security][signalr] falha ao observar hash TLS: %v", err)
		} else {
			observedTLSHash = normalizeTLSCertHash(hash)
			if observedTLSHash != "" {
				header.Set("X-Agent-Tls-Cert-Hash", observedTLSHash)
			}
		}
		r.logf("[security][signalr] hash TLS observado=%s esperado=%s", observedTLSHash, normalizeTLSCertHash(cfg.ApiTLSCertHash))
		if cfg.EnforceTLSHashValidation && observedTLSHash != "" && normalizeTLSCertHash(cfg.ApiTLSCertHash) != "" && observedTLSHash != normalizeTLSCertHash(cfg.ApiTLSCertHash) {
			r.logf("[security][signalr] mismatch detectado; enviando tls-mismatch")
			r.reportTLSMismatch(cfg, "api", observedTLSHash)
		}

		if err := evaluateTLSPinPolicy("signalr", observedTLSHash, cfg.ApiTLSCertHash, cfg.EnforceTLSHashValidation); err != nil {
			r.logf("[security][signalr] bloqueado: %v", err)
			return nil, "", err
		}
		if cfg.EnforceTLSHashValidation {
			r.logf("[security][signalr] validacao TLS hash OK")
		} else {
			r.logf("[security][signalr] validacao TLS hash em modo compativel (enforce=false)")
		}
	}

	dialer := tlsutil.NewWebSocketDialer(connectTimeout)
	conn, resp, err := dialer.DialContext(ctx, wsURL.String(), header)
	if err != nil {
		if resp != nil {
			return nil, "", fmt.Errorf("falha ao conectar hub (%s): %w", resp.Status, err)
		}
		return nil, "", err
	}
	return conn, observedTLSHash, nil
}

func (r *Runtime) completeSecureHandshake(ctx context.Context, conn *websocket.Conn, observedTLSHash string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = handshakeTimeout
	}
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()

	deadline := time.Now().Add(timeout)
	challengeSeen := false

	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return err
		}

		_, payload, err := conn.ReadMessage()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				if challengeSeen {
					return fmt.Errorf("timeout aguardando HandshakeAck")
				}
				return nil
			}
			return err
		}

		for _, rec := range splitSignalRRecords(payload) {
			msg := map[string]any{}
			if err := json.Unmarshal([]byte(rec), &msg); err != nil {
				continue
			}

			t, _ := toInt(msg["type"])
			switch t {
			case 1:
				target := strings.TrimSpace(toString(msg["target"]))
				if strings.EqualFold(target, "HandshakeChallenge") {
					challengeSeen = true
					if strings.TrimSpace(observedTLSHash) == "" {
						return fmt.Errorf("HandshakeChallenge recebido sem hash TLS observado")
					}
					if err := r.invoke(conn, "SecureHandshakeAsync", observedTLSHash); err != nil {
						return fmt.Errorf("falha ao responder SecureHandshakeAsync: %w", err)
					}
					continue
				}
				if strings.EqualFold(target, "HandshakeAck") {
					success, message := parseHandshakeAck(msg["arguments"])
					if !success {
						if strings.TrimSpace(message) == "" {
							message = "handshake rejeitado pelo servidor"
						}
						return errors.New(message)
					}
					r.logf("secure handshake confirmado pelo servidor")
					return nil
				}
			case 6:
				continue
			case 7:
				reason, _ := msg["error"].(string)
				if strings.TrimSpace(reason) == "" {
					reason = "servidor encerrou a conexao"
				}
				return errors.New(reason)
			}

			if err := r.handleSignalRPayload(ctx, conn, []byte(rec)); err != nil {
				return err
			}
			if !challengeSeen {
				return nil
			}
		}
	}
}

func (r *Runtime) executeAndRespond(ctx context.Context, conn *websocket.Conn, cmdID, cmdType string, payload any) {
	exitCode, output, errText := r.executeCommand(ctx, normalizeCommandType(cmdType), payload)
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes] + "\n... output truncado ..."
	}
	if err := r.invoke(conn, "CommandResult", cmdID, exitCode, output, errText); err != nil {
		r.logf("falha ao enviar CommandResult cmdId=%s: %v", cmdID, err)
		r.enqueueCommandResultOutbox("signalr", cmdID, exitCode, output, errText, err)
		return
	}
	r.logf("CommandResult enviado cmdId=%s exitCode=%d", cmdID, exitCode)
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

func (r *Runtime) enqueueCommandResultOutbox(transport, cmdID string, exitCode int, output, errText string, sendErr error) {
	if r.opts.EnqueueCommandResultOutbox == nil {
		return
	}
	errMsg := ""
	if sendErr != nil {
		errMsg = sendErr.Error()
	}
	if err := r.opts.EnqueueCommandResultOutbox(strings.TrimSpace(transport), strings.TrimSpace(cmdID), exitCode, output, errText, errMsg); err != nil {
		r.logf("falha ao enfileirar CommandResult offline transport=%s cmdId=%s: %v", strings.TrimSpace(transport), strings.TrimSpace(cmdID), err)
		return
	}
	r.logf("CommandResult enfileirado para retry offline transport=%s cmdId=%s", strings.TrimSpace(transport), strings.TrimSpace(cmdID))
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

// validateTransportSecurity enforces secure transport in non-local endpoints.
// A local dev override exists via DISCOVERY_ALLOW_INSECURE_TRANSPORT.
