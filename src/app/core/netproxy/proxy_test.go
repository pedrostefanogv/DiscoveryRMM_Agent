package netproxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

func allowLoopbackForTestServer(t *testing.T, serverURL string) *Allowlist {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	a := NewAllowlist()
	a.SetAllowLoopback(true)
	if err := a.AddCIDR("127.0.0.1/32"); err != nil {
		t.Fatalf("AddCIDR: %v", err)
	}
	a.AddPort(port)
	return a
}

func TestProxy_RequestIdAndBinaryBodyBase64RoundTrip(t *testing.T) {
	reqBytes := []byte{0x00, 0xff, 0xfe, 0x80, 0x10, 0x20, 0x7f, 0xaa}
	respBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ := io.ReadAll(r.Body)
		if !bytes.Equal(gotBody, reqBytes) {
			t.Errorf("request body corrompido: got %x want %x", gotBody, reqBytes)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Echo-Method", r.Method)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(respBytes)
	}))
	defer srv.Close()

	p := NewProxy(allowLoopbackForTestServer(t, srv.URL), 0)
	defer p.Close()

	reqJSON, _ := json.Marshal(ProxyRequest{
		RequestID:  "req-bin-1",
		Method:     "POST",
		URL:        srv.URL + "/upload",
		BodyBase64: base64.StdEncoding.EncodeToString(reqBytes),
	})
	rawResp := p.HandleRequest(reqJSON)

	var resp ProxyResponse
	if err := json.Unmarshal(rawResp, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("esperava sucesso, erro: %s", resp.Error)
	}
	if resp.RequestID != "req-bin-1" {
		t.Fatalf("requestId = %q, want req-bin-1", resp.RequestID)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if resp.ContentType != "application/octet-stream" {
		t.Fatalf("contentType = %q", resp.ContentType)
	}
	if resp.FinalURL != srv.URL+"/upload" {
		t.Fatalf("finalUrl = %q", resp.FinalURL)
	}
	decoded, err := base64.StdEncoding.DecodeString(resp.BodyBase64)
	if err != nil {
		t.Fatalf("decode bodyBase64: %v", err)
	}
	if !bytes.Equal(decoded, respBytes) {
		t.Fatalf("response binary mismatch: got %x want %x", decoded, respBytes)
	}

	// Erro também deve propagar requestId:
	errReq, _ := json.Marshal(ProxyRequest{
		RequestID: "req-err-1",
		Method:    "GET",
		URL:       "http://169.254.169.254/latest/meta-data/",
	})
	var errResp ProxyResponse
	_ = json.Unmarshal(p.HandleRequest(errReq), &errResp)
	if errResp.Success || errResp.RequestID != "req-err-1" {
		t.Fatalf("erro deveria propagar requestId=req-err-1, got %+v", errResp)
	}
}

func TestProxy_BlocksSSRFRedirects(t *testing.T) {
	var targetURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetURL, http.StatusFound)
	}))
	defer srv.Close()

	p := NewProxy(allowLoopbackForTestServer(t, srv.URL), 0)
	defer p.Close()

	for _, redir := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"file:///etc/passwd",
		"http://127.0.0.1:1/",
	} {
		targetURL = redir
		reqJSON, _ := json.Marshal(ProxyRequest{
			RequestID: "req-redir",
			Method:    "GET",
			URL:       srv.URL + "/start",
		})
		var resp ProxyResponse
		_ = json.Unmarshal(p.HandleRequest(reqJSON), &resp)
		if resp.Success {
			t.Fatalf("redirect para %q deveria ter sido bloqueado", redir)
		}
	}

	// > 5 redirects
	hopSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer hopSrv.Close()
	p2 := NewProxy(allowLoopbackForTestServer(t, hopSrv.URL), 0)
	defer p2.Close()

	reqJSON, _ := json.Marshal(ProxyRequest{RequestID: "req-hops", Method: "GET", URL: hopSrv.URL + "/0"})
	var hopResp ProxyResponse
	_ = json.Unmarshal(p2.HandleRequest(reqJSON), &hopResp)
	if hopResp.Success {
		t.Fatal("mais de 5 redirects deveria falhar")
	}
}

func TestProxy_DNSRebindingBlockedAtDial(t *testing.T) {
	a := NewAllowlist()
	_ = a.AddCIDR("192.168.1.0/24")
	a.AddPort(80)
	a.SetResolver(&fakeResolver{
		fn: func(_ int32, _ string) ([]net.IPAddr, error) {
			return []net.IPAddr{
				{IP: net.ParseIP("192.168.1.10")},
				{IP: net.ParseIP("127.0.0.1")},
			}, nil
		},
	})
	p := NewProxy(a, 0)
	defer p.Close()

	reqJSON, _ := json.Marshal(ProxyRequest{
		RequestID: "req-rebind",
		Method:    "GET",
		URL:       "http://rebind.lan:80/",
	})
	var resp ProxyResponse
	_ = json.Unmarshal(p.HandleRequest(reqJSON), &resp)
	if resp.Success {
		t.Fatal("DNS rebinding misturando IP interno e loopback deveria ser bloqueado")
	}
}

