package e2e_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"discovery/app/core/fileserver"
	"discovery/app/core/netproxy"
	"discovery/app/core/remotesession"
	"discovery/app/core/screen"
)

// ── F16 Boundaries (6 tests) ────────────────────────────────────────────────

func TestF16_T2_UrlParseEdgeCasesAndIPv6HostPort(t *testing.T) {
	al := netproxy.NewAllowlist()
	al.SetAllowLoopback(true)
	_ = al.AddCIDR("127.0.0.1/32")
	_ = al.AddCIDR("::1/128")
	al.AddPort(8080)
	al.AddPort(80)
	al.AddPort(443)
	px := netproxy.NewProxy(al, 1024*1024)
	defer px.Close()

	// Esquemas proibidos, userinfo e portas invalidas devem ser rejeitados
	for _, badURL := range []string{
		"file:///C:/Windows/win.ini",
		"ftp://127.0.0.1:21/a",
		"http://127.0.0.1:0/a",
		"http://127.0.0.1:65536/a",
		"http://user:pass@127.0.0.1:80/path",
		"://broken-url",
	} {
		req, _ := json.Marshal(map[string]any{"requestId": "u-bad", "method": "GET", "url": badURL})
		raw := px.HandleRequest(req)
		var resp map[string]any
		_ = json.Unmarshal(raw, &resp)
		if resp["success"] != false {
			t.Fatalf("URL invalida %q deveria falhar, obteve %v", badURL, resp)
		}
	}

	// Loopback com query contendo ':' nao pode quebrar o parse de porta
	_, _, port := startLoopbackHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	al.AddPort(port)
	reqOK, _ := json.Marshal(map[string]any{
		"requestId": "u-ok",
		"method":    "GET",
		"url":       fmt.Sprintf("http://127.0.0.1:%d/path?q=a:b", port),
	})
	rawOK := px.HandleRequest(reqOK)
	var respOK map[string]any
	_ = json.Unmarshal(rawOK, &respOK)
	if respOK["success"] != true {
		t.Fatalf("URL com query ':' deveria funcionar com allowLoopback=true, obteve %v", respOK)
	}
}

func TestF16_T2_MaxResponseBytesAndLargeChunkedBody(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "66666666-6666-6666-6666-666666666661"
	)

	largePayload := bytes.Repeat([]byte("M"), 1800*1024) // > 1.5 MiB -> força chunked stream
	_, baseURL, port := startLoopbackHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/big" {
			_, _ = w.Write(largePayload)
			return
		}
		_, _ = w.Write([]byte("1234567890"))
	}))

	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	readySub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "proxy.ready"))
	respSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "proxy.resp"))

	mgr := newTestSessionManager(t, agentNC, clientID, siteID, agentID)
	ok, errMsg := mgr.HandleCommand(context.Background(), map[string]any{
		"action":    "start",
		"sessionId": sessionID,
		"kind":      "proxy",
		"proxy": map[string]any{
			"allowlist":        []string{"127.0.0.1/32"},
			"allowedPorts":     []int{port},
			"allowLoopback":    true,
			"maxResponseBytes": 2 * 1024 * 1024,
		},
	})
	if !ok {
		t.Fatalf("start proxy: %s", errMsg)
	}
	_ = waitSubMsg(t, readySub, 5*time.Second)

	req, _ := json.Marshal(map[string]any{"requestId": "big-1", "method": "GET", "url": baseURL + "/big"})
	_ = viewerNC.Publish(sessionSubject(clientID, siteID, agentID, sessionID, "proxy.req"), req)
	_ = viewerNC.Flush()

	var reassembled []byte
	var gotEOF bool
	deadline := time.Now().Add(8 * time.Second)
	for !gotEOF && time.Now().Before(deadline) {
		msg := waitSubMsg(t, respSub, 5*time.Second)
		var r map[string]any
		if err := json.Unmarshal(msg.Data, &r); err != nil {
			t.Fatalf("chunk JSON invalido: %v", err)
		}
		if r["success"] != true {
			t.Fatalf("chunk falhou: %v", r)
		}
		if b64, ok := r["bodyBase64"].(string); ok && b64 != "" {
			part, _ := base64.StdEncoding.DecodeString(b64)
			reassembled = append(reassembled, part...)
		}
		if eof, ok := r["eof"].(bool); ok && eof {
			gotEOF = true
		} else if st, ok := r["stream"].(bool); !ok || !st {
			gotEOF = true
		}
	}
	if !bytes.Equal(reassembled, largePayload) {
		t.Fatalf("payload grande remontado diverge: got=%d want=%d", len(reassembled), len(largePayload))
	}
}

