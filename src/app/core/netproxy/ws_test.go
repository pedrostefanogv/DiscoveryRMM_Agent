package netproxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSProxy_LifecycleAndBidirectionalTextBinary(t *testing.T) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	p := NewProxy(allowLoopbackForTestServer(t, srv.URL), 0)
	defer p.Close()

	outCh := make(chan ProxyResponse, 16)
	emit := func(b []byte) error {
		var r ProxyResponse
		if err := json.Unmarshal(b, &r); err == nil {
			outCh <- r
		}
		return nil
	}

	openReq, _ := json.Marshal(ProxyRequest{
		Type:      "ws_open",
		RequestID: "ws-1",
		URL:       wsURL,
	})
	if err := p.HandleRequestStream(context.Background(), openReq, emit); err != nil {
		t.Fatalf("ws_open: %v", err)
	}

	select {
	case r := <-outCh:
		if r.Type != "ws_opened" || !r.Success || r.RequestID != "ws-1" {
			t.Fatalf("esperava ws_opened, got %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando ws_opened")
	}

	// Text round-trip
	textReq, _ := json.Marshal(ProxyRequest{
		Type:        "ws_data",
		RequestID:   "ws-1",
		MessageType: "text",
		DataBase64:  base64.StdEncoding.EncodeToString([]byte("hello-ws")),
	})
	if err := p.HandleRequestStream(context.Background(), textReq, emit); err != nil {
		t.Fatalf("ws_data text: %v", err)
	}
	select {
	case r := <-outCh:
		got, _ := base64.StdEncoding.DecodeString(r.DataBase64)
		if r.Type != "ws_data" || r.MessageType != "text" || string(got) != "hello-ws" {
			t.Fatalf("esperava echo text, got %+v (%q)", r, string(got))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando ws_data text")
	}

	// Binary round-trip
	binPayload := []byte{0x00, 0x01, 0xff, 0xfe, 0x80}
	binReq, _ := json.Marshal(ProxyRequest{
		Type:        "ws_data",
		RequestID:   "ws-1",
		MessageType: "binary",
		DataBase64:  base64.StdEncoding.EncodeToString(binPayload),
	})
	if err := p.HandleRequestStream(context.Background(), binReq, emit); err != nil {
		t.Fatalf("ws_data binary: %v", err)
	}
	select {
	case r := <-outCh:
		got, _ := base64.StdEncoding.DecodeString(r.DataBase64)
		if r.Type != "ws_data" || r.MessageType != "binary" || !bytes.Equal(got, binPayload) {
			t.Fatalf("esperava echo binary, got %+v (%x)", r, got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando ws_data binary")
	}

	// Close
	closeReq, _ := json.Marshal(ProxyRequest{
		Type:        "ws_close",
		RequestID:   "ws-1",
		CloseCode:   websocket.CloseNormalClosure,
		CloseReason: "done",
	})
	if err := p.HandleRequestStream(context.Background(), closeReq, emit); err != nil {
		t.Fatalf("ws_close: %v", err)
	}
}

func TestWSProxy_BlocksSSRFAndNonWSSchemes(t *testing.T) {
	p := NewProxy(NewAllowlist(), 0)
	defer p.Close()

	for _, u := range []string{
		"http://192.168.1.10/ws",
		"ws://169.254.169.254/latest",
		"ws://127.0.0.1:8080/ws",
	} {
		req, _ := json.Marshal(ProxyRequest{
			Type:      "ws_open",
			RequestID: "ws-blocked",
			URL:       u,
		})
		var got ProxyResponse
		_ = p.HandleRequestStream(context.Background(), req, func(b []byte) error {
			return json.Unmarshal(b, &got)
		})
		if got.Success || got.Type != "ws_error" {
			t.Fatalf("URL %q deveria retornar ws_error, got %+v", u, got)
		}
	}
}

func TestWSProxy_CloseCleansUpActiveSockets(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	p := NewProxy(allowLoopbackForTestServer(t, srv.URL), 0)

	opened := make(chan struct{}, 1)
	openReq, _ := json.Marshal(ProxyRequest{
		Type:      "ws_open",
		RequestID: "ws-leak-check",
		URL:       wsURL,
	})
	err := p.HandleRequestStream(context.Background(), openReq, func(b []byte) error {
		var r ProxyResponse
		if json.Unmarshal(b, &r) == nil && r.Type == "ws_opened" {
			select {
			case opened <- struct{}{}:
			default:
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ws_open: %v", err)
	}
	<-opened

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	p.wsMu.Lock()
	remaining := len(p.wsConns)
	p.wsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("wsConns deveria estar vazio apos Close, got %d", remaining)
	}
}
