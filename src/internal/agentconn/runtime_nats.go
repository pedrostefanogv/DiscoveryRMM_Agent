package agentconn

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

const natsFanoutAckWait = 30 * time.Minute

type natsCommandRouteScope string

const (
	natsCommandRouteAgent  natsCommandRouteScope = "agent"
	natsCommandRouteSite   natsCommandRouteScope = "site"
	natsCommandRouteClient natsCommandRouteScope = "client"
	natsCommandRouteGlobal natsCommandRouteScope = "global"
)

func (scope natsCommandRouteScope) isFanout() bool {
	return scope == natsCommandRouteSite || scope == natsCommandRouteClient || scope == natsCommandRouteGlobal
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

	wsProxyPath := natsWebSocketProxyPath(natsURL)

	opts := []nats.Option{
		nats.Name("discovery-agent-" + cfg.AgentID),
		nats.Timeout(connectTimeout),
		nats.ReconnectWait(reconnectBase),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			r.logf("[heartbeat][nats] NATS desconectado: %v — heartbeats suspensos ate reconexao", err)
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			r.logf("[heartbeat][nats] NATS reconectado — heartbeats retomados")
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			r.logf("[heartbeat][nats] conexao NATS encerrada permanentemente — heartbeats nao serao mais enviados")
		}),
	}
	if wsProxyPath != "" {
		opts = append(opts, nats.ProxyPath(wsProxyPath))
		r.logf("[transport][%s] websocket proxyPath aplicado: %s", transportLabel, wsProxyPath)
	}
	tokenOpts := append([]nats.Option{}, opts...)
	if strings.TrimSpace(cfg.AuthToken) != "" {
		tokenOpts = append(tokenOpts, nats.Token(strings.TrimSpace(cfg.AuthToken)))
	}
	aclJWT := extractJWTToken(cfg.AuthToken)

	nc, err := nats.Connect(natsURL, tokenOpts...)
	if err != nil {
		return fmt.Errorf("falha ao conectar NATS: %w", err)
	}
	defer nc.Close()

	ipAddr := detectLocalIP()
	subjects, err := resolveNATSSubjects(cfg)
	if err != nil {
		return err
	}
	if err := r.validateAgentIdentityACL(subjects, aclJWT); err != nil {
		return fmt.Errorf("ACL/JWT do AgentIdentity invalida: %w", err)
	}

	hb := r.collectHeartbeat(cfg, ipAddr)
	hbLog := heartbeatLogPayload(hb)
	if err := publishJSON(nc, subjects.Heartbeat, hb); err != nil {
		r.logf("[heartbeat][nats] falha ao publicar heartbeat inicial (subject=%s): %v payload=%s", subjects.Heartbeat, err, hbLog)
	} else {
		r.logf("[heartbeat][nats] heartbeat inicial publicado com sucesso (subject=%s) payload=%s", subjects.Heartbeat, hbLog)
	}
	r.publishDashboardEventNATS(nc, subjects.Dashboard, cfg, "AgentConnected", map[string]any{
		"agentId":   cfg.AgentID,
		"clientId":  cfg.ClientID,
		"siteId":    cfg.SiteID,
		"transport": transportLabel,
		"server":    natsURL,
	})

	r.setStatusConnected(cfg.AgentID, natsURL, transportLabel)
	r.logf("agente conectado ao NATS (commandUnicast=%s, commandSite=%s, commandClient=%s, commandGlobal=%s, globalPong=%s, syncSubject=%s, p2pDiscovery=%s)", subjects.CommandAgent, subjects.CommandSiteFanout, subjects.CommandClientFanout, subjects.CommandGlobalFanout, subjects.GlobalPong, subjects.SyncPing, subjects.P2PDiscovery)

	if _, err = nc.Subscribe(subjects.CommandAgent, r.natsCommandHandler(ctx, nc, cfg, subjects, natsCommandRouteAgent, false)); err != nil {
		return fmt.Errorf("falha ao inscrever no subject de comando unicast: %w", err)
	}
	if err := r.subscribeFanoutCommand(ctx, nc, cfg, subjects, subjects.CommandSiteFanout, natsCommandRouteSite); err != nil {
		return fmt.Errorf("falha ao inscrever no subject de comando fan-out site: %w", err)
	}
	if err := r.subscribeFanoutCommand(ctx, nc, cfg, subjects, subjects.CommandClientFanout, natsCommandRouteClient); err != nil {
		return fmt.Errorf("falha ao inscrever no subject de comando fan-out client: %w", err)
	}
	if err := r.subscribeFanoutCommand(ctx, nc, cfg, subjects, subjects.CommandGlobalFanout, natsCommandRouteGlobal); err != nil {
		return fmt.Errorf("falha ao inscrever no subject de comando fan-out global: %w", err)
	}
	if _, err = nc.Subscribe(subjects.SyncPing, r.natsSyncPingHandler()); err != nil {
		return fmt.Errorf("falha ao inscrever no subject de sync ping: %w", err)
	}
	if _, err = nc.Subscribe(subjects.P2PDiscovery, r.natsP2PDiscoveryHandler()); err != nil {
		return fmt.Errorf("falha ao inscrever no subject de discovery P2P: %w", err)
	}
	if _, err = nc.Subscribe(subjects.GlobalPong, r.natsGlobalPongHandler()); err != nil {
		return fmt.Errorf("falha ao inscrever no subject de global pong: %w", err)
	}

	return r.runNATSEventLoop(ctx, nc, cfg, transportLabel, subjects, ipAddr)
}