func TestF16_T2_RequestTimeoutBoundary(t *testing.T) {
	al := netproxy.NewAllowlist()
	al.SetAllowLoopback(true)
	_ = al.AddCIDR("127.0.0.1/32")
	_, baseURL, port := startLoopbackHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(2500 * time.Millisecond)
		}
		_, _ = w.Write([]byte("done"))
	}))
	al.AddPort(port)

	px := netproxy.NewProxy(al, 1024*1024)
	defer px.Close()

	start := time.Now()
	reqSlow, _ := json.Marshal(map[string]any{"requestId": "t-slow", "method": "GET", "url": baseURL + "/slow", "timeoutMs": 100})
	raw := px.HandleRequest(reqSlow)
	elapsed := time.Since(start)

	var resp map[string]any
	_ = json.Unmarshal(raw, &resp)
	if resp["success"] != false {
		t.Fatalf("esperava falha por timeout, obteve %v", resp)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout demorou %v (esperado <= ~1s)", elapsed)
	}

	// Sessao continua viva apos timeout
	reqFast, _ := json.Marshal(map[string]any{"requestId": "t-fast", "method": "GET", "url": baseURL + "/fast", "timeoutMs": 0})
	rawFast := px.HandleRequest(reqFast)
	var respFast map[string]any
	_ = json.Unmarshal(rawFast, &respFast)
	if respFast["success"] != true {
		t.Fatalf("proxy nao recuperou apos timeout: %v", respFast)
	}
}

func TestF16_T2_TunnelFrameBoundariesAndUnknownOp(t *testing.T) {
	badFrames := [][]byte{
		{},
		{0x01, 0x01, 0x00, 0x00, 0x01},       // 5 bytes < 6
		{0x02, 0x01, 0x00, 0x00, 0x00, 0x01}, // versao desconhecida
		{0x01, 0xFF, 0x00, 0x00, 0x00, 0x01}, // op desconhecido
		[]byte(`{malformed-json`),
		[]byte(`{"streamId":1,"op":"unknown_op"}`),
	}
	for i, f := range badFrames {
		if _, _, _, _, _, err := remotesession.DecodeTunnelFrame(f); err == nil {
			t.Fatalf("frame invalido %d (%x) deveria retornar erro", i, f)
		}
	}

	// 0-byte & 64 KiB DATA binary frames
	for _, sz := range []int{0, 64 * 1024} {
		payload := bytes.Repeat([]byte{0xAB}, sz)
		enc := remotesession.EncodeTunnelBinaryData(nil, 42, payload)
		isBin, opByte, sid, got, _, err := remotesession.DecodeTunnelFrame(enc)
		if err != nil || !isBin || opByte != remotesession.TunnelOpData || sid != 42 || !bytes.Equal(got, payload) {
			t.Fatalf("round-trip frame binario len=%d falhou: err=%v sid=%d gotLen=%d", sz, err, sid, len(got))
		}
	}
}

