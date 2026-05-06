package agentconn

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

const (
	testHomologAgentID  = "019def54-fde2-7ced-ac3c-3c2de42433aa"
	testHomologClientID = "019dead3-70ee-7c66-8662-7558e0b23ad5"
	testHomologSiteID   = "019dead3-845a-79e3-9769-f94a0f6a114f"
)

func hbFloat(v float64) *float64 { return &v }
func hbInt(v int) *int           { return &v }
func hbInt64(v int64) *int64     { return &v }

func testHeartbeatExamples() []AgentHeartbeat {
	return []AgentHeartbeat{
		{
			AgentId:      "d2719a7d-43bb-4e7e-bbe6-18dce7bf1db7",
			IpAddress:    "192.168.1.10",
			AgentVersion: "1.0.0-test",
			TimestampUtc: "2026-05-05T12:00:00Z",
		},
		{
			AgentId:       "d2719a7d-43bb-4e7e-bbe6-18dce7bf1db7",
			IpAddress:     "10.0.0.24",
			Hostname:      "WIN-AGENT-01",
			AgentVersion:  "1.2.3",
			TimestampUtc:  "2026-05-05T12:00:10Z",
			CpuPercent:    hbFloat(17.3),
			MemoryPercent: hbFloat(62.1),
			MemoryTotalGb: hbFloat(64.0),
			MemoryUsedGb:  hbFloat(39.74),
			DiskPercent:   hbFloat(32.4),
			DiskTotalGb:   hbFloat(930.65),
			DiskUsedGb:    hbFloat(301.47),
			P2pPeers:      hbInt(12),
			UptimeSeconds: hbInt64(58956),
			ProcessCount:  hbInt(294),
		},
		{
			AgentId:       "d2719a7d-43bb-4e7e-bbe6-18dce7bf1db7",
			Hostname:      "WIN-AGENT-02",
			AgentVersion:  "1.2.3-debug",
			TimestampUtc:  "2026-05-05T12:00:20Z",
			MemoryTotalGb: hbFloat(32),
			P2pPeers:      hbInt(0),
			UptimeSeconds: hbInt64(42),
			ProcessCount:  hbInt(3),
		},
	}
}

func startEmbeddedNATSServer(t *testing.T) *natsserver.Server {
	t.Helper()

	server, err := natsserver.NewServer(&natsserver.Options{
		Host:   "127.0.0.1",
		Port:   -1,
		NoLog:  true,
		NoSigs: true,
	})
	if err != nil {
		t.Fatalf("falha ao criar NATS server de teste: %v", err)
	}

	go server.Start()
	if !server.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS server de teste nao ficou pronto")
	}
	t.Cleanup(server.Shutdown)

	return server
}

func TestHeartbeatSend_NATS_ThreeExamples(t *testing.T) {
	server := startEmbeddedNATSServer(t)
	nc, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("falha ao conectar no NATS de teste: %v", err)
	}
	t.Cleanup(nc.Close)

	const subject = "tenant.client-1.site.site-1.agent.agent-1.heartbeat"
	sub, err := nc.SubscribeSync(subject)
	if err != nil {
		t.Fatalf("falha ao criar subscribe de teste: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("falha ao flush do subscribe: %v", err)
	}

	for i, expected := range testHeartbeatExamples() {
		t.Run("example_"+string(rune('1'+i-0)), func(t *testing.T) {
			if err := publishJSON(nc, subject, expected); err != nil {
				t.Fatalf("publishJSON falhou: %v", err)
			}
			if err := nc.Flush(); err != nil {
				t.Fatalf("flush apos publish falhou: %v", err)
			}

			msg, err := sub.NextMsg(2 * time.Second)
			if err != nil {
				t.Fatalf("nao recebeu mensagem heartbeat no NATS: %v", err)
			}

			var got AgentHeartbeat
			if err := json.Unmarshal(msg.Data, &got); err != nil {
				t.Fatalf("payload heartbeat NATS invalido: %v", err)
			}

			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("heartbeat NATS divergente\nexpected=%+v\ngot=%+v", expected, got)
			}
		})
	}
}