func natsWebSocketProxyPath(natsURL string) string {
	u, err := url.Parse(strings.TrimSpace(natsURL))
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "ws" && scheme != "wss" {
		return ""
	}
	path := strings.TrimSpace(u.EscapedPath())
	if path == "" || path == "/" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func (r *Runtime) subscribeFanoutCommand(ctx context.Context, nc *nats.Conn, cfg Config, subjects natsSubjects, subject string, route natsCommandRouteScope) error {
	js, err := nc.JetStream()
	if err == nil {
		durable := natsFanoutDurableName(cfg, route)
		if _, subErr := js.Subscribe(subject, r.natsCommandHandler(ctx, nc, cfg, subjects, route, true),
			nats.Durable(durable),
			nats.ManualAck(),
			nats.AckExplicit(),
			nats.AckWait(natsFanoutAckWait),
			nats.DeliverAll(),
			nats.MaxAckPending(256),
		); subErr == nil {
			r.logf("[nats][jetstream] fan-out com replay habilitado subject=%s durable=%s", subject, durable)
			return nil
		} else {
			r.logf("[nats][jetstream] indisponivel para subject=%s (fallback core): %v", subject, subErr)
		}
	} else {
		r.logf("[nats][jetstream] nao disponivel (fallback core) subject=%s: %v", subject, err)
	}

	if _, subErr := nc.Subscribe(subject, r.natsCommandHandler(ctx, nc, cfg, subjects, route, false)); subErr != nil {
		return subErr
	}
	r.logf("[nats][core] fan-out sem replay subject=%s", subject)
	return nil
}

func natsFanoutDurableName(cfg Config, route natsCommandRouteScope) string {
	agentID := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(cfg.AgentID), "-", ""))
	if agentID == "" {
		agentID = "unknown"
	}
	if len(agentID) > 24 {
		agentID = agentID[:24]
	}
	return fmt.Sprintf("agent-%s-%s", agentID, string(route))
}

// ─── NATS Handlers ─────────────────────────────────────────────────