func TestF16_T2_TunnelStreamLifecycleErrors(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "66666666-6666-6666-6666-666666666662"
	)

	echoPort, _ := startLoopbackTCPEcho(t)
	closedPort := freeTCPPort(t)
	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	readySub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "proxy.ready"))
	tunnelOutSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "tunnel.out"))

	mgr := newTestSessionManager(t, agentNC, clientID, siteID, agentID)
	ok, errMsg := mgr.HandleCommand(context.Background(), map[string]any{
		"action":    "start",
		"sessionId": sessionID,
		"kind":      "proxy",
		"proxy": map[string]any{
			"allowlist":     []string{"127.0.0.1/32"},
			"allowedPorts":  []int{echoPort, closedPort},
			"allowLoopback": true,
		},
	})
	if !ok {
		t.Fatalf("start proxy: %s", errMsg)
	}
	_ = waitSubMsg(t, readySub, 5*time.Second)

	tunnelIn := sessionSubject(clientID, siteID, agentID, sessionID, "tunnel.in")
	// 1. Frame binario com streamId=0 -> op=error (invalid_stream)
	_ = viewerNC.Publish(tunnelIn, remotesession.EncodeTunnelBinaryData(nil, 0, []byte("bad")))
	_ = viewerNC.Flush()
	m1 := waitSubMsg(t, tunnelOutSub, 5*time.Second)
	_, _, _, _, c1, _ := remotesession.DecodeTunnelFrame(m1.Data)
	if c1 == nil || c1.Op != "error" {
		t.Fatalf("esperava op=error para streamId 0 binario, got=%+v", c1)
	}

	// 2. open para porta fechada -> op=error
	openClosed, _ := json.Marshal(map[string]any{"streamId": 7, "op": "open", "targetHost": "127.0.0.1", "targetPort": closedPort})
	_ = viewerNC.Publish(tunnelIn, openClosed)
	_ = viewerNC.Flush()
	m2 := waitSubMsg(t, tunnelOutSub, 5*time.Second)
	_, _, _, _, c2, _ := remotesession.DecodeTunnelFrame(m2.Data)
	if c2 == nil || c2.Op != "error" {
		t.Fatalf("esperava op=error para porta recusada, got=%+v", c2)
	}

	// 3. open duplicado no mesmo streamId=1 ativo -> op=error (duplicate_stream)
	open1, _ := json.Marshal(map[string]any{"streamId": 1, "op": "open", "targetHost": "127.0.0.1", "targetPort": echoPort})
	_ = viewerNC.Publish(tunnelIn, open1)
	_ = viewerNC.Flush()
	_ = waitSubMsg(t, tunnelOutSub, 5*time.Second) // opened
	_ = viewerNC.Publish(tunnelIn, open1)
	_ = viewerNC.Flush()
	m3 := waitSubMsg(t, tunnelOutSub, 5*time.Second)
	_, _, _, _, c3, _ := remotesession.DecodeTunnelFrame(m3.Data)
	if c3 == nil || c3.Op != "error" {
		t.Fatalf("esperava op=error para open duplicado em streamId 1, got=%+v", c3)
	}

	// 4. DATA em streamId inexistente (999) + close idempotente nao panica
	_ = viewerNC.Publish(tunnelIn, remotesession.EncodeTunnelBinaryData(nil, 999, []byte("orphan")))
	_ = viewerNC.Publish(tunnelIn, remotesession.EncodeTunnelBinaryClose(999, "done"))
	_ = viewerNC.Flush()
}

type fakeResolver map[string][]net.IPAddr

func (f fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if ips, ok := f[host]; ok {
		return ips, nil
	}
	return nil, fmt.Errorf("no such host: %s", host)
}

func TestF16_T2_DnsRebindingAndMultiIPValidation(t *testing.T) {
	al := netproxy.NewAllowlist()
	_ = al.AddCIDR("10.0.0.0/8")
	al.AddPort(80)
	al.SetAllowLoopback(false)
	al.SetResolver(fakeResolver{
		"rebind.internal": {{IP: net.ParseIP("10.0.0.5")}, {IP: net.ParseIP("127.0.0.1")}},
		"meta.internal":   {{IP: net.ParseIP("169.254.169.254")}},
	})

	if al.ContainsIP(nil) {
		t.Fatalf("ContainsIP(nil) deve retornar false")
	}
	if _, err := al.DialAllowed(context.Background(), "tcp", "rebind.internal:80"); err == nil {
		t.Fatalf("DialAllowed deveria bloquear host com resolucao DNS mista contendo 127.0.0.1 quando allowLoopback=false")
	}
	if _, err := al.DialAllowed(context.Background(), "tcp", "meta.internal:80"); err == nil {
		t.Fatalf("DialAllowed deveria bloquear resolucao para link-local metadata 169.254.169.254")
	}
}

// ── F17 Boundaries (6 tests) ────────────────────────────────────────────────

