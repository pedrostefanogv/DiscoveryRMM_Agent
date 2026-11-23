package agentconn

import (
	"context"
	"discovery/app/core/testutil"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func startEmbeddedJetStreamNATS(t *testing.T) (*natsserver.Server, *nats.Conn, nats.JetStreamContext) {
	t.Helper()
	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  testutil.TempDir(t),
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("natsserver.NewServer: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		t.Fatalf("embedded NATS server did not become ready in time")
	}
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	t.Cleanup(nc.Close)

	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("nc.JetStream: %v", err)
	}
	return srv, nc, js
}

func TestNATSFanoutAndSupportChatStreamNames_CompactLowercaseHex(t *testing.T) {
	cfg := Config{
		ClientID: "019DEAD3-70EE-7C66-8662-7558E0B23AD5",
		SiteID:   "019DEAD3-845A-79E3-9769-F94A0F6A114F",
		AgentID:  "019DEF54-FDE2-7CED-AC3C-3C2DE42433AA",
	}

	siteStreams := natsFanoutStreamCandidates(cfg, natsCommandRouteSite)
	if len(siteStreams) != 2 || siteStreams[0] != "DISCOVERY_FANOUT_COMMANDS_019dead370ee7c6686627558e0b23ad5" || siteStreams[1] != "DISCOVERY_FANOUT_COMMANDS" {
		t.Fatalf("unexpected site fanout stream candidates: %#v", siteStreams)
	}

	clientStreams := natsFanoutStreamCandidates(cfg, natsCommandRouteClient)
	if len(clientStreams) != 2 || clientStreams[0] != "DISCOVERY_FANOUT_COMMANDS_019dead370ee7c6686627558e0b23ad5" {
		t.Fatalf("unexpected client fanout stream candidates: %#v", clientStreams)
	}

	globalStreams := natsFanoutStreamCandidates(cfg, natsCommandRouteGlobal)
	if len(globalStreams) != 2 || globalStreams[0] != "DISCOVERY_FANOUT_COMMANDS_GLOBAL" || globalStreams[1] != "DISCOVERY_FANOUT_COMMANDS" {
		t.Fatalf("unexpected global fanout stream candidates: %#v", globalStreams)
	}

	chatStreams := natsSupportChatStreamCandidates(cfg.ClientID)
	if len(chatStreams) != 2 || chatStreams[0] != "DISCOVERY_SUPPORT_CHAT_019dead370ee7c6686627558e0b23ad5" || chatStreams[1] != "DISCOVERY_SUPPORT_CHAT" {
		t.Fatalf("unexpected support chat stream candidates: %#v", chatStreams)
	}

	if got := natsFanoutDurableName(cfg, natsCommandRouteSite); got != "agent-019def54fde27cedac3c3c2d-site" {
		t.Fatalf("unexpected fanout durable name: %q", got)
	}
	if got := natsSupportChatDurableName(cfg.AgentID); got != "agent-019def54fde27cedac3c3c2de42433aa-supportchat" {
		t.Fatalf("unexpected supportchat durable name: %q", got)
	}

	other := Config{ClientID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", AgentID: cfg.AgentID}
	if natsFanoutStreamCandidates(other, natsCommandRouteSite)[0] == siteStreams[0] {
		t.Fatalf("expected disjoint stream names across tenants")
	}
}

func TestValidateAgentIdentityJWTClaims_AcceptsTenantScopedJetStreamACLs(t *testing.T) {
	clientID := "019dead3-70ee-7c66-8662-7558e0b23ad5"
	siteID := "019dead3-845a-79e3-9769-f94a0f6a114f"
	agentID := "019def54-fde2-7ced-ac3c-3c2de42433aa"
	subjects, err := resolveNATSSubjects(Config{ClientID: clientID, SiteID: siteID, AgentID: agentID})
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	pubAllow := []string{
		subjects.Heartbeat,
		subjects.Result,
		subjects.Hardware,
		subjects.RemoteDebugLog,
		subjects.RemoteDebugControl,
		fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote.session.>",
			stripSubjectHyphens(subjects.ClientID),
			stripSubjectHyphens(subjects.SiteID),
			stripSubjectHyphens(subjects.AgentID)),
		subjects.SupportChatAgent,
		"$JS.API.STREAM.NAMES",
	}
	pubAllow = append(pubAllow, expectedTenantJetStreamPubSubjects(clientID)...)

	jwt := buildTestJWT(t, map[string]any{
		"nats": map[string]any{
			"sub": map[string]any{
				"allow": []string{
					subjects.CommandAgent,
					subjects.CommandSiteFanout,
					subjects.CommandClientFanout,
					subjects.CommandGlobalFanout,
					subjects.GlobalPong,
					subjects.SyncPing,
					subjects.P2PDiscovery,
					subjects.RemoteDebugControl,
					subjects.SupportChatServer,
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote.session.>",
						stripSubjectHyphens(subjects.ClientID),
						stripSubjectHyphens(subjects.SiteID),
						stripSubjectHyphens(subjects.AgentID)),
					"_INBOX.>",
				},
			},
			"pub": map[string]any{
				"allow": pubAllow,
			},
		},
	})

	if err := validateAgentIdentityJWTClaims(jwt, subjects); err != nil {
		t.Fatalf("expected tenant-scoped JetStream ACLs to pass: %v", err)
	}
	if !isLegacyWildcardJetStreamSubject("$JS.ACK.DISCOVERY_FANOUT_COMMANDS.>") {
		t.Fatalf("expected legacy wildcard fanout subject to be detected")
	}
	if isLegacyWildcardJetStreamSubject("$JS.ACK.DISCOVERY_FANOUT_COMMANDS_019dead370ee7c6686627558e0b23ad5.>") {
		t.Fatalf("did not expect tenant-scoped subject to be flagged as legacy wildcard")
	}
}

