//go:build windows

package remotesession

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/core/netproxy"
)

func TestPolicy_KindRightsAndProxyPolicy(t *testing.T) {
	if got := ParseRemoteSessionKind("  RDP "); got != KindRdp {
		t.Fatalf("ParseRemoteSessionKind(\"  RDP \") = %q", got)
	}
	if got := ParseRemoteSessionKind(float64(6)); got != KindRdp {
		t.Fatalf("ParseRemoteSessionKind(6) = %q", got)
	}
	if got := ParseRemoteSessionKind(4); got != KindAll {
		t.Fatalf("ParseRemoteSessionKind(4) = %q, want all", got)
	}
	if got := ParseRemoteSessionKind(5); got != KindProcesses {
		t.Fatalf("ParseRemoteSessionKind(5) = %q, want processes", got)
	}

	if got := ParseRemoteSessionRights(nil); got != RightsNone {
		t.Fatalf("nil rights = %d, want 0", got)
	}
	if got := ParseRemoteSessionRights(float64(RightsNoProxy | RightsNoRdp)); !got.Has(RightsNoProxy) || !got.Has(RightsNoRdp) {
		t.Fatalf("numeric rights = %d", got)
	}
	if got := ParseRemoteSessionRights("NoProxy, NoRdp"); !got.Has(RightsNoProxy) || !got.Has(RightsNoRdp) {
		t.Fatalf("string rights = %d", got)
	}

	p := ParseProxyPolicy(map[string]any{
		"proxy": map[string]any{
			"allowlist":        []any{"192.168.10.0/24"},
			"allowedPorts":     []any{float64(80), float64(443)},
			"maxResponseBytes": float64(2048),
		},
	}, KindProxy)
	if p.AllowLoopback {
		t.Fatal("allowLoopback deve ser false por padrao")
	}
	if p.MaxResponseBytes != 2048 || len(p.Allowlist) != 1 || len(p.AllowedPorts) != 2 || p.RdpHost != "127.0.0.1" {
		t.Fatalf("policy parse inesperada: %+v", p)
	}

	def := ParseProxyPolicy(nil, KindProxy)
	if def.MaxResponseBytes != DefaultProxyMaxResponseBytes || def.AllowLoopback {
		t.Fatalf("default policy inesperada: %+v", def)
	}
}