func TestProxy_ChunkedStreamingOver1_5MiB(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB, 0xCD, 0xEF, 0x01, 0x00, 0x7F, 0x80}, 330000) // ~2.31 MB

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	p := NewProxyWithConfig(allowLoopbackForTestServer(t, srv.URL), ProxyConfig{
		MaxResponseBytes:    10 * 1024 * 1024,
		NatsMaxPayloadBytes: 2 * 1024 * 1024,
	})
	defer p.Close()

	reqJSON, _ := json.Marshal(ProxyRequest{
		RequestID: "req-stream-1",
		Method:    "GET",
		URL:       srv.URL + "/large",
	})

	var frames [][]byte
	if err := p.HandleRequestStream(context.Background(), reqJSON, func(b []byte) error {
		cp := make([]byte, len(b))
		copy(cp, b)
		frames = append(frames, cp)
		return nil
	}); err != nil {
		t.Fatalf("HandleRequestStream: %v", err)
	}

	if len(frames) < 2 {
		t.Fatalf("esperava multiplos chunks para payload de %d bytes, got %d", len(payload), len(frames))
	}

	var reassembled bytes.Buffer
	for i, raw := range frames {
		if len(raw) >= 1536*1024 {
			t.Fatalf("frame %d excedeu 1.5 MiB (%d bytes)", i, len(raw))
		}
		var chunk ProxyResponse
		if err := json.Unmarshal(raw, &chunk); err != nil {
			t.Fatalf("unmarshal frame %d: %v", i, err)
		}
		if !chunk.Success || !chunk.Stream {
			t.Fatalf("frame %d inesperado: %+v", i, chunk)
		}
		if chunk.RequestID != "req-stream-1" {
			t.Fatalf("frame %d requestId=%q", i, chunk.RequestID)
		}
		if chunk.ChunkIndex != i {
			t.Fatalf("frame %d chunkIndex=%d", i, chunk.ChunkIndex)
		}
		isLast := i == len(frames)-1
		if chunk.EOF != isLast {
			t.Fatalf("frame %d eof=%v want %v", i, chunk.EOF, isLast)
		}
		if chunk.BodyBase64 != "" {
			b, err := base64.StdEncoding.DecodeString(chunk.BodyBase64)
			if err != nil {
				t.Fatalf("decode frame %d: %v", i, err)
			}
			reassembled.Write(b)
		}
	}

	if !bytes.Equal(reassembled.Bytes(), payload) {
		t.Fatalf("payload remontado diverge: got %d bytes want %d bytes", reassembled.Len(), len(payload))
	}
}

func TestProxy_MaxResponseBytesEnforced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chunked" {
			w.Header().Set("X-Chunked", "1")
			flusher, _ := w.(http.Flusher)
			for i := 0; i < 10; i++ {
				_, _ = w.Write(bytes.Repeat([]byte("A"), 2048))
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}
		_, _ = w.Write(bytes.Repeat([]byte("B"), 16384))
	}))
	defer srv.Close()

	p := NewProxyWithConfig(allowLoopbackForTestServer(t, srv.URL), ProxyConfig{
		MaxResponseBytes: 4096,
	})
	defer p.Close()

	for _, path := range []string{"/known", "/chunked"} {
		reqJSON, _ := json.Marshal(ProxyRequest{
			RequestID: "req-max-" + path,
			Method:    "GET",
			URL:       srv.URL + path,
		})
		var resp ProxyResponse
		_ = json.Unmarshal(p.HandleRequest(reqJSON), &resp)
		if resp.Success {
			t.Fatalf("%s deveria falhar ao exceder maxResponseBytes", path)
		}
	}
}

func TestProxy_PerRequestTimeoutMs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
			_, _ = w.Write([]byte("late"))
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	p := NewProxy(allowLoopbackForTestServer(t, srv.URL), 0)
	defer p.Close()

	reqJSON, _ := json.Marshal(ProxyRequest{
		RequestID: "req-timeout",
		Method:    "GET",
		URL:       srv.URL + "/slow",
		TimeoutMs: 1000, // clamp min 1s
	})

	start := time.Now()
	var resp ProxyResponse
	_ = json.Unmarshal(p.HandleRequest(reqJSON), &resp)
	elapsed := time.Since(start)
	if resp.Success {
		t.Fatal("esperava erro de timeout")
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("timeout por requisicao demorou demais: %v", elapsed)
	}
}

func TestProxy_ConcurrentRequestsCorrelatedByRequestId(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		_, _ = w.Write([]byte("echo:" + id))
	}))
	defer srv.Close()

	p := NewProxy(allowLoopbackForTestServer(t, srv.URL), 0)
	defer p.Close()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := "conc-" + strconv.Itoa(idx)
			reqJSON, _ := json.Marshal(ProxyRequest{
				RequestID: id,
				Method:    "GET",
				URL:       srv.URL + "/?id=" + id,
			})
			var resp ProxyResponse
			if err := json.Unmarshal(p.HandleRequest(reqJSON), &resp); err != nil {
				t.Errorf("unmarshal: %v", err)
				return
			}
			if !resp.Success || resp.RequestID != id {
				t.Errorf("correlacao incorreta: %+v", resp)
				return
			}
			got, _ := base64.StdEncoding.DecodeString(resp.BodyBase64)
			if string(got) != "echo:"+id {
				t.Errorf("body = %q, want %q", string(got), "echo:"+id)
			}
		}(i)
	}
	wg.Wait()
}
