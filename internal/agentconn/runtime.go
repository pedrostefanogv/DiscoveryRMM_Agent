package agentconn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"
)

const (
	hubPath          = "/hubs/agent"
	heartbeatEvery   = 30 * time.Second
	reconnectBase    = 10 * time.Second
	reconnectJitter  = 5 * time.Second
	handshakeTimeout = 10 * time.Second
	maxOutputBytes   = 1 << 20

	connectAttemptTimeout = 5 * time.Second
	natsCommandTpl        = "agent.%s.command"
	natsHeartbeatTpl      = "agent.%s.heartbeat"
	natsResultTpl         = "agent.%s.result"
	natsSyncPingTpl       = "agent.%s.sync.ping"
	natsDashboardTopic    = "dashboard.events"
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
}

// Options defines dependencies injected by the app layer.
type Options struct {
	LoadConfig      func() Config
	Logf            func(format string, args ...any)
	OnSyncPing      func(SyncPing)
	HandleCommand   func(parent context.Context, cmdType string, payload any) (handled bool, exitCode int, output string, errText string)
	OnCommandOutput func(cmdType string, output string, errText string)
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
		if !cfg.HandshakeEnabled {
			// Mantemos o secure handshake habilitado por padrao para evitar downgrade.
			cfg.HandshakeEnabled = true
		}

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
	var attempts []func() error
	var labels []string

	if cfg.NatsServer != "" {
		labels = append(labels, "nats")
		server := cfg.NatsServer
		attempts = append(attempts, func() error {
			if strings.TrimSpace(cfg.AgentID) == "" {
				return fmt.Errorf("agentId ausente para NATS")
			}
			return r.runNATSSession(ctx, cfg, server, "nats", connectAttemptTimeout)
		})
	}

	if cfg.NatsWsServer != "" {
		labels = append(labels, "nats-wss")
		server := cfg.NatsWsServer
		attempts = append(attempts, func() error {
			if strings.TrimSpace(cfg.AgentID) == "" {
				return fmt.Errorf("agentId ausente para NATS WS")
			}
			return r.runNATSSession(ctx, cfg, server, "nats-wss", connectAttemptTimeout)
		})
	}

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
		}
	}
}