func TestManager_RightsEnforcementAndHasSession(t *testing.T) {
	srv := startLivenessNATS(t)
	nc, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(nc.Close)

	mgr := NewManager(nc)
	mgr.SetNatsConn(nc, "c1", "s1", "a1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// NoProxy bloqueia kind=proxy
	ok, msg := mgr.HandleCommand(ctx, map[string]any{
		"action":    "start",
		"sessionId": "aaaa0001",
		"kind":      "proxy",
		"rights":    float64(RightsNoProxy),
	})
	if ok || msg == "" {
		t.Fatalf("esperava recusa por NoProxy, got ok=%v msg=%q", ok, msg)
	}

	// NoRdp bloqueia kind=rdp
	ok, msg = mgr.HandleCommand(ctx, map[string]any{
		"action":    "start",
		"sessionId": "aaaa0002",
		"kind":      "rdp",
		"rights":    "NoRdp",
	})
	if ok || msg == "" {
		t.Fatalf("esperava recusa por NoRdp, got ok=%v msg=%q", ok, msg)
	}
	if mgr.CountActive() != 0 {
		t.Fatalf("nenhuma sessao deveria ficar ativa, got %d", mgr.CountActive())
	}
}

func TestManager_RunProxySessionEndToEndAndCtxCancelCleanup(t *testing.T) {
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("lan-device-ok"))
	}))
	defer httpSrv.Close()

	u, _ := url.Parse(httpSrv.URL)
	port, _ := strconv.Atoi(u.Port())

	natsSrv := startLivenessNATS(t)
	agentNC, _ := nats.Connect(natsSrv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(agentNC.Close)
	viewerNC, _ := nats.Connect(natsSrv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(viewerNC.Close)

	sid := "bbbb0001"
	readyCh := make(chan map[string]any, 1)
	respCh := make(chan netproxy.ProxyResponse, 1)

	rSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "proxy.ready"), func(m *nats.Msg) {
		var body map[string]any
		if json.Unmarshal(m.Data, &body) == nil {
			readyCh <- body
		}
	})
	defer rSub.Unsubscribe()
	pSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "proxy.resp"), func(m *nats.Msg) {
		var resp netproxy.ProxyResponse
		if json.Unmarshal(m.Data, &resp) == nil {
			respCh <- resp
		}
	})
	defer pSub.Unsubscribe()
	_ = viewerNC.Flush()

	mgr := NewManager(agentNC)
	mgr.SetNatsConn(agentNC, "client-1", "site-1", "agent-1")

	ctx, cancel := context.WithCancel(context.Background())
	ok, errMsg := mgr.HandleCommand(ctx, map[string]any{
		"action":    "start",
		"sessionId": sid,
		"kind":      "proxy",
		"proxy": map[string]any{
			"allowlist":     []any{"127.0.0.1/32"},
			"allowedPorts":  []any{float64(port)},
			"allowLoopback": true,
		},
	})
	if !ok {
		t.Fatalf("start proxy falhou: %s", errMsg)
	}
	if !mgr.HasSession(sid) {
		t.Fatal("HasSession deveria retornar true")
	}

	select {
	case ready := <-readyCh:
		if ready["mode"] != "proxy" || ready["status"] != "ready" {
			t.Fatalf("proxy.ready inesperado: %+v", ready)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando proxy.ready")
	}

	reqJSON, _ := json.Marshal(netproxy.ProxyRequest{
		RequestID: "req-e2e-1",
		Method:    "GET",
		URL:       httpSrv.URL + "/info",
	})
	_ = viewerNC.Publish(tunnelSubject(sid, "proxy.req"), reqJSON)
	_ = viewerNC.Flush()

	select {
	case resp := <-respCh:
		if !resp.Success || resp.RequestID != "req-e2e-1" {
			t.Fatalf("proxy.resp falhou: %+v", resp)
		}
		rawBody, _ := base64.StdEncoding.DecodeString(resp.BodyBase64)
		if string(rawBody) != "lan-device-ok" {
			t.Fatalf("body = %q, want lan-device-ok", string(rawBody))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando proxy.resp")
	}

	// Cancelar ctx deve encerrar runProxySession e remover a sessao de mgr.sessions
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.CountActive() == 0 && !mgr.HasSession(sid) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("sessao proxy nao foi removida apos ctx.Done (active=%d)", mgr.CountActive())
}

func TestManager_RunRdpSessionReadyAndStop(t *testing.T) {
	natsSrv := startLivenessNATS(t)
	agentNC, _ := nats.Connect(natsSrv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(agentNC.Close)
	viewerNC, _ := nats.Connect(natsSrv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(viewerNC.Close)

	sid := "cccc0001"
	readyCh := make(chan map[string]any, 1)
	rSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "proxy.ready"), func(m *nats.Msg) {
		var body map[string]any
		if json.Unmarshal(m.Data, &body) == nil {
			readyCh <- body
		}
	})
	defer rSub.Unsubscribe()
	_ = viewerNC.Flush()

	mgr := NewManager(agentNC)
	mgr.SetNatsConn(agentNC, "client-1", "site-1", "agent-1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ok, errMsg := mgr.HandleCommand(ctx, map[string]any{
		"action":    "start",
		"sessionId": sid,
		"kind":      "rdp",
		"rdpPort":   float64(3389),
	})
	if !ok {
		t.Fatalf("start rdp falhou: %s", errMsg)
	}

	select {
	case ready := <-readyCh:
		if ready["mode"] != "rdp" || ready["rdpHost"] != "127.0.0.1" {
			t.Fatalf("rdp proxy.ready inesperado: %+v", ready)
		}
		if _, hasStatus := ready["rdpStatus"]; !hasStatus {
			t.Fatalf("rdpStatus ausente em proxy.ready: %+v", ready)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timeout aguardando proxy.ready de sessao rdp")
	}

	ok, stopMsg := mgr.HandleCommand(ctx, map[string]any{
		"action":    "stop",
		"sessionId": sid,
	})
	if !ok {
		t.Fatalf("stop rdp falhou: %s", stopMsg)
	}
	if mgr.HasSession(sid) || mgr.CountActive() != 0 {
		t.Fatal("sessao rdp deveria ter sido encerrada pelo stop")
	}
}