func (r *Runtime) natsCommandHandler(ctx context.Context, nc *nats.Conn, cfg Config, subjects natsSubjects, route natsCommandRouteScope, requiresAck bool) func(msg *nats.Msg) {
	return func(msg *nats.Msg) {
		var env natsCommandEnvelope
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			r.logf("mensagem de comando NATS invalida (subject=%s): %v", strings.TrimSpace(msg.Subject), err)
			if requiresAck {
				_ = ackNATSMessage(msg)
			}
			return
		}
		sanitizeNATSCommandEnvelope(&env)
		expiresAt, err := validateNATSCommandEnvelope(route, cfg, &env)
		if err != nil {
			r.logf("comando NATS descartado por validacao (subject=%s): %v", strings.TrimSpace(msg.Subject), err)
			if requiresAck {
				_ = ackNATSMessage(msg)
			}
			return
		}
		now := time.Now().UTC()
		if isCommandExpired(expiresAt, now) {
			r.logf("comando NATS expirado descartado dispatchId=%s commandId=%s subject=%s expiresAtUtc=%s", env.DispatchID, env.CommandID, strings.TrimSpace(msg.Subject), strings.TrimSpace(env.ExpiresAtUTC))
			if requiresAck {
				_ = ackNATSMessage(msg)
			}
			return
		}

		dedupeKey := ""
		if route.isFanout() {
			dedupeKey = fanoutDedupeKey(env)
			ttl := fanoutDedupeTTL(expiresAt, now)
			cachedResult, shouldExecute := r.reserveFanoutDispatch(dedupeKey, ttl)
			if !shouldExecute {
				r.logf("comando fan-out duplicado ignorado dispatchId=%s idempotencyKey=%s scope=%s subject=%s", env.DispatchID, env.IdempotencyKey, route, strings.TrimSpace(msg.Subject))
				if requiresAck && cachedResult != nil {
					if err := publishJSON(nc, subjects.Result, cachedResult); err != nil {
						r.logf("falha ao republicar result cacheado para comando fan-out duplicado dispatchId=%s: %v", env.DispatchID, err)
						return
					}
					if err := ackNATSMessage(msg); err != nil {
						r.logf("falha ao ack de comando fan-out duplicado dispatchId=%s: %v", env.DispatchID, err)
					}
				}
				return
			}
		}

		go r.executeAndPublishNATSCommand(ctx, nc, cfg, subjects, env, msg, requiresAck, dedupeKey)
	}
}

func (r *Runtime) executeAndPublishNATSCommand(ctx context.Context, nc *nats.Conn, cfg Config, subjects natsSubjects, env natsCommandEnvelope, msg *nats.Msg, requiresAck bool, dedupeKey string) {
	exitCode, output, errText := r.executeCommand(ctx, normalizeCommandType(env.CommandType), env.Payload)
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes] + "\n... output truncado ..."
	}

	res := natsResultEnvelope{
		DispatchID:   env.DispatchID,
		CommandID:    env.CommandID,
		AgentID:      strings.TrimSpace(cfg.AgentID),
		ExitCode:     exitCode,
		Output:       output,
		ErrorMessage: errText,
	}
	if dedupeKey != "" {
		r.completeFanoutDispatch(dedupeKey, res)
	}

	if err := publishJSON(nc, subjects.Result, res); err != nil {
		r.logf("falha ao publicar result (dispatchId=%s cmd=%s): %v", env.DispatchID, env.CommandID, err)
		r.enqueueCommandResultOutbox("nats", env.DispatchID, env.CommandID, exitCode, output, errText, err)
		return
	}
	if requiresAck {
		if err := ackNATSMessage(msg); err != nil {
			r.logf("falha ao ack do comando fan-out dispatchId=%s commandId=%s: %v", env.DispatchID, env.CommandID, err)
			return
		}
	}
	r.logf("result NATS publicado dispatchId=%s cmdId=%s exitCode=%d", env.DispatchID, env.CommandID, exitCode)
}

