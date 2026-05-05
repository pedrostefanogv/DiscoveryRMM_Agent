package agentconn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

func startSignalRTestConnection(t *testing.T) (*websocket.Conn, <-chan []byte) {
	t.Helper()

	frames := make(chan []byte, 16)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("falha ao aceitar websocket de teste: %v", err)
			return
		}
		defer conn.Close()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frames <- data
		}
	}))
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("falha ao conectar websocket de teste: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn, frames
}

func startSignalRDuplexTestConnection(t *testing.T) (*websocket.Conn, <-chan []byte, func([]byte) error) {
	t.Helper()

	frames := make(chan []byte, 32)
	serverConnCh := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("falha ao aceitar websocket de teste (duplex): %v", err)
			return
		}
		serverConnCh <- conn
		defer conn.Close()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frames <- data
		}
	}))
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("falha ao conectar websocket duplex de teste: %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	var serverConn *websocket.Conn
	select {
	case serverConn = <-serverConnCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout aguardando conexao do servidor websocket de teste")
	}

	sendFromServer := func(payload []byte) error {
		return serverConn.WriteMessage(websocket.TextMessage, payload)
	}

	return clientConn, frames, sendFromServer
}

func mustSignalRInvocationFrame(t *testing.T, target string, args ...any) []byte {
	t.Helper()

	envelope := map[string]any{
		"type":      1,
		"target":    target,
		"arguments": args,
	}
	b, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("falha ao serializar frame SignalR de teste: %v", err)
	}
	return append(b, 0x1e)
}