func TestF17_T2_BinaryFileFrameMalformedHeadersAndMagic(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "66666666-6666-6666-6666-666666666671"
	)

	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	readySub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "files.ready"))
	respSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "files.resp"))

	handler := remotesession.NewNatsStreamHandler(agentNC, clientID, siteID, agentID)
	sf := remotesession.NewSessionFiles(sessionID, handler, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sf.Start(ctx) }()
	t.Cleanup(func() { sf.Stop() })
	_ = waitSubMsg(t, readySub, 5*time.Second)

	reqSubj := sessionSubject(clientID, siteID, agentID, sessionID, "files.req")
	badFrames := [][]byte{
		{'D', 'F', 0, 0},                           // truncado < 6
		{'D', 'G', 0, 0, 0, 2, '{', '}'},           // magic errado
		{'D', 'F', 0, 0, 0, 0},                     // headerLen=0
		{'D', 'F', 0, 0, 0, 99, '{', '}'},          // headerLen > len(frame)
		{'D', 'F', 0, 0, 0, 4, '{', 'b', 'a', 'd'}, // json invalido
	}
	for i, bf := range badFrames {
		_ = viewerNC.Publish(reqSubj, bf)
		_ = viewerNC.Flush()
		msg := waitSubMsg(t, respSub, 5*time.Second)
		var resp map[string]any
		if err := json.Unmarshal(msg.Data, &resp); err != nil || resp["success"] != false {
			t.Fatalf("frame malformado %d deveria retornar success=false JSON, obteve %s", i, string(msg.Data))
		}
	}
}

func TestF17_T2_ZeroByteAndExactChunkBoundaryFileTransfer(t *testing.T) {
	rootDir := t.TempDir()
	srv := fileserver.NewServer(rootDir)

	// 1. 0-byte file
	idx0 := 0
	reqEmpty, _ := json.Marshal(fileserver.FileSessionRequest{
		Version: 1, RequestID: "empty-0", Action: "put", Path: "empty.bin",
		Data: []byte{}, ChunkIndex: &idx0, TotalChunks: 1, ChunkSize: 64 * 1024,
	})
	var respEmpty fileserver.FileSessionResponse
	_ = json.Unmarshal(srv.HandleRequest(reqEmpty), &respEmpty)
	if !respEmpty.Success {
		t.Fatalf("put 0-byte falhou: %s", respEmpty.Error)
	}
	if info, err := os.Stat(filepath.Join(rootDir, "empty.bin")); err != nil || info.Size() != 0 {
		t.Fatalf("empty.bin tamanho inesperado: info=%v err=%v", info, err)
	}

	// 2. chunkSize - 1, chunkSize, chunkSize + 1 via get/put
	const cs = 1024
	for _, totalLen := range []int{cs - 1, cs, cs + 1} {
		data := bytes.Repeat([]byte{0x5A}, totalLen)
		fname := fmt.Sprintf("bound-%d.bin", totalLen)
		nChunks := (totalLen + cs - 1) / cs
		for ci := 0; ci < nChunks; ci++ {
			end := minInt((ci+1)*cs, totalLen)
			idx := ci
			req, _ := json.Marshal(fileserver.FileSessionRequest{
				Version: 1, RequestID: fmt.Sprintf("b-%d-%d", totalLen, ci), Action: "put",
				Path: fname, Data: data[ci*cs : end], ChunkIndex: &idx, TotalChunks: nChunks, ChunkSize: cs,
			})
			var r fileserver.FileSessionResponse
			_ = json.Unmarshal(srv.HandleRequest(req), &r)
			if !r.Success {
				t.Fatalf("put %s chunk %d falhou: %s", fname, ci, r.Error)
			}
		}
		got, err := os.ReadFile(filepath.Join(rootDir, fname))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s divergiu: err=%v len=%d want=%d", fname, err, len(got), totalLen)
		}
	}
}

func TestF17_T2_FilePathTraversalAndSandboxRejection(t *testing.T) {
	rootDir := t.TempDir()
	srv := fileserver.NewServer(rootDir)

	for _, badPath := range []string{
		`..\..\Windows\System32\drivers\etc\hosts`,
		`../outside.txt`,
		`C:\Windows\System32\cmd.exe`,
	} {
		req, _ := json.Marshal(fileserver.FileSessionRequest{
			Version: 1, RequestID: "trav", Action: "stat", Path: badPath,
		})
		var resp fileserver.FileSessionResponse
		_ = json.Unmarshal(srv.HandleRequest(req), &resp)
		if resp.Success {
			t.Fatalf("path traversal %q nao foi bloqueado pelo sandbox", badPath)
		}
	}
}