func sanitizeNATSCommandEnvelope(env *natsCommandEnvelope) {
	if env == nil {
		return
	}
	env.DispatchID = strings.TrimSpace(env.DispatchID)
	env.CommandID = strings.TrimSpace(env.CommandID)
	env.TargetScope = strings.ToLower(strings.TrimSpace(env.TargetScope))
	env.TargetClientID = strings.TrimSpace(env.TargetClientID)
	env.TargetSiteID = strings.TrimSpace(env.TargetSiteID)
	env.IssuedAtUTC = strings.TrimSpace(env.IssuedAtUTC)
	env.ExpiresAtUTC = strings.TrimSpace(env.ExpiresAtUTC)
	env.IdempotencyKey = strings.TrimSpace(env.IdempotencyKey)
}

func validateNATSCommandEnvelope(route natsCommandRouteScope, cfg Config, env *natsCommandEnvelope) (*time.Time, error) {
	if env == nil {
		return nil, fmt.Errorf("envelope ausente")
	}
	if normalizeCommandType(env.CommandType) == "" {
		return nil, fmt.Errorf("commandType ausente")
	}

	clientID := strings.TrimSpace(cfg.ClientID)
	siteID := strings.TrimSpace(cfg.SiteID)

	if route.isFanout() {
		if env.DispatchID == "" {
			return nil, fmt.Errorf("dispatchId obrigatorio para fan-out")
		}
		if env.IdempotencyKey == "" {
			return nil, fmt.Errorf("idempotencyKey obrigatorio para fan-out")
		}
		if env.TargetScope != string(route) {
			return nil, fmt.Errorf("targetScope=%q incoerente com subject fan-out %q", env.TargetScope, route)
		}
		if env.IssuedAtUTC == "" {
			return nil, fmt.Errorf("issuedAtUtc obrigatorio para fan-out")
		}
		if _, err := time.Parse(time.RFC3339, env.IssuedAtUTC); err != nil {
			return nil, fmt.Errorf("issuedAtUtc invalido: %w", err)
		}
		switch route {
		case natsCommandRouteSite:
			if env.TargetClientID == "" || env.TargetSiteID == "" {
				return nil, fmt.Errorf("targetClientId e targetSiteId obrigatorios para scope=site")
			}
		case natsCommandRouteClient:
			if env.TargetClientID == "" {
				return nil, fmt.Errorf("targetClientId obrigatorio para scope=client")
			}
		}
	} else {
		if env.CommandID == "" && env.DispatchID == "" {
			return nil, fmt.Errorf("commandId ou dispatchId obrigatorio para unicast")
		}
		if env.TargetScope != "" && env.TargetScope != string(natsCommandRouteAgent) {
			return nil, fmt.Errorf("targetScope=%q invalido para subject unicast", env.TargetScope)
		}
	}

	if env.TargetClientID != "" && clientID != "" && !strings.EqualFold(env.TargetClientID, clientID) {
		return nil, fmt.Errorf("targetClientId=%q nao corresponde ao clientId do agent", env.TargetClientID)
	}
	if env.TargetSiteID != "" && siteID != "" && !strings.EqualFold(env.TargetSiteID, siteID) {
		return nil, fmt.Errorf("targetSiteId=%q nao corresponde ao siteId do agent", env.TargetSiteID)
	}

	if env.ExpiresAtUTC == "" {
		return nil, nil
	}
	expiresAt, err := time.Parse(time.RFC3339, env.ExpiresAtUTC)
	if err != nil {
		return nil, fmt.Errorf("expiresAtUtc invalido: %w", err)
	}
	expiresAt = expiresAt.UTC()
	return &expiresAt, nil
}

func isCommandExpired(expiresAt *time.Time, now time.Time) bool {
	if expiresAt == nil {
		return false
	}
	return !expiresAt.After(now.UTC())
}

func fanoutDedupeKey(env natsCommandEnvelope) string {
	return strings.ToLower(strings.TrimSpace(env.IdempotencyKey)) + "|" + strings.ToLower(strings.TrimSpace(env.DispatchID))
}

