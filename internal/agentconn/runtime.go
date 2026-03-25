package agentconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
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
	CommandType string `json:"commandType"`
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
	ApiScheme    string
	ApiServer    string
	NatsServer   string
	NatsWsServer string
	AuthToken    string
	AgentID      string
}

// Options defines dependencies injected by the app layer.
type Options struct {
	LoadConfig func() Config
	Logf       func(format string, args ...any)
	OnSyncPing func(SyncPing)
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
		cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)
		cfg.AgentID = strings.TrimSpace(cfg.AgentID)

		if cfg.ApiServer != "" && cfg.ApiScheme != "http" && cfg.ApiScheme != "https" {
			r.logf("configuracao de agente ignorada: apiScheme invalido")
			cfg.ApiServer = ""
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
		r.logf("fallback: %s falhou: %v", labels[i], err)
	}
	return lastErr
}

func (r *Runtime) runSignalRSession(ctx context.Context, cfg Config, connectTimeout time.Duration) error {
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	conn, err := r.connectSignalR(connectCtx, cfg, connectTimeout)
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
		return err
	}
	if err := r.waitHandshakeAck(conn, connectTimeout); err != nil {
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
		env.CommandType = strings.TrimSpace(env.CommandType)
		if env.CommandID == "" {
			r.logf("comando NATS ignorado: commandId vazio")
			return
		}

		go func(c natsCommandEnvelope) {
			exitCode, output, errText := executeCommand(ctx, c.CommandType, c.Payload)
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

func (r *Runtime) connectSignalR(ctx context.Context, cfg Config, connectTimeout time.Duration) (*websocket.Conn, error) {
	wsScheme := "ws"
	if cfg.ApiScheme == "https" {
		wsScheme = "wss"
	}

	values := url.Values{}
	values.Set("access_token", cfg.AuthToken)

	wsURL := url.URL{
		Scheme:   wsScheme,
		Host:     cfg.ApiServer,
		Path:     hubPath,
		RawQuery: values.Encode(),
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+cfg.AuthToken)
	header.Set("X-Agent-ID", cfg.AgentID)

	if connectTimeout <= 0 {
		connectTimeout = handshakeTimeout
	}
	dialer := websocket.Dialer{HandshakeTimeout: connectTimeout}
	conn, resp, err := dialer.DialContext(ctx, wsURL.String(), header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("falha ao conectar hub (%s): %w", resp.Status, err)
		}
		return nil, err
	}
	return conn, nil
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
	exitCode, output, errText := executeCommand(ctx, cmdType, payload)
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes] + "\n... output truncado ..."
	}
	if err := r.invoke(conn, "CommandResult", cmdID, exitCode, output, errText); err != nil {
		r.logf("falha ao enviar CommandResult cmdId=%s: %v", cmdID, err)
		return
	}
	r.logf("CommandResult enviado cmdId=%s exitCode=%d", cmdID, exitCode)
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