func decodeDashboardEventFromNATS(t *testing.T, payload []byte) struct {
	EventType    string         `json:"eventType"`
	Data         map[string]any `json:"data"`
	TimestampUtc string         `json:"timestampUtc"`
	ClientId     string         `json:"clientId"`
	SiteId       string         `json:"siteId"`
} {
	t.Helper()

	var envelope struct {
		EventType    string         `json:"eventType"`
		Data         map[string]any `json:"data"`
		TimestampUtc string         `json:"timestampUtc"`
		ClientId     string         `json:"clientId"`
		SiteId       string         `json:"siteId"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("dashboard event NATS invalido: %v", err)
	}
	return envelope
}

func TestPublishDashboardEventNATS_CanonicalEnvelope(t *testing.T) {
	server := startEmbeddedNATSServer(t)
	nc, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("falha ao conectar no NATS de teste: %v", err)
	}
	t.Cleanup(nc.Close)

	cfg := Config{
		AgentID:  testHomologAgentID,
		ClientID: testHomologClientID,
		SiteID:   testHomologSiteID,
	}
	subjects, err := resolveNATSSubjects(cfg)
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	sub, err := nc.SubscribeSync(subjects.Dashboard)
	if err != nil {
		t.Fatalf("falha ao criar subscribe dashboard de teste: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("falha ao flush do subscribe dashboard: %v", err)
	}

	runtime := NewRuntime(Options{})
	ok := runtime.publishDashboardEventNATS(nc, subjects.Dashboard, cfg, "AgentConnected", map[string]any{
		"agentId":   cfg.AgentID,
		"clientId":  cfg.ClientID,
		"siteId":    cfg.SiteID,
		"transport": "nats",
		"server":    server.ClientURL(),
	})
	if !ok {
		t.Fatal("publishDashboardEventNATS deveria publicar evento canonico")
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("falha ao flush do publish dashboard: %v", err)
	}

	msg, err := sub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("nao recebeu dashboard event no NATS: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(msg.Data, &raw); err != nil {
		t.Fatalf("payload bruto dashboard invalido: %v", err)
	}
	if _, ok := raw["timestamp"]; ok {
		t.Fatal("evento dashboard nao deveria conter campo legado timestamp")
	}
	if _, ok := raw["timestampUtc"]; !ok {
		t.Fatal("evento dashboard deveria conter timestampUtc")
	}

	envelope := decodeDashboardEventFromNATS(t, msg.Data)
	if envelope.EventType != "AgentConnected" {
		t.Fatalf("eventType = %q, esperado AgentConnected", envelope.EventType)
	}
	if envelope.ClientId != cfg.ClientID {
		t.Fatalf("clientId raiz = %q, esperado %q", envelope.ClientId, cfg.ClientID)
	}
	if envelope.SiteId != cfg.SiteID {
		t.Fatalf("siteId raiz = %q, esperado %q", envelope.SiteId, cfg.SiteID)
	}
	if _, err := time.Parse(time.RFC3339, envelope.TimestampUtc); err != nil {
		t.Fatalf("timestampUtc invalido: %v", err)
	}
	if got := fmt.Sprint(envelope.Data["agentId"]); got != cfg.AgentID {
		t.Fatalf("data.agentId = %q, esperado %q", got, cfg.AgentID)
	}
	if got := fmt.Sprint(envelope.Data["clientId"]); got != cfg.ClientID {
		t.Fatalf("data.clientId = %q, esperado %q", got, cfg.ClientID)
	}
	if got := fmt.Sprint(envelope.Data["siteId"]); got != cfg.SiteID {
		t.Fatalf("data.siteId = %q, esperado %q", got, cfg.SiteID)
	}
	if got := fmt.Sprint(envelope.Data["transport"]); got != "nats" {
		t.Fatalf("data.transport = %q, esperado nats", got)
	}
	if got := fmt.Sprint(envelope.Data["server"]); got != server.ClientURL() {
		t.Fatalf("data.server = %q, esperado %q", got, server.ClientURL())
	}
}

func TestPublishDashboardEventNATS_RejectsLegacyEventType(t *testing.T) {
	server := startEmbeddedNATSServer(t)
	nc, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("falha ao conectar no NATS de teste: %v", err)
	}
	t.Cleanup(nc.Close)

	cfg := Config{
		AgentID:  testHomologAgentID,
		ClientID: testHomologClientID,
		SiteID:   testHomologSiteID,
	}
	subjects, err := resolveNATSSubjects(cfg)
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	sub, err := nc.SubscribeSync(subjects.Dashboard)
	if err != nil {
		t.Fatalf("falha ao criar subscribe dashboard de teste: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("falha ao flush do subscribe dashboard: %v", err)
	}

	var logs []string
	runtime := NewRuntime(Options{
		Logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	})
	ok := runtime.publishDashboardEventNATS(nc, subjects.Dashboard, cfg, "agent_connected", map[string]any{
		"agentId":   cfg.AgentID,
		"clientId":  cfg.ClientID,
		"siteId":    cfg.SiteID,
		"transport": "nats",
	})
	if ok {
		t.Fatal("publishDashboardEventNATS nao deveria aceitar eventType legado")
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("falha ao flush apos rejeicao de dashboard event: %v", err)
	}
	if _, err := sub.NextMsg(250 * time.Millisecond); err == nil {
		t.Fatal("evento legado nao deveria ser publicado em dashboard.events")
	}
	if len(logs) == 0 {
		t.Fatal("esperava log de CONTRACT_VIOLATION para eventType legado")
	}
	if !strings.Contains(logs[0], "[CONTRACT_VIOLATION]") || !strings.Contains(logs[0], "agent_connected") {
		t.Fatalf("log inesperado para eventType legado: %q", logs[0])
	}
}

func TestNATSCommandHandler_PublishesResultWithoutDashboardLegacyEvent(t *testing.T) {
	server := startEmbeddedNATSServer(t)
	nc, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("falha ao conectar no NATS de teste: %v", err)
	}
	t.Cleanup(nc.Close)

	cfg := Config{
		AgentID:  testHomologAgentID,
		ClientID: testHomologClientID,
		SiteID:   testHomologSiteID,
	}
	subjects, err := resolveNATSSubjects(cfg)
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	resultSub, err := nc.SubscribeSync(subjects.Result)
	if err != nil {
		t.Fatalf("falha ao criar subscribe result de teste: %v", err)
	}
	dashboardSub, err := nc.SubscribeSync(subjects.Dashboard)
	if err != nil {
		t.Fatalf("falha ao criar subscribe dashboard de teste: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("falha ao flush dos subscribes: %v", err)
	}

	runtime := NewRuntime(Options{
		HandleCommand: func(parent context.Context, cmdType string, payload any) (bool, int, string, string) {
			if strings.TrimSpace(strings.ToLower(cmdType)) != "powershell" {
				return true, 1, "", "tipo de comando inesperado"
			}
			return true, 0, "ok-from-test", ""
		},
	})
	handler := runtime.natsCommandHandler(context.Background(), nc, cfg, subjects, natsCommandRouteAgent, false)

	commandPayload, err := json.Marshal(natsCommandEnvelope{
		CommandID:   "cmd-123",
		CommandType: "powershell",
		Payload:     map[string]any{"command": "Get-Date"},
	})
	if err != nil {
		t.Fatalf("falha ao serializar comando NATS de teste: %v", err)
	}
	handler(&nats.Msg{Data: commandPayload})

	msg, err := resultSub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("nao recebeu result NATS: %v", err)
	}
	var result natsResultEnvelope
	if err := json.Unmarshal(msg.Data, &result); err != nil {
		t.Fatalf("result NATS invalido: %v", err)
	}
	if result.CommandID != "cmd-123" {
		t.Fatalf("commandId = %q, esperado cmd-123", result.CommandID)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exitCode = %d, esperado 0", result.ExitCode)
	}
	if result.Output != "ok-from-test" {
		t.Fatalf("output = %q, esperado ok-from-test", result.Output)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("falha ao flush apos result: %v", err)
	}
	if _, err := dashboardSub.NextMsg(250 * time.Millisecond); err == nil {
		t.Fatal("command_result nao deveria mais ser publicado em dashboard.events")
	}
}

func TestNATSFanoutCommandHandler_DedupesByDispatchAndIdempotency(t *testing.T) {
	server := startEmbeddedNATSServer(t)
	nc, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("falha ao conectar no NATS de teste: %v", err)
	}
	t.Cleanup(nc.Close)

	cfg := Config{
		AgentID:  testHomologAgentID,
		ClientID: testHomologClientID,
		SiteID:   testHomologSiteID,
	}
	subjects, err := resolveNATSSubjects(cfg)
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	resultSub, err := nc.SubscribeSync(subjects.Result)
	if err != nil {
		t.Fatalf("falha ao criar subscribe result de teste: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("falha ao flush do subscribe result: %v", err)
	}

	execCount := 0
	runtime := NewRuntime(Options{
		HandleCommand: func(parent context.Context, cmdType string, payload any) (bool, int, string, string) {
			execCount++
			return true, 0, "fanout-ok", ""
		},
	})
	handler := runtime.natsCommandHandler(context.Background(), nc, cfg, subjects, natsCommandRouteSite, false)

	env := natsCommandEnvelope{
		DispatchID:     "dispatch-1",
		CommandID:      "cmd-fanout-1",
		CommandType:    "powershell",
		TargetScope:    "site",
		TargetClientID: cfg.ClientID,
		TargetSiteID:   cfg.SiteID,
		IssuedAtUTC:    time.Now().UTC().Format(time.RFC3339),
		ExpiresAtUTC:   time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339),
		IdempotencyKey: "idem-1",
		Payload:        map[string]any{"command": "Get-Date"},
	}
	payload, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("falha ao serializar envelope fan-out: %v", err)
	}

	handler(&nats.Msg{Subject: subjects.CommandSiteFanout, Data: payload})
	handler(&nats.Msg{Subject: subjects.CommandSiteFanout, Data: payload})

	msg, err := resultSub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("nao recebeu result fan-out: %v", err)
	}
	var result natsResultEnvelope
	if err := json.Unmarshal(msg.Data, &result); err != nil {
		t.Fatalf("result fan-out invalido: %v", err)
	}
	if result.DispatchID != "dispatch-1" {
		t.Fatalf("dispatchId = %q, esperado dispatch-1", result.DispatchID)
	}
	if result.CommandID != "cmd-fanout-1" {
		t.Fatalf("commandId = %q, esperado cmd-fanout-1", result.CommandID)
	}
	if result.Output != "fanout-ok" {
		t.Fatalf("output = %q, esperado fanout-ok", result.Output)
	}

	if _, err := resultSub.NextMsg(250 * time.Millisecond); err == nil {
		t.Fatal("nao deveria receber segundo result para comando fan-out duplicado")
	}
	if execCount != 1 {
		t.Fatalf("execucoes = %d, esperado 1", execCount)
	}
}

func TestNATSFanoutCommandHandler_DropsExpiredCommand(t *testing.T) {
	server := startEmbeddedNATSServer(t)
	nc, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("falha ao conectar no NATS de teste: %v", err)
	}
	t.Cleanup(nc.Close)

	cfg := Config{
		AgentID:  testHomologAgentID,
		ClientID: testHomologClientID,
		SiteID:   testHomologSiteID,
	}
	subjects, err := resolveNATSSubjects(cfg)
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	resultSub, err := nc.SubscribeSync(subjects.Result)
	if err != nil {
		t.Fatalf("falha ao criar subscribe result de teste: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("falha ao flush do subscribe result: %v", err)
	}

	execCount := 0
	runtime := NewRuntime(Options{
		HandleCommand: func(parent context.Context, cmdType string, payload any) (bool, int, string, string) {
			execCount++
			return true, 0, "expired-should-not-run", ""
		},
	})
	handler := runtime.natsCommandHandler(context.Background(), nc, cfg, subjects, natsCommandRouteSite, false)

	payload, err := json.Marshal(natsCommandEnvelope{
		DispatchID:     "dispatch-expired",
		CommandID:      "cmd-expired",
		CommandType:    "powershell",
		TargetScope:    "site",
		TargetClientID: cfg.ClientID,
		TargetSiteID:   cfg.SiteID,
		IssuedAtUTC:    time.Now().UTC().Add(-20 * time.Minute).Format(time.RFC3339),
		ExpiresAtUTC:   time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339),
		IdempotencyKey: "idem-expired",
		Payload:        map[string]any{"command": "Get-Date"},
	})
	if err != nil {
		t.Fatalf("falha ao serializar envelope expirado: %v", err)
	}
	handler(&nats.Msg{Subject: subjects.CommandSiteFanout, Data: payload})

	if _, err := resultSub.NextMsg(250 * time.Millisecond); err == nil {
		t.Fatal("comando expirado nao deveria publicar result")
	}
	if execCount != 0 {
		t.Fatalf("execucoes = %d, esperado 0 para comando expirado", execCount)
	}
}

func TestNATSFanoutCommandHandler_RejectsSubjectScopeMismatch(t *testing.T) {
	server := startEmbeddedNATSServer(t)
	nc, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("falha ao conectar no NATS de teste: %v", err)
	}
	t.Cleanup(nc.Close)

	cfg := Config{
		AgentID:  testHomologAgentID,
		ClientID: testHomologClientID,
		SiteID:   testHomologSiteID,
	}
	subjects, err := resolveNATSSubjects(cfg)
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	resultSub, err := nc.SubscribeSync(subjects.Result)
	if err != nil {
		t.Fatalf("falha ao criar subscribe result de teste: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("falha ao flush do subscribe result: %v", err)
	}

	execCount := 0
	runtime := NewRuntime(Options{
		HandleCommand: func(parent context.Context, cmdType string, payload any) (bool, int, string, string) {
			execCount++
			return true, 0, "scope-mismatch-should-not-run", ""
		},
	})
	handler := runtime.natsCommandHandler(context.Background(), nc, cfg, subjects, natsCommandRouteSite, false)

	payload, err := json.Marshal(natsCommandEnvelope{
		DispatchID:     "dispatch-mismatch",
		CommandID:      "cmd-mismatch",
		CommandType:    "powershell",
		TargetScope:    "client",
		TargetClientID: cfg.ClientID,
		IssuedAtUTC:    time.Now().UTC().Format(time.RFC3339),
		IdempotencyKey: "idem-mismatch",
		Payload:        map[string]any{"command": "Get-Date"},
	})
	if err != nil {
		t.Fatalf("falha ao serializar envelope incoerente: %v", err)
	}
	handler(&nats.Msg{Subject: subjects.CommandSiteFanout, Data: payload})

	if _, err := resultSub.NextMsg(250 * time.Millisecond); err == nil {
		t.Fatal("comando com subject/targetScope incoerente nao deveria publicar result")
	}
	if execCount != 0 {
		t.Fatalf("execucoes = %d, esperado 0 para comando incoerente", execCount)
	}
}