func (r *Runtime) runNATSSession(ctx context.Context, cfg Config, server, transportLabel string, connectTimeout time.Duration) error {
	if !guidPattern.MatchString(strings.TrimSpace(cfg.AgentID)) {
		return fmt.Errorf("agentId invalido para NATS: esperado GUID")
	}

	natsURL, err := normalizeNATSURL(server)
	if err != nil {
		return err
	}

	parsedNATSURL, err := url.Parse(natsURL)
	if err != nil {
		return fmt.Errorf("url NATS invalida: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(parsedNATSURL.Scheme), "wss") {
		observedTLSHash, observeErr := observeTLSPeerCertHash(ctx, parsedNATSURL.Host, connectTimeout)
		if observeErr != nil {
			r.logf("[security][nats-wss] falha ao observar hash TLS: %v", observeErr)
		} else {
			observedTLSHash = normalizeTLSCertHash(observedTLSHash)
		}
		r.logf("[security][nats-wss] hash TLS observado=%s esperado=%s", observedTLSHash, normalizeTLSCertHash(cfg.NatsTLSCertHash))
		if cfg.EnforceTLSHashValidation && observedTLSHash != "" && normalizeTLSCertHash(cfg.NatsTLSCertHash) != "" && observedTLSHash != normalizeTLSCertHash(cfg.NatsTLSCertHash) {
			r.logf("[security][nats-wss] mismatch detectado; enviando tls-mismatch")
			r.reportTLSMismatch(cfg, "nats", observedTLSHash)
		}
		if err := evaluateTLSPinPolicy("nats-wss", observedTLSHash, cfg.NatsTLSCertHash, cfg.EnforceTLSHashValidation); err != nil {
			r.logf("[security][nats-wss] bloqueado: %v", err)
			return err
		}
		if cfg.EnforceTLSHashValidation {
			r.logf("[security][nats-wss] validacao TLS hash OK")
		} else {
			r.logf("[security][nats-wss] validacao TLS hash em modo compativel (enforce=false)")
		}
	}

	opts := []nats.Option{
		nats.Name("discovery-agent-" + cfg.AgentID),
		nats.Timeout(connectTimeout),
		nats.ReconnectWait(reconnectBase),
		nats.MaxReconnects(-1),
	}
	if strings.TrimSpace(cfg.AuthToken) != "" {
		opts = append(opts, nats.Token(strings.TrimSpace(cfg.AuthToken)))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return fmt.Errorf("falha ao conectar NATS: %w", err)
	}
	defer nc.Close()

	ipAddr := detectLocalIP()
	cmdSubject := fmt.Sprintf(natsCommandTpl, cfg.AgentID)
	hbSubject := fmt.Sprintf(natsHeartbeatTpl, cfg.AgentID)
	resultSubject := fmt.Sprintf(natsResultTpl, cfg.AgentID)
	syncPingSubject := fmt.Sprintf(natsSyncPingTpl, cfg.AgentID)

	if err := publishJSON(nc, hbSubject, natsHeartbeatEnvelope{IPAddress: ipAddr, AgentVersion: "discovery"}); err != nil {
		r.logf("falha ao publicar heartbeat inicial: %v", err)
	}
	_ = publishJSON(nc, natsDashboardTopic, natsDashboardEvent{
		EventType: "agent_connected",
		Data: map[string]any{
			"agentId":   cfg.AgentID,
			"transport": transportLabel,
			"server":    natsURL,
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	r.setStatusConnected(cfg.AgentID, natsURL, transportLabel)
	r.logf("agente conectado ao NATS (subject=%s, syncSubject=%s)", cmdSubject, syncPingSubject)

	_, err = nc.Subscribe(cmdSubject, func(msg *nats.Msg) {
		var env natsCommandEnvelope
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			r.logf("mensagem de comando NATS invalida: %v", err)
			return
		}
		env.CommandID = strings.TrimSpace(env.CommandID)
		if env.CommandID == "" {
			r.logf("comando NATS ignorado: commandId vazio")
			return
		}

		go func(c natsCommandEnvelope) {
			exitCode, output, errText := r.executeCommand(ctx, normalizeCommandType(c.CommandType), c.Payload)
			if len(output) > maxOutputBytes {
				output = output[:maxOutputBytes] + "\n... output truncado ..."
			}

			res := natsResultEnvelope{
				CommandID:    c.CommandID,
				ExitCode:     exitCode,
				Output:       output,
				ErrorMessage: errText,
			}
			if err := publishJSON(nc, resultSubject, res); err != nil {
				r.logf("falha ao publicar result (cmd=%s): %v", c.CommandID, err)
				return
			}

			_ = publishJSON(nc, natsDashboardTopic, natsDashboardEvent{
				EventType: "command_result",
				Data: map[string]any{
					"agentId":   cfg.AgentID,
					"commandId": c.CommandID,
					"exitCode":  exitCode,
				},
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
			r.logf("result NATS publicado cmdId=%s exitCode=%d", c.CommandID, exitCode)
		}(env)
	})
	if err != nil {
		return fmt.Errorf("falha ao inscrever no subject de comando: %w", err)
	}

	_, err = nc.Subscribe(syncPingSubject, func(msg *nats.Msg) {
		var ping SyncPing
		if err := json.Unmarshal(msg.Data, &ping); err != nil {
			r.logf("mensagem de sync ping NATS invalida: %v", err)
			return
		}
		r.emitSyncPing(ping)
	})
	if err != nil {
		return fmt.Errorf("falha ao inscrever no subject de sync ping: %w", err)
	}

	heartbeatTicker := time.NewTicker(heartbeatEvery)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = publishJSON(nc, natsDashboardTopic, natsDashboardEvent{
				EventType: "agent_disconnected",
				Data: map[string]any{
					"agentId":   cfg.AgentID,
					"transport": transportLabel,
				},
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
			return nil
		case <-heartbeatTicker.C:
			if err := publishJSON(nc, hbSubject, natsHeartbeatEnvelope{IPAddress: ipAddr, AgentVersion: "discovery"}); err != nil {
				return fmt.Errorf("heartbeat NATS falhou: %w", err)
			}
		}
	}
}

func publishJSON(nc *nats.Conn, subject string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return nc.Publish(subject, b)
}

func normalizeNATSURL(server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", fmt.Errorf("servidor NATS vazio")
	}
	if strings.Contains(server, "://") {
		u, err := url.Parse(server)
		if err != nil {
			return "", fmt.Errorf("url NATS invalida: %w", err)
		}
		scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
		switch scheme {
		case "nats", "ws", "wss":
			if strings.TrimSpace(u.Host) == "" {
				return "", fmt.Errorf("url NATS invalida: host ausente")
			}
			return u.String(), nil
		default:
			return "", fmt.Errorf("url NATS invalida: scheme %s nao suportado", scheme)
		}
	}
	return "nats://" + server, nil
}

func normalizeTLSCertHash(raw string) string {
	h := strings.ToUpper(strings.TrimSpace(raw))
	h = strings.ReplaceAll(h, ":", "")
	h = strings.ReplaceAll(h, " ", "")
	return h
}

func evaluateTLSPinPolicy(transport, observedHash, expectedHash string, enforce bool) error {
	transport = strings.TrimSpace(strings.ToLower(transport))
	observedHash = normalizeTLSCertHash(observedHash)
	expectedHash = normalizeTLSCertHash(expectedHash)

	if !enforce {
		return nil
	}
	if expectedHash == "" {
		return fmt.Errorf("politica de seguranca bloqueou %s: hash TLS esperado ausente com enforce=true", transport)
	}
	if observedHash == "" {
		return fmt.Errorf("politica de seguranca bloqueou %s: hash TLS observado ausente", transport)
	}
	if observedHash != expectedHash {
		return fmt.Errorf("politica de seguranca bloqueou %s: hash TLS divergente (observado=%s esperado=%s)", transport, observedHash, expectedHash)
	}
	return nil
}

func rewriteNATSHost(server, newHost string) (string, error) {
	server = strings.TrimSpace(server)
	newHost = strings.TrimSpace(newHost)
	if server == "" || newHost == "" {
		return server, nil
	}

	natsURL, err := normalizeNATSURL(server)
	if err != nil {
		return "", err
	}

	u, err := url.Parse(natsURL)
	if err != nil {
		return "", err
	}

	port := ""
	if _, p, splitErr := net.SplitHostPort(strings.TrimSpace(u.Host)); splitErr == nil {
		port = p
	}

	hostOnly := newHost
	if strings.Contains(newHost, ":") {
		if h, p, splitErr := net.SplitHostPort(newHost); splitErr == nil {
			hostOnly = strings.TrimSpace(h)
			if strings.TrimSpace(p) != "" {
				port = strings.TrimSpace(p)
			}
		}
	}
	hostOnly = strings.TrimSpace(strings.Trim(hostOnly, "[]"))
	if hostOnly == "" {
		return "", fmt.Errorf("natsServerHost invalido")
	}

	if strings.TrimSpace(port) != "" {
		u.Host = net.JoinHostPort(hostOnly, port)
	} else {
		u.Host = hostOnly
	}

	return u.String(), nil
}

func buildExternalNATSWSSURL(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("natsServerHost vazio")
	}
	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err != nil {
			return "", err
		}
		host = strings.TrimSpace(u.Host)
	}
	host = strings.Trim(strings.TrimSpace(host), "/")
	if host == "" {
		return "", fmt.Errorf("natsServerHost invalido")
	}
	return "wss://" + host, nil
}

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
	endpoint := apiScheme + "://" + apiServer + "/api/agent-auth/me/tls-mismatch"
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
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
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

	header := http.Header{}
	header.Set("Authorization", "Bearer "+cfg.AuthToken)
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

	dialer := websocket.Dialer{HandshakeTimeout: connectTimeout}
	conn, resp, err := dialer.DialContext(ctx, wsURL.String(), header)
	if err != nil {
		if resp != nil {
			return nil, "", fmt.Errorf("falha ao conectar hub (%s): %w", resp.Status, err)
		}
		return nil, "", err
	}
	return conn, observedTLSHash, nil
}

func observeTLSPeerCertHash(ctx context.Context, apiServer string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = handshakeTimeout
	}

	address, serverName, err := normalizeTLSTarget(apiServer)
	if err != nil {
		return "", err
	}

	tlsDialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: timeout},
		Config: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: serverName,
		},
	}

	conn, err := tlsDialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return "", fmt.Errorf("conexao TLS invalida para captura de certificado")
	}

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", fmt.Errorf("certificado TLS remoto ausente")
	}

	sum := sha256.Sum256(state.PeerCertificates[0].Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:])), nil
}