func TestSubscribeFanoutCommand_BindsTenantStreamWithEmbeddedJetStream(t *testing.T) {
	_, nc, js := startEmbeddedJetStreamNATS(t)
	cfg := Config{
		ClientID: "019dead3-70ee-7c66-8662-7558e0b23ad5",
		SiteID:   "019dead3-845a-79e3-9769-f94a0f6a114f",
		AgentID:  "019def54-fde2-7ced-ac3c-3c2de42433aa",
	}
	subjects, err := resolveNATSSubjects(cfg)
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	tenantStream := "DISCOVERY_FANOUT_COMMANDS_" + normalizeCompactGUID(cfg.ClientID)
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     tenantStream,
		Subjects: []string{subjects.CommandSiteFanout, subjects.CommandClientFanout},
	}); err != nil {
		t.Fatalf("AddStream: %v", err)
	}

	received := make(chan string, 1)
	rt := NewRuntime(Options{
		HandleCommand: func(_ context.Context, cmdType string, _ any) (bool, int, string, string) {
			received <- cmdType
			return true, 0, "ok", ""
		},
	})

	now := time.Now().UTC()
	payload, _ := json.Marshal(map[string]any{
		"dispatchId":     "disp-fanout-1",
		"commandId":      "cmd-fanout-1",
		"commandType":    "ping",
		"targetScope":    "site",
		"targetClientId": cfg.ClientID,
		"targetSiteId":   cfg.SiteID,
		"idempotencyKey": "idem-fanout-1",
		"issuedAtUtc":    now.Format(time.RFC3339),
		"expiresAtUtc":   now.Add(5 * time.Minute).Format(time.RFC3339),
	})
	if _, err := js.Publish(subjects.CommandSiteFanout, payload); err != nil {
		t.Fatalf("js.Publish: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rt.subscribeFanoutCommand(ctx, nc, cfg, subjects, subjects.CommandSiteFanout, natsCommandRouteSite); err != nil {
		t.Fatalf("subscribeFanoutCommand: %v", err)
	}

	select {
	case cmdType := <-received:
		if cmdType != "ping" {
			t.Fatalf("unexpected commandType: %q", cmdType)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for JetStream replay command")
	}

	durable := natsFanoutDurableName(cfg, natsCommandRouteSite)
	deadline := time.Now().Add(3 * time.Second)
	for {
		info, err := js.ConsumerInfo(tenantStream, durable)
		if err != nil {
			t.Fatalf("ConsumerInfo(%s, %s): %v", tenantStream, durable, err)
		}
		if info.NumAckPending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected NumAckPending == 0, got %d", info.NumAckPending)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestSubscribeFanoutCommand_FallsBackToLegacyStreamAndCore(t *testing.T) {
	t.Run("legacy_stream_fallback", func(t *testing.T) {
		_, nc, js := startEmbeddedJetStreamNATS(t)
		cfg := Config{
			ClientID: "019dead3-70ee-7c66-8662-7558e0b23ad5",
			SiteID:   "019dead3-845a-79e3-9769-f94a0f6a114f",
			AgentID:  "019def54-fde2-7ced-ac3c-3c2de42433aa",
		}
		subjects, err := resolveNATSSubjects(cfg)
		if err != nil {
			t.Fatalf("resolveNATSSubjects: %v", err)
		}
		if _, err := js.AddStream(&nats.StreamConfig{
			Name:     fanoutStreamPrefix,
			Subjects: []string{"tenant.*.agents.command"},
		}); err != nil {
			t.Fatalf("AddStream: %v", err)
		}

		received := make(chan string, 1)
		rt := NewRuntime(Options{
			HandleCommand: func(_ context.Context, cmdType string, _ any) (bool, int, string, string) {
				received <- cmdType
				return true, 0, "ok", ""
			},
		})
		now := time.Now().UTC()
		payload, _ := json.Marshal(map[string]any{
			"dispatchId":     "disp-legacy-1",
			"commandId":      "cmd-legacy-1",
			"commandType":    "ping-legacy",
			"targetScope":    "client",
			"targetClientId": cfg.ClientID,
			"idempotencyKey": "idem-legacy-1",
			"issuedAtUtc":    now.Format(time.RFC3339),
			"expiresAtUtc":   now.Add(5 * time.Minute).Format(time.RFC3339),
		})
		if _, err := js.Publish(subjects.CommandClientFanout, payload); err != nil {
			t.Fatalf("js.Publish: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := rt.subscribeFanoutCommand(ctx, nc, cfg, subjects, subjects.CommandClientFanout, natsCommandRouteClient); err != nil {
			t.Fatalf("subscribeFanoutCommand: %v", err)
		}

		select {
		case cmdType := <-received:
			if cmdType != "ping-legacy" {
				t.Fatalf("unexpected commandType: %q", cmdType)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for legacy JetStream replay command")
		}
	})

	t.Run("core_nats_fallback", func(t *testing.T) {
		_, nc, _ := startEmbeddedJetStreamNATS(t)
		cfg := Config{
			ClientID: "019dead3-70ee-7c66-8662-7558e0b23ad5",
			SiteID:   "019dead3-845a-79e3-9769-f94a0f6a114f",
			AgentID:  "019def54-fde2-7ced-ac3c-3c2de42433aa",
		}
		subjects, err := resolveNATSSubjects(cfg)
		if err != nil {
			t.Fatalf("resolveNATSSubjects: %v", err)
		}
		received := make(chan string, 1)
		rt := NewRuntime(Options{
			HandleCommand: func(_ context.Context, cmdType string, _ any) (bool, int, string, string) {
				received <- cmdType
				return true, 0, "ok", ""
			},
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := rt.subscribeFanoutCommand(ctx, nc, cfg, subjects, subjects.CommandGlobalFanout, natsCommandRouteGlobal); err != nil {
			t.Fatalf("subscribeFanoutCommand: %v", err)
		}

		now := time.Now().UTC()
		payload, _ := json.Marshal(map[string]any{
			"dispatchId":     "disp-core-1",
			"commandId":      "cmd-core-1",
			"commandType":    "ping-core",
			"targetScope":    "global",
			"idempotencyKey": "idem-core-1",
			"issuedAtUtc":    now.Format(time.RFC3339),
			"expiresAtUtc":   now.Add(5 * time.Minute).Format(time.RFC3339),
		})
		if err := nc.Publish(subjects.CommandGlobalFanout, payload); err != nil {
			t.Fatalf("nc.Publish: %v", err)
		}
		_ = nc.Flush()

		select {
		case cmdType := <-received:
			if cmdType != "ping-core" {
				t.Fatalf("unexpected commandType: %q", cmdType)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for core fallback command")
		}
	})
}

func TestSubscribeSupportChat_BindsTenantStreamWithEmbeddedJetStream(t *testing.T) {
	_, nc, js := startEmbeddedJetStreamNATS(t)
	cfg := Config{
		ClientID: "019dead3-70ee-7c66-8662-7558e0b23ad5",
		SiteID:   "019dead3-845a-79e3-9769-f94a0f6a114f",
		AgentID:  "019def54-fde2-7ced-ac3c-3c2de42433aa",
	}
	subjects, err := resolveNATSSubjects(cfg)
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	supportStream := "DISCOVERY_SUPPORT_CHAT_" + normalizeCompactGUID(cfg.ClientID)
	streamSubject := fmt.Sprintf("tenant.%s.site.*.agent.*.supportchat.*", cfg.ClientID)
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     supportStream,
		Subjects: []string{streamSubject},
	}); err != nil {
		t.Fatalf("AddStream: %v", err)
	}

	var count atomic.Int32
	delivered := make(chan string, 1)
	rt := NewRuntime(Options{
		HandleSupportChatEnvelope: func(_ context.Context, raw []byte) error {
			count.Add(1)
			delivered <- string(raw)
			return nil
		},
	})

	env := `{"type":"SessionOpened","sessionId":"s-1"}`
	if _, err := js.Publish(subjects.SupportChatServer, []byte(env)); err != nil {
		t.Fatalf("js.Publish: %v", err)
	}

	if err := rt.subscribeSupportChat(nc, subjects); err != nil {
		t.Fatalf("subscribeSupportChat: %v", err)
	}

	select {
	case got := <-delivered:
		if !strings.Contains(got, `"SessionOpened"`) {
			t.Fatalf("unexpected supportchat envelope: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for supportchat JetStream replay")
	}

	durable := natsSupportChatDurableName(cfg.AgentID)
	deadline := time.Now().Add(3 * time.Second)
	for {
		info, err := js.ConsumerInfo(supportStream, durable)
		if err != nil {
			t.Fatalf("ConsumerInfo(%s, %s): %v", supportStream, durable, err)
		}
		if info.NumAckPending == 0 && count.Load() == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected NumAckPending == 0, got %d", info.NumAckPending)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