func fanoutDedupeTTL(expiresAt *time.Time, now time.Time) time.Duration {
	ttl := commandFanoutDedupeDefaultTTL
	if expiresAt != nil {
		delta := expiresAt.UTC().Sub(now.UTC())
		if delta > ttl {
			ttl = delta
		}
	}
	if ttl < commandFanoutDedupeMinTTL {
		ttl = commandFanoutDedupeMinTTL
	}
	if ttl > commandFanoutDedupeMaxTTL {
		ttl = commandFanoutDedupeMaxTTL
	}
	return ttl
}

func (r *Runtime) reserveFanoutDispatch(dedupeKey string, ttl time.Duration) (*natsResultEnvelope, bool) {
	if strings.TrimSpace(dedupeKey) == "" {
		return nil, true
	}
	now := time.Now().UTC()

	r.dedupeMu.Lock()
	defer r.dedupeMu.Unlock()

	for key, entry := range r.fanoutDedupe {
		if !entry.ExpiresAt.After(now) {
			delete(r.fanoutDedupe, key)
		}
	}

	if existing, ok := r.fanoutDedupe[dedupeKey]; ok {
		if existing.Result != nil {
			copyResult := *existing.Result
			return &copyResult, false
		}
		return nil, false
	}

	r.fanoutDedupe[dedupeKey] = fanoutDedupeRecord{ExpiresAt: now.Add(ttl)}
	return nil, true
}

func (r *Runtime) completeFanoutDispatch(dedupeKey string, result natsResultEnvelope) {
	if strings.TrimSpace(dedupeKey) == "" {
		return
	}
	now := time.Now().UTC()

	r.dedupeMu.Lock()
	defer r.dedupeMu.Unlock()

	entry, ok := r.fanoutDedupe[dedupeKey]
	if !ok {
		entry = fanoutDedupeRecord{ExpiresAt: now.Add(commandFanoutDedupeDefaultTTL)}
	}
	if !entry.ExpiresAt.After(now) {
		entry.ExpiresAt = now.Add(commandFanoutDedupeMinTTL)
	}
	copyResult := result
	entry.Result = &copyResult
	r.fanoutDedupe[dedupeKey] = entry
}