func normalizeTLSTarget(apiServer string) (string, string, error) {
	target := strings.TrimSpace(apiServer)
	if target == "" {
		return "", "", fmt.Errorf("apiServer vazio")
	}

	if strings.Contains(target, "://") {
		u, err := url.Parse(target)
		if err != nil {
			return "", "", fmt.Errorf("apiServer invalido: %w", err)
		}
		target = strings.TrimSpace(u.Host)
	}

	host := target
	port := "443"
	if h, p, err := net.SplitHostPort(target); err == nil {
		host = strings.TrimSpace(h)
		port = strings.TrimSpace(p)
	} else {
		target = strings.Trim(target, "[]")
		if target == "" {
			return "", "", fmt.Errorf("host TLS vazio")
		}
		host = target
	}

	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return "", "", fmt.Errorf("host TLS vazio")
	}
	if port == "" {
		port = "443"
	}

	return net.JoinHostPort(host, port), host, nil
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

func parseHandshakeAck(raw any) (bool, string) {
	arr, ok := raw.([]any)
	if !ok {
		return true, ""
	}
	if len(arr) == 0 {
		return true, ""
	}

	success := true
	message := ""

	if b, ok := arr[0].(bool); ok {
		success = b
	}
	if len(arr) >= 2 {
		message = strings.TrimSpace(toString(arr[1]))
	}

	if m, ok := arr[0].(map[string]any); ok {
		if b, ok := m["success"].(bool); ok {
			success = b
		}
		if strings.TrimSpace(message) == "" {
			message = strings.TrimSpace(toString(m["message"]))
		}
	}

	return success, message
}