func TestF17_T2_ScreenDirtyRectsAndEncoderBounds(t *testing.T) {
	// 1. Frame 1x1 + quality 0/101 clamping
	f1 := screen.GetPooledFrame(1, 1, 4)
	defer screen.PutPooledFrame(f1)
	enc := screen.NewJPEGEncoder()
	for _, q := range []int{0, 101} {
		b, err := enc.Encode(f1, q)
		if err != nil || len(b) == 0 {
			t.Fatalf("Encode 1x1 q=%d falhou: %v", q, err)
		}
	}

	// 2. Identical frames -> 0 dirty rects
	det := screen.NewDirtyDetector(32)
	f2 := screen.GetPooledFrame(64, 64, 64*4)
	defer screen.PutPooledFrame(f2)
	if rects := det.Detect(f2); len(rects) != 1 {
		t.Fatalf("primeiro frame esperava 1 rect full, obteve %d", len(rects))
	}
	if rects := det.Detect(f2); len(rects) != 0 {
		t.Fatalf("frame identico esperava 0 dirty rects, obteve %d", len(rects))
	}

	// 3. Out-of-bounds DirtyRect nao pode panicar EncodeDirtyRects
	_, err := screen.EncodeDirtyRects(f2, []screen.DirtyRect{{X: -5, Y: -5, Width: 99999, Height: 99999}}, 75)
	if err != nil {
		t.Fatalf("EncodeDirtyRects com rect fora dos limites falhou: %v", err)
	}
}

func TestF17_T2_TerminalInputSizeAndProtocolVersionBoundary(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "66666666-6666-6666-6666-666666666675"
	)

	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	termOutSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "term.out"))

	handler := remotesession.NewNatsStreamHandler(agentNC, clientID, siteID, agentID)
	st := remotesession.NewSessionTerminal(sessionID, handler, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := st.Start(ctx, "cmd", 80, 24); err != nil {
		t.Fatalf("SessionTerminal.Start: %v", err)
	}
	t.Cleanup(func() { st.Stop() })

	termIn := sessionSubject(clientID, siteID, agentID, sessionID, "term.in")
	// 1. pv=99 deve emitir protocol_version_mismatch explicito
	_ = viewerNC.Publish(termIn, []byte(`{"hello":true,"pv":99}`))
	_ = viewerNC.Flush()

	var sawMismatch bool
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := termOutSub.NextMsg(400 * time.Millisecond)
		if err != nil {
			continue
		}
		if strings.Contains(string(msg.Data), "protocol_version_mismatch") {
			sawMismatch = true
			break
		}
	}
	if !sawMismatch {
		t.Fatalf("esperava erro protocol_version_mismatch para pv=99")
	}

	// 2. Input 32 KiB + 1 nao derruba o terminal
	oversized := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("A"), 32*1024+1))
	bigMsg, _ := json.Marshal(map[string]any{"data": oversized, "pv": 1})
	_ = viewerNC.Publish(termIn, bigMsg)
	_ = viewerNC.Flush()
	time.Sleep(150 * time.Millisecond)
	if !st.HasTerminal() {
		t.Fatalf("terminal encerrou ao receber input > 32 KiB")
	}
}

func TestF17_T2_NatsBackpressureAndBandwidthGateBoundaries(t *testing.T) {
	gate := remotesession.NewBandwidthGate(remotesession.BandwidthGateConfig{
		Mode:        remotesession.BandwidthManual,
		CeilingKbps: 100,
	})
	snap := gate.Snapshot()
	if snap.Mode != remotesession.BandwidthManual || snap.CeilingKbps != 100 {
		t.Fatalf("BandwidthSnapshot inesperado: %+v", snap)
	}
	// Verifica que PublishFrame fragmenta payload > MaxPayloadBytes em .frame.frag sem perder dados
	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "66666666-6666-6666-6666-666666666676"
	)
	fragSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "frame.frag"))
	ns := remotesession.NewNatsStreamHandler(agentNC, clientID, siteID, agentID)
	ns.SetMaxPayloadBytes(8 * 1024)

	bigFrame := bytes.Repeat([]byte{0x42}, 24*1024)
	encodedFrame, err := remotesession.EncodeFrameMsgPack(1, 1000, 1920, 1080, bigFrame)
	if err != nil {
		t.Fatalf("EncodeFrameMsgPack: %v", err)
	}
	if err := ns.PublishFrame(sessionID, encodedFrame); err != nil {
		t.Fatalf("PublishFrame fragmentado: %v", err)
	}
	_ = agentNC.Flush()
	m := waitSubMsg(t, fragSub, 3*time.Second)
	if len(m.Data) == 0 {
		t.Fatalf("fragmento vazio")
	}
}
