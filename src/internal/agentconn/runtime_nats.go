package agentconn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"discovery/internal/tlsutil"
)

// NATSCredentials representa a resposta de POST /api/v1/agent-auth/me/nats-credentials.
type NATSCredentials struct {
	NKey string `json:"nkey"`
	JWT  string `json:"jwt"`
}

// fetchNATSCredentials obtém credenciais NATS (JWT/NKey) do servidor.
// Usa o token Bearer padrão para autenticar no endpoint REST.
func (r *Runtime) fetchNATSCredentials(ctx context.Context, cfg Config) (*NATSCredentials, error) {
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	if apiServer == "" || token == "" {
		return nil, fmt.Errorf("configuracao API incompleta para nats-credentials")
	}
	if apiScheme != "http" && apiScheme != "https" {
		return nil, fmt.Errorf("apiScheme invalido para nats-credentials")
	}

	endpoint := apiScheme + "://" + apiServer + "/api/v1/agent-auth/me/nats-credentials"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("falha ao montar request nats-credentials: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if agentID := strings.TrimSpace(cfg.AgentID); agentID != "" {
		req.Header.Set("X-Agent-ID", agentID)
	}

	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao chamar nats-credentials: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("nats-credentials retornou HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var creds NATSCredentials
	if err := json.Unmarshal(body, &creds); err != nil {
		return nil, fmt.Errorf("resposta invalida de nats-credentials: %w", err)
	}
	if strings.TrimSpace(creds.NKey) == "" && strings.TrimSpace(creds.JWT) == "" {
		return nil, fmt.Errorf("nats-credentials retornou credenciais vazias")
	}
	return &creds, nil
}

// ─── NATS Session ──────────────────────────────────────────────────

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
	tokenOpts := append([]nats.Option{}, opts...)
	if strings.TrimSpace(cfg.AuthToken) != "" {
		tokenOpts = append(tokenOpts, nats.Token(strings.TrimSpace(cfg.AuthToken)))
	}

	nc, err := nats.Connect(natsURL, tokenOpts...)
	if err != nil {
		// Se falhou com token raw, tenta credenciais JWT/NKey via nats-credentials.
		creds, credsErr := r.fetchNATSCredentials(ctx, cfg)
		if credsErr != nil {
			r.logf("[transport][%s] nats-credentials indisponivel: %v", transportLabel, credsErr)
			return fmt.Errorf("falha ao conectar NATS: %w", err)
		}

		// Reconecta com credenciais JWT/NKey sem manter o token raw do agent.
		optsWithCreds := append([]nats.Option{}, opts...)
		jwtOption, cleanup, jwtErr := natsAuthOptionFromCredentials(creds)
		if jwtErr != nil {
			return fmt.Errorf("falha ao preparar credenciais NATS: %w", jwtErr)
		}
		if cleanup != nil {
			defer cleanup()
		}
		optsWithCreds = append(optsWithCreds, jwtOption)
		r.logf("[transport][%s] autenticando com credenciais JWT/NKey do servidor", transportLabel)

		nc, err = nats.Connect(natsURL, optsWithCreds...)
		if err != nil {
			return fmt.Errorf("falha ao conectar NATS (com credenciais): %w", err)
		}
	}
	defer nc.Close()

	ipAddr := detectLocalIP()
	subjects, err := resolveNATSSubjects(cfg)
	if err != nil {
		return err
	}

	if err := publishJSON(nc, subjects.Heartbeat, natsHeartbeatEnvelope{IPAddress: ipAddr, AgentVersion: "discovery"}); err != nil {
		r.logf("falha ao publicar heartbeat inicial: %v", err)
	}
	_ = publishJSON(nc, subjects.Dashboard, natsDashboardEvent{
		EventType: "agent_connected",
		Data: map[string]any{
			"agentId":   cfg.AgentID,
			"clientId":  cfg.ClientID,
			"siteId":    cfg.SiteID,
			"transport": transportLabel,
			"server":    natsURL,
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})

	r.setStatusConnected(cfg.AgentID, natsURL, transportLabel)
	r.logf("agente conectado ao NATS (command=%s, syncSubject=%s, p2pDiscovery=%s)", subjects.Command, subjects.SyncPing, subjects.P2PDiscovery)

	if _, err = nc.Subscribe(subjects.Command, r.natsCommandHandler(ctx, nc, cfg, subjects)); err != nil {
		return fmt.Errorf("falha ao inscrever no subject de comando: %w", err)
	}
	if _, err = nc.Subscribe(subjects.SyncPing, r.natsSyncPingHandler()); err != nil {
		return fmt.Errorf("falha ao inscrever no subject de sync ping: %w", err)
	}
	if _, err = nc.Subscribe(subjects.P2PDiscovery, r.natsP2PDiscoveryHandler()); err != nil {
		return fmt.Errorf("falha ao inscrever no subject de discovery P2P: %w", err)
	}

	return r.runNATSEventLoop(ctx, nc, cfg, transportLabel, subjects, ipAddr)
}

func natsAuthOptionFromCredentials(creds *NATSCredentials) (nats.Option, func(), error) {
	if creds == nil {
		return nil, nil, fmt.Errorf("credenciais NATS ausentes")
	}
	jwt := strings.TrimSpace(creds.JWT)
	seed := strings.TrimSpace(creds.NKey)
	if jwt == "" && seed == "" {
		return nil, nil, fmt.Errorf("credenciais NATS vazias")
	}
	if jwt != "" && seed != "" {
		kp, err := nkeys.FromSeed([]byte(seed))
		if err != nil {
			return nil, nil, fmt.Errorf("seed NKey invalida: %w", err)
		}
		return nats.UserJWT(func() (string, error) {
				return jwt, nil
			}, kp.Sign), func() {
				kp.Wipe()
			}, nil
	}
	if jwt != "" {
		return nats.Token(jwt), nil, nil
	}
	return nil, nil, fmt.Errorf("JWT ausente para autenticacao NATS")
}

// ─── NATS Handlers ─────────────────────────────────────────────────

func (r *Runtime) natsCommandHandler(ctx context.Context, nc *nats.Conn, cfg Config, subjects natsSubjects) func(msg *nats.Msg) {
	return func(msg *nats.Msg) {
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
			if err := publishJSON(nc, subjects.Result, res); err != nil {
				r.logf("falha ao publicar result (cmd=%s): %v", c.CommandID, err)
				r.enqueueCommandResultOutbox("nats", c.CommandID, exitCode, output, errText, err)
				return
			}

			_ = publishJSON(nc, subjects.Dashboard, natsDashboardEvent{
				EventType: "command_result",
				Data: map[string]any{
					"agentId":   cfg.AgentID,
					"clientId":  cfg.ClientID,
					"siteId":    cfg.SiteID,
					"commandId": c.CommandID,
					"exitCode":  exitCode,
				},
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
			r.logf("result NATS publicado cmdId=%s exitCode=%d", c.CommandID, exitCode)
		}(env)
	}
}

func (r *Runtime) natsSyncPingHandler() func(msg *nats.Msg) {
	return func(msg *nats.Msg) {
		var ping SyncPing
		if err := json.Unmarshal(msg.Data, &ping); err != nil {
			r.logf("mensagem de sync ping NATS invalida: %v", err)
			return
		}
		r.emitSyncPing(ping)
	}
}

func (r *Runtime) natsP2PDiscoveryHandler() func(msg *nats.Msg) {
	return func(msg *nats.Msg) {
		snapshot, err := parseP2PDiscoverySnapshot(msg.Data)
		if err != nil {
			r.logf("mensagem de discovery P2P NATS invalida: %v", err)
			return
		}
		snapshot.ReceivedAt = time.Now().UTC()
		if r.opts.OnP2PDiscoverySnapshot != nil {
			r.opts.OnP2PDiscoverySnapshot(snapshot)
		}
	}
}

// ─── NATS Event Loop ───────────────────────────────────────────────

func (r *Runtime) runNATSEventLoop(ctx context.Context, nc *nats.Conn, cfg Config, transportLabel string, subjects natsSubjects, ipAddr string) error {
	heartbeatTicker := time.NewTicker(heartbeatEvery)
	defer heartbeatTicker.Stop()
	drainTicker := time.NewTicker(commandResultDrainEvery)
	defer drainTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = publishJSON(nc, subjects.Dashboard, natsDashboardEvent{
				EventType: "agent_disconnected",
				Data: map[string]any{
					"agentId":   cfg.AgentID,
					"clientId":  cfg.ClientID,
					"siteId":    cfg.SiteID,
					"transport": transportLabel,
				},
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
			return nil
		case <-heartbeatTicker.C:
			if err := publishJSON(nc, subjects.Heartbeat, natsHeartbeatEnvelope{IPAddress: ipAddr, AgentVersion: "discovery"}); err != nil {
				return fmt.Errorf("heartbeat NATS falhou: %w", err)
			}
		case <-drainTicker.C:
			r.drainCommandResultOutbox(ctx, "nats", func(item CommandResultOutboxItem) error {
				res := natsResultEnvelope{
					CommandID:    item.CommandID,
					ExitCode:     item.ExitCode,
					Output:       item.Output,
					ErrorMessage: item.ErrorMessage,
				}
				return publishJSON(nc, subjects.Result, res)
			})
		}
	}
}

// ─── NATS Subjects ─────────────────────────────────────────────────

func resolveNATSSubjects(cfg Config) (natsSubjects, error) {
	clientID, err := canonicalSubjectSegment("clientId", cfg.ClientID)
	if err != nil {
		return natsSubjects{}, err
	}
	siteID, err := canonicalSubjectSegment("siteId", cfg.SiteID)
	if err != nil {
		return natsSubjects{}, err
	}
	agentID, err := canonicalSubjectSegment("agentId", cfg.AgentID)
	if err != nil {
		return natsSubjects{}, err
	}
	prefix := fmt.Sprintf("tenant.%s.site.%s.agent.%s", clientID, siteID, agentID)
	return natsSubjects{
		Command:      prefix + ".command",
		Heartbeat:    prefix + ".heartbeat",
		Result:       prefix + ".result",
		Hardware:     prefix + ".hardware",
		SyncPing:     prefix + ".sync.ping",
		P2PDiscovery: fmt.Sprintf("tenant.%s.site.%s.p2p.discovery", clientID, siteID),
		Dashboard:    fmt.Sprintf("tenant.%s.site.%s.dashboard.events", clientID, siteID),
	}, nil
}

func validateCanonicalNATSContext(cfg Config) error {
	if !guidPattern.MatchString(strings.TrimSpace(cfg.AgentID)) {
		return fmt.Errorf("agentId ausente ou invalido para NATS canônico")
	}
	if _, err := canonicalSubjectSegment("clientId", cfg.ClientID); err != nil {
		return err
	}
	if _, err := canonicalSubjectSegment("siteId", cfg.SiteID); err != nil {
		return err
	}
	return nil
}

func canonicalSubjectSegment(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s ausente para subject NATS canônico", name)
	}
	if strings.ContainsAny(value, ".*> \t\r\n") {
		return "", fmt.Errorf("%s invalido para subject NATS canônico", name)
	}
	return value, nil
}

func publishJSON(nc *nats.Conn, subject string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return nc.Publish(subject, b)
}

func parseP2PDiscoverySnapshot(data []byte) (P2PDiscoverySnapshot, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return P2PDiscoverySnapshot{}, err
	}

	var snapshot P2PDiscoverySnapshot
	if err := decodeP2PDiscoveryField(raw, &snapshot.Sequence, "sequence", "Sequence"); err != nil {
		return P2PDiscoverySnapshot{}, fmt.Errorf("sequence invalido: %w", err)
	}
	if err := decodeP2PDiscoveryField(raw, &snapshot.TTLSeconds, "ttlSeconds", "TTLSeconds"); err != nil {
		return P2PDiscoverySnapshot{}, fmt.Errorf("ttlSeconds invalido: %w", err)
	}
	var peersRaw []map[string]json.RawMessage
	if err := decodeP2PDiscoveryField(raw, &peersRaw, "peers", "Peers"); err != nil {
		return P2PDiscoverySnapshot{}, fmt.Errorf("peers invalido: %w", err)
	}
	if len(peersRaw) == 0 {
		return P2PDiscoverySnapshot{}, fmt.Errorf("snapshot sem peers")
	}

	snapshot.Peers = make([]P2PDiscoveryPeer, 0, len(peersRaw))
	for _, item := range peersRaw {
		var peer P2PDiscoveryPeer
		if err := decodeP2PDiscoveryField(item, &peer.AgentID, "agentId", "AgentID", "AgentId"); err != nil {
			return P2PDiscoverySnapshot{}, fmt.Errorf("agentId invalido: %w", err)
		}
		if err := decodeP2PDiscoveryField(item, &peer.PeerID, "peerId", "PeerID", "PeerId"); err != nil {
			return P2PDiscoverySnapshot{}, fmt.Errorf("peerId invalido: %w", err)
		}
		if err := decodeP2PDiscoveryField(item, &peer.Addrs, "addrs", "Addrs"); err != nil {
			return P2PDiscoverySnapshot{}, fmt.Errorf("addrs invalido: %w", err)
		}
		if err := decodeP2PDiscoveryField(item, &peer.Port, "port", "Port"); err != nil {
			return P2PDiscoverySnapshot{}, fmt.Errorf("port invalido: %w", err)
		}
		peer.AgentID = strings.TrimSpace(peer.AgentID)
		peer.PeerID = strings.TrimSpace(peer.PeerID)
		cleanAddrs := make([]string, 0, len(peer.Addrs))
		for _, addr := range peer.Addrs {
			if trimmed := strings.TrimSpace(addr); trimmed != "" {
				cleanAddrs = append(cleanAddrs, trimmed)
			}
		}
		peer.Addrs = cleanAddrs
		snapshot.Peers = append(snapshot.Peers, peer)
	}
	if snapshot.TTLSeconds <= 0 {
		snapshot.TTLSeconds = 120
	}
	return snapshot, nil
}

func decodeP2PDiscoveryField(raw map[string]json.RawMessage, dest any, keys ...string) error {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		if len(bytes.TrimSpace(value)) == 0 || string(bytes.TrimSpace(value)) == "null" {
			return nil
		}
		return json.Unmarshal(value, dest)
	}
	return nil
}
