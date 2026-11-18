package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"discovery/app/core/remotesession"
)

func TestParseRDPTunnelFlags_AndURLResolution(t *testing.T) {
	// Token precedence: env -> stdin -> file -> --token
	tmpDir := t.TempDir()
	tokenFile := filepath.Join(tmpDir, "tok.txt")
	if err := os.WriteFile(tokenFile, []byte("  file-secret\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	cfg, showHelp, err := parseRDPTunnelConfig(
		[]string{
			"--rdp-tunnel",
			"--local-port=33999",
			"--api-url=https://rmm.example.com/base/",
			"--agent-id=ag-1",
			"--session-id=sess-1",
			"--token-file=" + tokenFile,
		},
		func(k string) string {
			if k == "DISCOVERY_TUNNEL_TOKEN" {
				return "env-secret"
			}
			return ""
		},
		nil,
		io.Discard,
	)
	if err != nil || showHelp {
		t.Fatalf("parseRDPTunnelConfig: err=%v showHelp=%v", err, showHelp)
	}
	if cfg.ListenAddr != "127.0.0.1:33999" || cfg.Token != "env-secret" {
		t.Fatalf("cfg inesperada: %+v", cfg)
	}
	wsURL, err := resolveRDPTunnelWSURL(cfg)
	if err != nil {
		t.Fatalf("resolveRDPTunnelWSURL: %v", err)
	}
	wantWS := "wss://rmm.example.com/base/api/v1/remote-sessions/ag-1/sess-1/tunnel/ws"
	if wsURL != wantWS {
		t.Fatalf("wsURL = %q, want %q", wsURL, wantWS)
	}

	// Token via stdin
	cfgStdin, _, err := parseRDPTunnelConfig(
		[]string{"/rdp-tunnel", "/token-stdin", "/ws-url=ws://127.0.0.1:8080/ws"},
		func(string) string { return "" },
		strings.NewReader("stdin-secret\nsecond-line"),
		io.Discard,
	)
	if err != nil || cfgStdin.Token != "stdin-secret" {
		t.Fatalf("token-stdin falhou: err=%v token=%q", err, cfgStdin.Token)
	}

	// Non-loopback listen rejected without --allow-non-loopback
	if _, _, err := parseRDPTunnelConfig(
		[]string{"--rdp-tunnel", "--listen=0.0.0.0:33890"},
		func(string) string { return "" },
		nil,
		io.Discard,
	); err == nil {
		t.Fatal("esperava rejeicao de bind nao-loopback 0.0.0.0:33890")
	}

	// Help flag
	_, help, err := parseRDPTunnelConfig([]string{"--rdp-tunnel", "--help"}, nil, nil, io.Discard)
	if err != nil || !help {
		t.Fatalf("esperava showHelp=true, got help=%v err=%v", help, err)
	}
}

func TestRDPTunnel_EndToEndLoopbackToWebSocket(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	var (
		authHeader string
		gotOpen    = make(chan remotesession.TunnelControlMsg, 1)
		gotBinary  = make(chan []byte, 4)
		mu         sync.Mutex
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeader = r.Header.Get("Authorization")
		mu.Unlock()

		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()

		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			isBin, opByte, sid, payload, ctrl, derr := remotesession.DecodeTunnelFrame(data)
			if derr != nil {
				continue
			}
			if !isBin && ctrl != nil && ctrl.Op == "open" {
				gotOpen <- *ctrl
				resp, _ := json.Marshal(remotesession.TunnelControlMsg{
					StreamID:   sid,
					Op:         "opened",
					TargetHost: ctrl.TargetHost,
					TargetPort: ctrl.TargetPort,
					Protocol:   ctrl.Protocol,
				})
				_ = c.WriteMessage(websocket.TextMessage, resp)
				continue
			}
			if isBin && opByte == remotesession.TunnelOpData && mt == websocket.BinaryMessage {
				gotBinary <- append([]byte(nil), payload...)
				// Echo back as binary frame
				_ = c.WriteMessage(websocket.BinaryMessage, remotesession.EncodeTunnelBinaryData(nil, sid, payload))
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	cfg := rdpTunnelConfig{
		ListenAddr: "127.0.0.1:0",
		TargetHost: "127.0.0.1",
		TargetPort: 3389,
		Protocol:   "rdp",
		Token:      "jwt-bearer-secret",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fwd, err := startRDPTunnelForwarder(ctx, cfg, wsURL)
	if err != nil {
		t.Fatalf("startRDPTunnelForwarder: %v", err)
	}
	defer fwd.Close()

	if err := fwd.ListenAndServe(ctx); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}

	tcpConn, err := net.DialTimeout("tcp", fwd.Addr(), 3*time.Second)
	if err != nil {
		t.Fatalf("dial local forwarder: %v", err)
	}
	defer tcpConn.Close()

	select {
	case openMsg := <-gotOpen:
		if openMsg.TargetHost != "127.0.0.1" || openMsg.TargetPort != 3389 || openMsg.Protocol != "rdp" {
			t.Fatalf("openMsg inesperado: %+v", openMsg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando op=open no WebSocket")
	}

	mu.Lock()
	gotAuth := authHeader
	mu.Unlock()
	if gotAuth != "Bearer jwt-bearer-secret" {
		t.Fatalf("Authorization header = %q, want Bearer jwt-bearer-secret", gotAuth)
	}

	want := []byte{0x03, 0x00, 0x00, 0x13, 0x0e, 0xe0, 0x00, 0x00, 0xff, 0x80}
	if _, err := tcpConn.Write(want); err != nil {
		t.Fatalf("write tcp: %v", err)
	}

	select {
	case got := <-gotBinary:
		if !bytes.Equal(got, want) {
			t.Fatalf("ws binary mismatch: got %x want %x", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando frame binario no WS")
	}

	_ = tcpConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, len(want))
	if _, err := io.ReadFull(tcpConn, buf); err != nil {
		t.Fatalf("read echoed tcp bytes: %v", err)
	}
	if !bytes.Equal(buf, want) {
		t.Fatalf("echoed tcp bytes mismatch: got %x want %x", buf, want)
	}
}

func TestRDPTunnel_PreflightOnlyMode(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()

		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		_, _, _, _, ctrl, derr := remotesession.DecodeTunnelFrame(data)
		if derr == nil && ctrl != nil && ctrl.Op == "preflight" {
			reply, _ := json.Marshal(remotesession.TunnelControlMsg{
				StreamID: 0,
				Op:       "preflight",
				RdpStatus: &remotesession.RDPStatus{
					Ready:            false,
					TermServiceState: "stopped",
					Port:             3389,
					Issues:           []string{remotesession.IssueTermServiceNotRunning},
					CheckedAtUtc:     "2026-11-18T00:00:00Z",
				},
			})
			_ = c.WriteMessage(websocket.TextMessage, reply)
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	code := runRDPTunnelCLI([]string{
		"--rdp-tunnel",
		"--preflight-only",
		"--ws-url=" + wsURL,
	})
	if code != 2 {
		t.Fatalf("preflight-only com Ready=false retornou exit code %d, want 2", code)
	}
}