func (r *Runtime) sendHandshake(conn *websocket.Conn) error {
	// SignalR JSON protocol handshake frame.
	return conn.WriteMessage(websocket.TextMessage, []byte("{\"protocol\":\"json\",\"version\":1}\x1e"))
}

func (r *Runtime) waitHandshakeAck(conn *websocket.Conn, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = handshakeTimeout
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()
	_, message, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	records := splitSignalRRecords(message)
	for _, rec := range records {
		if strings.TrimSpace(rec) == "{}" || strings.TrimSpace(rec) == "" {
			return nil
		}
		var hs map[string]any
		if json.Unmarshal([]byte(rec), &hs) == nil {
			if e, ok := hs["error"].(string); ok && strings.TrimSpace(e) != "" {
				return fmt.Errorf("handshake rejeitado: %s", e)
			}
		}
	}
	return nil
}

func (r *Runtime) invoke(conn *websocket.Conn, target string, args ...any) error {
	frame := map[string]any{
		"type":      1,
		"target":    target,
		"arguments": args,
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	payload = append(payload, 0x1e)
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func (r *Runtime) handleSignalRPayload(ctx context.Context, conn *websocket.Conn, payload []byte) error {
	for _, rec := range splitSignalRRecords(payload) {
		if strings.TrimSpace(rec) == "" || strings.TrimSpace(rec) == "{}" {
			continue
		}

		msg := map[string]any{}
		if err := json.Unmarshal([]byte(rec), &msg); err != nil {
			continue
		}

		t, _ := toInt(msg["type"])
		switch t {
		case 1:
			target, _ := msg["target"].(string)
			if strings.EqualFold(target, "ExecuteCommand") {
				cmdID, cmdType, cmdPayload := parseExecuteArgs(msg["arguments"])
				if cmdID == "" {
					r.logf("ExecuteCommand ignorado: cmdId vazio")
					continue
				}
				go r.executeAndRespond(ctx, conn, cmdID, cmdType, cmdPayload)
				continue
			}
			if strings.EqualFold(target, "SyncPing") {
				ping, ok := parseSyncPingArgs(msg["arguments"])
				if !ok {
					r.logf("SyncPing ignorado: payload invalido")
					continue
				}
				r.emitSyncPing(ping)
				continue
			}
		case 6:
			// Ping frame from server.
			continue
		case 7:
			reason, _ := msg["error"].(string)
			if strings.TrimSpace(reason) == "" {
				reason = "servidor encerrou a conexao"
			}
			return errors.New(reason)
		}
	}
	return nil
}

func (r *Runtime) executeAndRespond(ctx context.Context, conn *websocket.Conn, cmdID, cmdType string, payload any) {
	exitCode, output, errText := r.executeCommand(ctx, normalizeCommandType(cmdType), payload)
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes] + "\n... output truncado ..."
	}
	if err := r.invoke(conn, "CommandResult", cmdID, exitCode, output, errText); err != nil {
		r.logf("falha ao enviar CommandResult cmdId=%s: %v", cmdID, err)
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

func executeCommand(parent context.Context, cmdType string, payload any) (int, string, string) {
	timeout := 2 * time.Minute
	command, args, pTimeout := parsePayload(payload)
	if pTimeout > 0 {
		timeout = pTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmdType = strings.ToLower(strings.TrimSpace(cmdType))
	var cmd *exec.Cmd

	switch cmdType {
	case "powershell", "ps":
		if command == "" {
			return 2, "", "payload sem comando powershell"
		}
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	case "cmd", "shell":
		if command == "" {
			return 2, "", "payload sem comando cmd/shell"
		}
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	case "exec", "process", "winget":
		if command == "" {
			return 2, "", "payload sem executavel"
		}
		cmd = exec.CommandContext(ctx, command, args...)
	default:
		if command == "" {
			return 2, "", "tipo de comando desconhecido e payload sem comando"
		}
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	}

	out, err := cmd.CombinedOutput()
	output := string(out)
	if err == nil {
		return 0, output, ""
	}

	exitCode := 1
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	}
	errText := err.Error()
	if ctx.Err() == context.DeadlineExceeded {
		errText = "timeout excedido"
	}
	return exitCode, output, errText
}

func parsePayload(payload any) (string, []string, time.Duration) {
	if payload == nil {
		return "", nil, 0
	}

	if s, ok := payload.(string); ok {
		return strings.TrimSpace(s), nil, 0
	}

	m, ok := payload.(map[string]any)
	if !ok {
		return "", nil, 0
	}

	command := strings.TrimSpace(toString(m["command"]))
	if command == "" {
		command = strings.TrimSpace(toString(m["script"]))
	}
	args := toStringSlice(m["args"])
	timeoutSec, _ := toInt(m["timeoutSec"])
	if timeoutSec <= 0 {
		timeoutSec, _ = toInt(m["timeoutSeconds"])
	}
	if timeoutSec > 0 {
		return command, args, time.Duration(timeoutSec) * time.Second
	}
	return command, args, 0
}

func parseExecuteArgs(raw any) (cmdID, cmdType string, payload any) {
	arr, ok := raw.([]any)
	if ok {
		if len(arr) >= 3 {
			return toString(arr[0]), toString(arr[1]), arr[2]
		}
		if len(arr) == 1 {
			if m, ok := arr[0].(map[string]any); ok {
				return strings.TrimSpace(toString(m["cmdId"])), strings.TrimSpace(toString(m["cmdType"])), m["payload"]
			}
		}
	}
	return "", "", nil
}

func normalizeCommandType(raw any) string {
	return strings.ToLower(strings.TrimSpace(toString(raw)))
}

func parseSyncPingArgs(raw any) (SyncPing, bool) {
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return SyncPing{}, false
	}
	first, ok := arr[0].(map[string]any)
	if !ok {
		return SyncPing{}, false
	}

	ping := SyncPing{
		EventID:          strings.TrimSpace(toString(first["eventId"])),
		AgentID:          strings.TrimSpace(toString(first["agentId"])),
		EventType:        strings.TrimSpace(toString(first["eventType"])),
		Resource:         strings.TrimSpace(toString(first["resource"])),
		ScopeType:        strings.TrimSpace(toString(first["scopeType"])),
		ScopeID:          strings.TrimSpace(toString(first["scopeId"])),
		InstallationType: strings.TrimSpace(toString(first["installationType"])),
		Revision:         strings.TrimSpace(toString(first["revision"])),
		Reason:           strings.TrimSpace(toString(first["reason"])),
		ChangedAtUTC:     strings.TrimSpace(toString(first["changedAtUtc"])),
		CorrelationID:    strings.TrimSpace(toString(first["correlationId"])),
	}
	if ping.Resource == "" {
		return SyncPing{}, false
	}
	return ping, true
}

func splitSignalRRecords(data []byte) []string {
	parts := strings.Split(string(data), string([]byte{0x1e}))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return strings.Trim(string(b), "\"")
	}
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s := strings.TrimSpace(toString(item))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int32:
		return int(t), true
	case int64:
		return int(t), true
	case string:
		if strings.TrimSpace(t) == "" {
			return 0, false
		}
		var i int
		_, err := fmt.Sscanf(t, "%d", &i)
		if err == nil {
			return i, true
		}
	}
	return 0, false
}

func detectLocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return "127.0.0.1"
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
func validateTransportSecurity(cfg Config) error {
	if allowInsecureTransport() {
		return nil
	}

	if cfg.ApiServer != "" && !isLocalTarget(cfg.ApiServer) && strings.TrimSpace(strings.ToLower(cfg.ApiScheme)) != "https" {
		return fmt.Errorf("apiScheme deve ser https para endpoints remotos")
	}

	if strings.TrimSpace(cfg.NatsWsServer) != "" {
		natsURL, err := normalizeNATSURL(cfg.NatsWsServer)
		if err != nil {
			return err
		}
		u, err := url.Parse(natsURL)
		if err != nil {
			return fmt.Errorf("url NATS WS invalida: %w", err)
		}
		if !isLocalTarget(u.Host) && strings.ToLower(strings.TrimSpace(u.Scheme)) != "wss" {
			return fmt.Errorf("natsWsServer remoto deve usar wss")
		}
	}

	if strings.TrimSpace(cfg.NatsServer) != "" {
		natsURL, err := normalizeNATSURL(cfg.NatsServer)
		if err != nil {
			return err
		}
		u, err := url.Parse(natsURL)
		if err != nil {
			return fmt.Errorf("url NATS invalida: %w", err)
		}
		scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
		if !isLocalTarget(u.Host) {
			if scheme != "wss" {
				return fmt.Errorf("natsServer remoto deve usar transporte seguro (wss)")
			}
		}
	}

	return nil
}

func allowInsecureTransport() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("DISCOVERY_ALLOW_INSECURE_TRANSPORT")))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isLocalTarget(value string) bool {
	host := strings.TrimSpace(value)
	if host == "" {
		return false
	}

	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err == nil {
			host = strings.TrimSpace(u.Host)
		}
	}

	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else {
		if strings.HasPrefix(host, "[") {
			if idx := strings.Index(host, "]"); idx > 1 {
				host = host[1:idx]
			}
		} else if strings.Count(host, ":") == 1 {
			host = strings.SplitN(host, ":", 2)[0]
		}
	}

	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