func ackNATSMessage(msg *nats.Msg) error {
	if msg == nil {
		return nil
	}
	return msg.Ack()
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

func (r *Runtime) natsGlobalPongHandler() func(msg *nats.Msg) {
	return func(msg *nats.Msg) {
		pong, err := parseGlobalPongMessage(msg.Data)
		if err != nil {
			r.logf("mensagem de global pong NATS invalida: %v", err)
			return
		}
		pong.ReceivedAt = time.Now().UTC()
		r.emitGlobalPong(pong)
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
	heartbeatInterval := heartbeatEvery
	if cfg.HeartbeatInterval > 0 {
		heartbeatInterval = time.Duration(cfg.HeartbeatInterval) * time.Second
	}
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()
	drainTicker := time.NewTicker(commandResultDrainEvery)
	defer drainTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.publishDashboardEventNATS(nc, subjects.Dashboard, cfg, "AgentDisconnected", map[string]any{
				"agentId":   cfg.AgentID,
				"clientId":  cfg.ClientID,
				"siteId":    cfg.SiteID,
				"transport": transportLabel,
			})
			return nil
		case <-r.reloadCh:
			r.publishDashboardEventNATS(nc, subjects.Dashboard, cfg, "AgentDisconnected", map[string]any{
				"agentId":   cfg.AgentID,
				"clientId":  cfg.ClientID,
				"siteId":    cfg.SiteID,
				"transport": transportLabel,
			})
			return fmt.Errorf("reload solicitado")
		case <-heartbeatTicker.C:
			r.sendHeartbeatNATS(nc, subjects.Heartbeat, cfg, ipAddr)
		case forceDone := <-r.forceHeartbeatCh:
			r.sendHeartbeatNATS(nc, subjects.Heartbeat, cfg, ipAddr)
			forceDone <- struct{}{}
		case <-drainTicker.C:
			r.drainCommandResultOutbox(ctx, "nats", func(item CommandResultOutboxItem) error {
				res := natsResultEnvelope{
					DispatchID:   item.DispatchID,
					CommandID:    item.CommandID,
					AgentID:      strings.TrimSpace(cfg.AgentID),
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
		CommandAgent:        prefix + ".command",
		CommandSiteFanout:   fmt.Sprintf("tenant.%s.site.%s.agents.command", clientID, siteID),
		CommandClientFanout: fmt.Sprintf("tenant.%s.agents.command", clientID),
		CommandGlobalFanout: "tenant.global.agents.command",
		GlobalPong:          "tenant.global.pong",
		Heartbeat:           prefix + ".heartbeat",
		Result:              prefix + ".result",
		Hardware:            prefix + ".hardware",
		RemoteDebugLog:      prefix + ".remote-debug.log",
		SyncPing:            prefix + ".sync.ping",
		P2PDiscovery:        fmt.Sprintf("tenant.%s.site.%s.p2p.discovery", clientID, siteID),
		Dashboard:           fmt.Sprintf("tenant.%s.site.%s.dashboard.events", clientID, siteID),
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

func (r *Runtime) validateAgentIdentityACL(subjects natsSubjects, jwtCandidate string) error {
	jwt := extractJWTToken(jwtCandidate)
	if jwt == "" {
		r.logf("[acl][nats] JWT indisponivel para validacao local dos subjects do AgentIdentity")
		return nil
	}
	if err := validateAgentIdentityJWTClaims(jwt, subjects); err != nil {
		return err
	}
	r.logf("[acl][nats] claims do AgentIdentity validadas para 7 subscribes e 4 publishes")
	return nil
}

func validateAgentIdentityJWTClaims(jwt string, subjects natsSubjects) error {
	claims, err := decodeJWTClaimsUnverified(jwt)
	if err != nil {
		return err
	}
	subAllow, err := extractJWTAllowedSubjects(claims, "sub")
	if err != nil {
		return fmt.Errorf("claim sub.allow invalida: %w", err)
	}
	pubAllow, err := extractJWTAllowedSubjects(claims, "pub")
	if err != nil {
		return fmt.Errorf("claim pub.allow invalida: %w", err)
	}

	expectedSub := []string{
		subjects.CommandAgent,
		subjects.CommandSiteFanout,
		subjects.CommandClientFanout,
		subjects.CommandGlobalFanout,
		subjects.GlobalPong,
		subjects.SyncPing,
		subjects.P2PDiscovery,
	}
	expectedPub := []string{
		subjects.Heartbeat,
		subjects.Result,
		subjects.Hardware,
		subjects.RemoteDebugLog,
	}

	if err := compareSubjectSets("subscribe", subAllow, expectedSub); err != nil {
		return err
	}
	if err := compareSubjectSets("publish", pubAllow, expectedPub); err != nil {
		return err
	}
	return nil
}

func decodeJWTClaimsUnverified(jwt string) (map[string]any, error) {
	parts := strings.Split(strings.TrimSpace(jwt), ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("token nao possui formato JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("falha ao decodificar payload JWT: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("claims JWT invalidas: %w", err)
	}
	return claims, nil
}

func extractJWTAllowedSubjects(claims map[string]any, direction string) ([]string, error) {
	natsClaims, ok := claims["nats"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("claim nats ausente")
	}
	dirClaim, ok := natsClaims[direction].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("claim nats.%s ausente", direction)
	}
	allowRaw, ok := dirClaim["allow"]
	if !ok {
		return nil, fmt.Errorf("claim nats.%s.allow ausente", direction)
	}

	var out []string
	switch typed := allowRaw.(type) {
	case []any:
		for _, value := range typed {
			s := strings.TrimSpace(toString(value))
			if s != "" {
				out = append(out, s)
			}
		}
	case []string:
		for _, value := range typed {
			s := strings.TrimSpace(value)
			if s != "" {
				out = append(out, s)
			}
		}
	case string:
		s := strings.TrimSpace(typed)
		if s != "" {
			out = append(out, s)
		}
	default:
		return nil, fmt.Errorf("claim nats.%s.allow com tipo invalido", direction)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("claim nats.%s.allow vazio", direction)
	}
	return out, nil
}

func compareSubjectSets(direction string, got []string, expected []string) error {
	gotList := normalizeSubjectList(got)
	expectedList := normalizeSubjectList(expected)

	gotSet := make(map[string]struct{}, len(gotList))
	for _, item := range gotList {
		gotSet[item] = struct{}{}
	}
	expectedSet := make(map[string]struct{}, len(expectedList))
	for _, item := range expectedList {
		expectedSet[item] = struct{}{}
	}

	var missing []string
	for _, item := range expectedList {
		if _, ok := gotSet[item]; !ok {
			missing = append(missing, item)
		}
	}
	var extra []string
	for _, item := range gotList {
		if _, ok := expectedSet[item]; !ok {
			extra = append(extra, item)
		}
	}

	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return fmt.Errorf("subjects de %s divergentes (missing=%v extra=%v)", direction, missing, extra)
}

func normalizeSubjectList(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized != "" {
			set[normalized] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func extractJWTToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Count(raw, ".") == 2 {
		return raw
	}
	return ""
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

func parseGlobalPongMessage(data []byte) (GlobalPongMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return GlobalPongMessage{}, err
	}

	var pong GlobalPongMessage
	if err := decodeP2PDiscoveryField(raw, &pong.EventType, "eventType", "EventType"); err != nil {
		return GlobalPongMessage{}, fmt.Errorf("eventType invalido: %w", err)
	}
	pong.EventType = strings.TrimSpace(pong.EventType)
	if pong.EventType == "" {
		pong.EventType = "pong"
	}
	if !strings.EqualFold(pong.EventType, "pong") {
		return GlobalPongMessage{}, fmt.Errorf("eventType %q invalido para global pong", pong.EventType)
	}
	if err := decodeP2PDiscoveryField(raw, &pong.ServerTimeUTC, "serverTimeUtc", "ServerTimeUtc", "ServerTimeUTC"); err != nil {
		return GlobalPongMessage{}, fmt.Errorf("serverTimeUtc invalido: %w", err)
	}
	pong.ServerTimeUTC = strings.TrimSpace(pong.ServerTimeUTC)
	if err := decodeGlobalPongServerOverloaded(raw, &pong.ServerOverloaded, "serverOverloaded", "ServerOverloaded"); err != nil {
		return GlobalPongMessage{}, fmt.Errorf("serverOverloaded invalido: %w", err)
	}

	return pong, nil
}

func decodeGlobalPongServerOverloaded(raw map[string]json.RawMessage, dest **bool, keys ...string) error {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}

		trimmed := bytes.TrimSpace(value)
		if len(trimmed) == 0 || string(trimmed) == "null" {
			*dest = nil
			return nil
		}

		var boolValue bool
		if err := json.Unmarshal(trimmed, &boolValue); err == nil {
			v := boolValue
			*dest = &v
			return nil
		}

		var textValue string
		if err := json.Unmarshal(trimmed, &textValue); err == nil {
			switch strings.ToLower(strings.TrimSpace(textValue)) {
			case "", "null":
				*dest = nil
				return nil
			case "true":
				v := true
				*dest = &v
				return nil
			case "false":
				v := false
				*dest = &v
				return nil
			}
		}

		return fmt.Errorf("valor nao suportado: %s", string(trimmed))
	}

	return nil
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