func nextSignalRFrame(t *testing.T, frames <-chan []byte) []byte {
	t.Helper()

	select {
	case raw := <-frames:
		return raw
	case <-time.After(2 * time.Second):
		t.Fatal("timeout aguardando frame SignalR")
		return nil
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

func decodeSignalRInvocationFrame(t *testing.T, frame []byte) struct {
	Type      int               `json:"type"`
	Target    string            `json:"target"`
	Arguments []json.RawMessage `json:"arguments"`
} {
	t.Helper()

	records := splitSignalRRecords(frame)
	if len(records) != 1 {
		t.Fatalf("frame SignalR inesperado: records=%d", len(records))
	}

	var envelope struct {
		Type      int               `json:"type"`
		Target    string            `json:"target"`
		Arguments []json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(records[0]), &envelope); err != nil {
		t.Fatalf("envelope SignalR invalido: %v", err)
	}
	if envelope.Type != 1 {
		t.Fatalf("type SignalR = %d, esperado 1", envelope.Type)
	}
	return envelope
}

func decodeHeartbeatFromSignalRFrame(t *testing.T, frame []byte) AgentHeartbeat {
	t.Helper()

	envelope := decodeSignalRInvocationFrame(t, frame)
	if envelope.Target != "HeartbeatV2" {
		t.Fatalf("target SignalR = %q, esperado HeartbeatV2", envelope.Target)
	}
	if len(envelope.Arguments) != 1 {
		t.Fatalf("arguments SignalR = %d, esperado 1", len(envelope.Arguments))
	}

	var hb AgentHeartbeat
	if err := json.Unmarshal(envelope.Arguments[0], &hb); err != nil {
		t.Fatalf("payload heartbeat SignalR invalido: %v", err)
	}
	return hb
}

func TestHeartbeatSend_SignalR_ThreeExamples(t *testing.T) {
	runtime := NewRuntime(Options{})
	conn, frames := startSignalRTestConnection(t)

	for i, expected := range testHeartbeatExamples() {
		t.Run("example_"+string(rune('1'+i-0)), func(t *testing.T) {
			if err := runtime.invoke(conn, "HeartbeatV2", expected); err != nil {
				t.Fatalf("invoke HeartbeatV2 falhou: %v", err)
			}

			select {
			case raw := <-frames:
				got := decodeHeartbeatFromSignalRFrame(t, raw)
				if !reflect.DeepEqual(got, expected) {
					t.Fatalf("heartbeat SignalR divergente\nexpected=%+v\ngot=%+v", expected, got)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timeout aguardando frame HeartbeatV2")
			}
		})
	}
}

func TestSignalRFlow_HandshakeRegisterAndHeartbeat_WithProvidedData(t *testing.T) {
	runtime := NewRuntime(Options{})
	conn, frames, sendFromServer := startSignalRDuplexTestConnection(t)

	if err := runtime.sendHandshake(conn); err != nil {
		t.Fatalf("sendHandshake falhou: %v", err)
	}
	handshakeFrame := string(nextSignalRFrame(t, frames))
	if handshakeFrame != "{\"protocol\":\"json\",\"version\":1}\x1e" {
		t.Fatalf("frame de handshake inesperado: %q", handshakeFrame)
	}

	if err := sendFromServer([]byte("{}\x1e")); err != nil {
		t.Fatalf("falha ao enviar ack inicial do servidor: %v", err)
	}
	if err := runtime.waitHandshakeAck(conn, 2*time.Second); err != nil {
		t.Fatalf("waitHandshakeAck falhou: %v", err)
	}

	const observedTLSHash = "AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899"
	handshakeErrCh := make(chan error, 1)
	go func() {
		handshakeErrCh <- runtime.completeSecureHandshake(context.Background(), conn, observedTLSHash, 2*time.Second)
	}()

	if err := sendFromServer(mustSignalRInvocationFrame(t, "HandshakeChallenge", "nonce-1", "expected-hash")); err != nil {
		t.Fatalf("falha ao enviar HandshakeChallenge de teste: %v", err)
	}
	secureHandshakeCall := decodeSignalRInvocationFrame(t, nextSignalRFrame(t, frames))
	if secureHandshakeCall.Target != "SecureHandshakeAsync" {
		t.Fatalf("target do secure handshake = %q, esperado SecureHandshakeAsync", secureHandshakeCall.Target)
	}
	if len(secureHandshakeCall.Arguments) != 1 {
		t.Fatalf("SecureHandshakeAsync arguments = %d, esperado 1", len(secureHandshakeCall.Arguments))
	}
	var gotObservedHash string
	if err := json.Unmarshal(secureHandshakeCall.Arguments[0], &gotObservedHash); err != nil {
		t.Fatalf("argumento de SecureHandshakeAsync invalido: %v", err)
	}
	if gotObservedHash != observedTLSHash {
		t.Fatalf("hash enviado no SecureHandshakeAsync = %q, esperado %q", gotObservedHash, observedTLSHash)
	}

	if err := sendFromServer(mustSignalRInvocationFrame(t, "HandshakeAck", true, "ok")); err != nil {
		t.Fatalf("falha ao enviar HandshakeAck de teste: %v", err)
	}
	if err := <-handshakeErrCh; err != nil {
		t.Fatalf("completeSecureHandshake falhou: %v", err)
	}

	const localIP = "192.168.1.50"
	if err := runtime.invoke(conn, "RegisterAgent", testHomologAgentID, localIP); err != nil {
		t.Fatalf("invoke RegisterAgent falhou: %v", err)
	}
	registerCall := decodeSignalRInvocationFrame(t, nextSignalRFrame(t, frames))
	if registerCall.Target != "RegisterAgent" {
		t.Fatalf("target de registro = %q, esperado RegisterAgent", registerCall.Target)
	}
	if len(registerCall.Arguments) != 2 {
		t.Fatalf("RegisterAgent arguments = %d, esperado 2", len(registerCall.Arguments))
	}
	var gotAgentID string
	if err := json.Unmarshal(registerCall.Arguments[0], &gotAgentID); err != nil {
		t.Fatalf("agentId do RegisterAgent invalido: %v", err)
	}
	if gotAgentID != testHomologAgentID {
		t.Fatalf("agentId enviado no RegisterAgent = %q, esperado %q", gotAgentID, testHomologAgentID)
	}

	heartbeatExamples := []AgentHeartbeat{
		{
			AgentId:      testHomologAgentID,
			IpAddress:    localIP,
			AgentVersion: "1.0.0",
			TimestampUtc: "2026-05-05T03:24:40Z",
		},
		{
			AgentId:       testHomologAgentID,
			IpAddress:     localIP,
			Hostname:      "HOMOLOG-WIN-01",
			AgentVersion:  "1.0.0",
			TimestampUtc:  "2026-05-05T03:24:55Z",
			CpuPercent:    hbFloat(17.3),
			MemoryPercent: hbFloat(42.1),
			MemoryTotalGb: hbFloat(16.0),
			MemoryUsedGb:  hbFloat(6.7),
			DiskPercent:   hbFloat(58.2),
			DiskTotalGb:   hbFloat(512.0),
			DiskUsedGb:    hbFloat(298.0),
			P2pPeers:      hbInt(3),
			UptimeSeconds: hbInt64(15),
			ProcessCount:  hbInt(120),
		},
		{
			AgentId:      testHomologAgentID,
			IpAddress:    localIP,
			Hostname:     "HOMOLOG-WIN-02",
			AgentVersion: "1.0.0",
			TimestampUtc: "2026-05-05T03:25:10Z",
		},
	}

	for i, expected := range heartbeatExamples {
		t.Run("signalr_flow_example_"+string(rune('1'+i-0)), func(t *testing.T) {
			if err := runtime.invoke(conn, "HeartbeatV2", expected); err != nil {
				t.Fatalf("invoke HeartbeatV2 falhou: %v", err)
			}
			got := decodeHeartbeatFromSignalRFrame(t, nextSignalRFrame(t, frames))
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("HeartbeatV2 divergente\nexpected=%+v\ngot=%+v", expected, got)
			}
		})
	}
}

func TestSignalRFlow_ExecuteCommandReturnsCommandResult(t *testing.T) {
	runtime := NewRuntime(Options{
		HandleCommand: func(parent context.Context, cmdType string, payload any) (bool, int, string, string) {
			if strings.TrimSpace(strings.ToLower(cmdType)) != "powershell" {
				return true, 1, "", "tipo de comando inesperado"
			}
			return true, 0, "ok-from-test", ""
		},
	})
	conn, frames, _ := startSignalRDuplexTestConnection(t)

	execPayload := mustSignalRInvocationFrame(t, "ExecuteCommand", "cmd-123", "powershell", map[string]any{"command": "Get-Date"})
	if err := runtime.handleSignalRPayload(context.Background(), conn, execPayload); err != nil {
		t.Fatalf("handleSignalRPayload(ExecuteCommand) falhou: %v", err)
	}

	resultCall := decodeSignalRInvocationFrame(t, nextSignalRFrame(t, frames))
	if resultCall.Target != "CommandResult" {
		t.Fatalf("target de resposta comando = %q, esperado CommandResult", resultCall.Target)
	}
	if len(resultCall.Arguments) != 4 {
		t.Fatalf("CommandResult arguments = %d, esperado 4", len(resultCall.Arguments))
	}

	var cmdID string
	if err := json.Unmarshal(resultCall.Arguments[0], &cmdID); err != nil {
		t.Fatalf("cmdId do CommandResult invalido: %v", err)
	}
	if cmdID != "cmd-123" {
		t.Fatalf("cmdId retornado = %q, esperado cmd-123", cmdID)
	}

	var exitCode int
	if err := json.Unmarshal(resultCall.Arguments[1], &exitCode); err != nil {
		t.Fatalf("exitCode do CommandResult invalido: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode retornado = %d, esperado 0", exitCode)
	}

	var output string
	if err := json.Unmarshal(resultCall.Arguments[2], &output); err != nil {
		t.Fatalf("output do CommandResult invalido: %v", err)
	}
	if output != "ok-from-test" {
		t.Fatalf("output retornado = %q, esperado ok-from-test", output)
	}
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
	handler := runtime.natsCommandHandler(context.Background(), nc, cfg, subjects)

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
