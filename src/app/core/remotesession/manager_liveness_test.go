//go:build windows

package remotesession

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"discovery/app/core/sessioncontrol"
	"discovery/app/core/sessionlive"
)

func startLivenessNATS(t *testing.T) *natsserver.Server {
	t.Helper()
	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatalf("criar NATS: %v", err)
	}
	go server.Start()
	if !server.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS nao ficou pronto")
	}
	t.Cleanup(server.Shutdown)
	return server
}

func livenessControlSubject(sessionID string) string {
	return fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote.session.%s.control",
		stripHyphens("client-1"), stripHyphens("site-1"), stripHyphens("agent-1"), stripHyphens(sessionID))
}

func publishViewerPing(t *testing.T, nc *nats.Conn, subject, sessionID string) {
	t.Helper()
	raw, err := sessioncontrol.Encode(sessioncontrol.Envelope{
		Type: ControlTypePing, SessionID: sessionID, From: sessioncontrol.RoleViewer,
	}, sessioncontrol.RemoteSessionTypes)
	if err != nil {
		t.Fatalf("encode ping: %v", err)
	}
	if err := nc.Publish(subject, raw); err != nil {
		t.Fatalf("publish ping: %v", err)
	}
	_ = nc.Flush()
}

// TestManager_LivenessPingPongAndViewerTimeout cobre o caminho real (NATS de
// verdade): o agente envia ping, responde pong ao ping do viewer e encerra por
// viewer-timeout quando o viewer some, publicando closed com o motivo.
func TestManager_LivenessPingPongAndViewerTimeout(t *testing.T) {
	server := startLivenessNATS(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	agentConn, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("conectar agent: %v", err)
	}
	t.Cleanup(agentConn.Close)
	viewerConn, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("conectar viewer: %v", err)
	}
	t.Cleanup(viewerConn.Close)

	mgr := NewManager(agentConn)
	mgr.SetNatsConn(agentConn, "client-1", "site-1", "agent-1")

	subject := livenessControlSubject("sess-1")
	pongs := make(chan struct{}, 8)
	closed := make(chan string, 4)
	var replied atomic.Bool
	sub, err := viewerConn.Subscribe(subject, func(msg *nats.Msg) {
		env, derr := sessioncontrol.Decode(msg.Data, sessioncontrol.RemoteSessionTypes)
		if derr != nil {
			return
		}
		switch env.Type {
		case ControlTypePing:
			if env.From == sessioncontrol.RoleAgent && replied.CompareAndSwap(false, true) {
				publishViewerPing(t, viewerConn, subject, "sess-1")
			}
		case ControlTypePong:
			select {
			case pongs <- struct{}{}:
			default:
			}
		case ControlTypeClosed:
			reason, _ := env.Payload["reason"].(string)
			select {
			case closed <- reason:
			default:
			}
		}
	})
	if err != nil {
		t.Fatalf("subscribe viewer: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	_ = viewerConn.Flush()

	now := time.Now().UTC()
	ok, msg := mgr.HandleCommand(ctx, map[string]any{
		"action":          "start",
		"sessionId":       "sess-1",
		"kind":            "proxy",
		"expiresAtUtc":    now.Add(10 * time.Minute).Format(time.RFC3339),
		"maxExpiresAtUtc": now.Add(2 * time.Hour).Format(time.RFC3339),
		"liveness": map[string]any{
			"pingIntervalSeconds":    1,
			"missedPingsBeforeClose": 1,
			"initialGraceSeconds":    1,
		},
	})
	if !ok {
		t.Fatalf("start falhou: %s", msg)
	}

	select {
	case <-pongs:
	case <-time.After(6 * time.Second):
		t.Fatal("agente nao devolveu pong ao ping do viewer")
	}

	select {
	case reason := <-closed:
		if reason != "viewer-timeout" {
			t.Fatalf("motivo do closed = %q, want viewer-timeout", reason)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("sessao nao encerrou por viewer-timeout")
	}

	if mgr.CountActive() != 0 {
		t.Fatalf("sessao deveria ter sido removida, ativas=%d", mgr.CountActive())
	}
}

// TestManager_LegacyKeyframeDoesNotCountAsPresence garante que o keyframe do
// viewer antigo e aceito (decode) mas NAO marca o peer como vivo: a liveness
// so vem de ping/pong, para nao manter sessao fantasma.
func TestManager_LegacyKeyframeDoesNotCountAsPresence(t *testing.T) {
	server := startLivenessNATS(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	agentConn, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("conectar agent: %v", err)
	}
	t.Cleanup(agentConn.Close)
	viewerConn, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("conectar viewer: %v", err)
	}
	t.Cleanup(viewerConn.Close)

	mgr := NewManager(agentConn)
	mgr.SetNatsConn(agentConn, "client-1", "site-1", "agent-1")

	subject := livenessControlSubject("sess-2")
	if _, err := viewerConn.Subscribe(subject, func(*nats.Msg) {}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	_ = viewerConn.Flush()

	now := time.Now().UTC()
	ok, msg := mgr.HandleCommand(ctx, map[string]any{
		"action":          "start",
		"sessionId":       "sess-2",
		"kind":            "proxy",
		"expiresAtUtc":    now.Add(10 * time.Minute).Format(time.RFC3339),
		"maxExpiresAtUtc": now.Add(2 * time.Hour).Format(time.RFC3339),
		"liveness":        map[string]any{"pingIntervalSeconds": 1, "missedPingsBeforeClose": 1, "initialGraceSeconds": 1},
	})
	if !ok {
		t.Fatalf("start falhou: %s", msg)
	}

	if err := viewerConn.Publish(subject, []byte("{\"action\":\"keyframe\"}")); err != nil {
		t.Fatalf("publish keyframe: %v", err)
	}
	_ = viewerConn.Flush()

	time.Sleep(300 * time.Millisecond)

	sessions := mgr.GetActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("esperava 1 sessao ativa, got %d", len(sessions))
	}
	if sessions[0].live == nil || sessions[0].live.PeerSeen() {
		t.Fatalf("keyframe legado NAO deveria marcar o peer como visto")
	}
}

// TestManager_NoPeerSignalExpiresAtInitialDeadline valida o fallback de viewer
// antigo: sem nenhum ping, a sessao cai no prazo original do start.
func TestManager_NoPeerSignalExpiresAtInitialDeadline(t *testing.T) {
	mgr := NewManager(nil)
	session := &Session{
		ID:              "sess-3",
		Kind:            "proxy",
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
		initialDeadline: time.Now().Add(-time.Second),
		live: sessionlive.NewPeer(sessionlive.Config{
			Interval: time.Second, MissesAllowed: 1, InitialGrace: time.Minute,
		}, time.Now().Add(-2*time.Minute)),
	}
	mgr.sessions["sess-3"] = session

	mgr.tickLiveness(session)

	if mgr.CountActive() != 0 {
		t.Fatalf("sessao sem primeiro sinal deveria encerrar no prazo original")
	}
}
