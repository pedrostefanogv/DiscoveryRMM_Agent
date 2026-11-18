package e2e_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"

	"discovery/app/core/remotesession"
)

func TestF16_T1_ProxySessionHeadlessLifecycleAndReady(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "44444444-4444-4444-4444-444444444444"
	)

	_, baseURL, port := startLoopbackHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hello" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("hello-lan-proxy"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
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
			"allowlist":     []string{"127.0.0.1/32"},
			"allowedPorts":  []int{port},
			"allowLoopback": true,
		},
	})
	if !ok {
		t.Fatalf("HandleCommand(start proxy) falhou: %s", errMsg)
	}

	readyMsg := waitSubMsg(t, readySub, 5*time.Second)
	var readyPayload map[string]any
	if err := json.Unmarshal(readyMsg.Data, &readyPayload); err != nil {
		t.Fatalf("proxy.ready JSON invalido: %v", err)
	}
	if readyPayload["status"] != "ready" {
		t.Fatalf("proxy.ready status inesperado: %v", readyPayload)
	}

	reqBytes, _ := json.Marshal(map[string]any{
		"requestId": "req-1",
		"method":    "GET",
		"url":       baseURL + "/hello",
	})
	if err := viewerNC.Publish(sessionSubject(clientID, siteID, agentID, sessionID, "proxy.req"), reqBytes); err != nil {
		t.Fatalf("Publish proxy.req: %v", err)
	}
	_ = viewerNC.Flush()

	respMsg := waitSubMsg(t, respSub, 5*time.Second)
	var resp map[string]any
	if err := json.Unmarshal(respMsg.Data, &resp); err != nil {
		t.Fatalf("proxy.resp JSON invalido: %v", err)
	}
	if resp["requestId"] != "req-1" || resp["success"] != true {
		t.Fatalf("proxy.resp falha: %v", resp)
	}
	if int(resp["statusCode"].(float64)) != 200 {
		t.Fatalf("statusCode esperado 200, obteve %v", resp["statusCode"])
	}
	rawBody, err := base64.StdEncoding.DecodeString(resp["bodyBase64"].(string))
	if err != nil || string(rawBody) != "hello-lan-proxy" {
		t.Fatalf("bodyBase64 decodificado inesperado (%v): %q", err, string(rawBody))
	}

	ok, errMsg = mgr.HandleCommand(context.Background(), map[string]any{
		"action":    "stop",
		"sessionId": sessionID,
	})
	if !ok {
		t.Fatalf("HandleCommand(stop proxy) falhou: %s", errMsg)
	}
}

func TestF16_T1_ProxyPOSTBinaryBodyBase64AndFinalURL(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "44444444-4444-4444-4444-444444444445"
	)

	binPayload := []byte{0x00, 0xFF, 0x42, 0x13, 0x37}
	_, baseURL, port := startLoopbackHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/end", http.StatusMovedPermanently)
		case "/end":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(binPayload)
		case "/echo":
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("X-Seen-Header", r.Header.Get("X-Test"))
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(body)
		}
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
			"allowlist":     []string{"127.0.0.1/32"},
			"allowedPorts":  []int{port},
			"allowLoopback": true,
		},
	})
	if !ok {
		t.Fatalf("start proxy: %s", errMsg)
	}
	_ = waitSubMsg(t, readySub, 5*time.Second)

	// 1. Redirect -> finalUrl
	req1, _ := json.Marshal(map[string]any{
		"requestId": "redir-1",
		"method":    "GET",
		"url":       baseURL + "/start",
	})
	_ = viewerNC.Publish(sessionSubject(clientID, siteID, agentID, sessionID, "proxy.req"), req1)
	msg1 := waitSubMsg(t, respSub, 5*time.Second)
	var r1 map[string]any
	_ = json.Unmarshal(msg1.Data, &r1)
	if r1["success"] != true || !strings.HasSuffix(fmt.Sprint(r1["finalUrl"]), "/end") {
		t.Fatalf("finalUrl esperado terminando em /end, obteve %v", r1)
	}
	if !strings.Contains(fmt.Sprint(r1["contentType"]), "application/octet-stream") {
		t.Fatalf("contentType inesperado: %v", r1["contentType"])
	}

	// 2. POST binary bodyBase64
	req2, _ := json.Marshal(map[string]any{
		"requestId":  "post-bin-2",
		"method":     "POST",
		"url":        baseURL + "/echo",
		"headers":    map[string]string{"X-Test": "val-1"},
		"bodyBase64": base64.StdEncoding.EncodeToString(binPayload),
	})
	_ = viewerNC.Publish(sessionSubject(clientID, siteID, agentID, sessionID, "proxy.req"), req2)
	msg2 := waitSubMsg(t, respSub, 5*time.Second)
	var r2 map[string]any
	_ = json.Unmarshal(msg2.Data, &r2)
	if r2["requestId"] != "post-bin-2" || r2["success"] != true {
		t.Fatalf("POST binary falhou: %v", r2)
	}
	gotBin, _ := base64.StdEncoding.DecodeString(fmt.Sprint(r2["bodyBase64"]))
	if !bytes.Equal(gotBin, binPayload) {
		t.Fatalf("payload binario divergiu: got=%x want=%x", gotBin, binPayload)
	}
}

func TestF16_T1_ProxyDefaultSSRFBlocksLoopbackAndMetadata(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "44444444-4444-4444-4444-444444444446"
	)

	var secretHits atomic.Int32
	_, baseURL, port := startLoopbackHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secretHits.Add(1)
		_, _ = w.Write([]byte("secret"))
	}))

	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	readySub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "proxy.ready"))
	respSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "proxy.resp"))

	mgr := newTestSessionManager(t, agentNC, clientID, siteID, agentID)
	// Politica ampla 0.0.0.0/0, MAS allowLoopback=false (default) -> loopback & metadata DEVEM ser bloqueados
	ok, errMsg := mgr.HandleCommand(context.Background(), map[string]any{
		"action":    "start",
		"sessionId": sessionID,
		"kind":      "proxy",
		"proxy": map[string]any{
			"allowlist":    []string{"0.0.0.0/0", "::/0"},
			"allowedPorts": []int{80, port},
		},
	})
	if !ok {
		t.Fatalf("start proxy: %s", errMsg)
	}
	_ = waitSubMsg(t, readySub, 5*time.Second)

	targets := []string{
		baseURL + "/secret",
		"http://[::1]:80/",
		"http://169.254.169.254/latest/meta-data/",
		"http://localhost:" + fmt.Sprint(port) + "/secret",
	}
	for i, target := range targets {
		req, _ := json.Marshal(map[string]any{
			"requestId": fmt.Sprintf("ssrf-%d", i),
			"method":    "GET",
			"url":       target,
		})
		_ = viewerNC.Publish(sessionSubject(clientID, siteID, agentID, sessionID, "proxy.req"), req)
		msg := waitSubMsg(t, respSub, 5*time.Second)
		var resp map[string]any
		_ = json.Unmarshal(msg.Data, &resp)
		if resp["success"] != false {
			t.Fatalf("F16 SSRF violacao: target %s nao foi bloqueado! resp=%v", target, resp)
		}
	}
	if secretHits.Load() != 0 {
		t.Fatalf("endpoint loopback recebeu %d hits mesmo com allowLoopback=false", secretHits.Load())
	}
}

func TestF16_T1_TunnelMultiplexedTCPStreamsBinaryAndJSON(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "44444444-4444-4444-4444-444444444447"
	)

	port1 := startLoopbackTCPBannerServer(t, []byte("S1:"))
	port2 := startLoopbackTCPBannerServer(t, []byte("S2:"))

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
			"allowedPorts":  []int{port1, port2},
			"allowLoopback": true,
		},
	})
	if !ok {
		t.Fatalf("start proxy: %s", errMsg)
	}
	_ = waitSubMsg(t, readySub, 5*time.Second)

	tunnelIn := sessionSubject(clientID, siteID, agentID, sessionID, "tunnel.in")

	open1, _ := json.Marshal(map[string]any{"streamId": 1, "op": "open", "targetHost": "127.0.0.1", "targetPort": port1, "protocol": "tcp"})
	open2, _ := json.Marshal(map[string]any{"streamId": 2, "op": "open", "targetHost": "127.0.0.1", "targetPort": port2, "protocol": "ssh"})
	_ = viewerNC.Publish(tunnelIn, open1)
	_ = viewerNC.Publish(tunnelIn, open2)
	_ = viewerNC.Flush()

	opened := map[uint32]bool{}
	for len(opened) < 2 {
		m := waitSubMsg(t, tunnelOutSub, 5*time.Second)
		isBin, _, sid, _, ctrl, err := remotesession.DecodeTunnelFrame(m.Data)
		if err != nil {
			t.Fatalf("DecodeTunnelFrame: %v", err)
		}
		if !isBin && ctrl.Op == "opened" {
			opened[sid] = true
		}
	}

	// Envia frames binarios 0x01 intercalados nos 2 streams
	_ = viewerNC.Publish(tunnelIn, remotesession.EncodeTunnelBinaryData(nil, 1, []byte("alpha")))
	_ = viewerNC.Publish(tunnelIn, remotesession.EncodeTunnelBinaryData(nil, 2, []byte("beta")))
	_ = viewerNC.Flush()

	got := map[uint32][]byte{}
	deadline := time.Now().Add(5 * time.Second)
	for (string(got[1]) != "S1:alpha" || string(got[2]) != "S2:beta") && time.Now().Before(deadline) {
		m := waitSubMsg(t, tunnelOutSub, 5*time.Second)
		isBin, opByte, sid, payload, _, err := remotesession.DecodeTunnelFrame(m.Data)
		if err != nil {
			t.Fatalf("DecodeTunnelFrame: %v", err)
		}
		if isBin && opByte == remotesession.TunnelOpData {
			got[sid] = append(got[sid], payload...)
		}
	}
	if string(got[1]) != "S1:alpha" || string(got[2]) != "S2:beta" {
		t.Fatalf("dados multiplexados divergentes: stream1=%q stream2=%q", string(got[1]), string(got[2]))
	}

	close1, _ := json.Marshal(map[string]any{"streamId": 1, "op": "close"})
	close2, _ := json.Marshal(map[string]any{"streamId": 2, "op": "close"})
	_ = viewerNC.Publish(tunnelIn, close1)
	_ = viewerNC.Publish(tunnelIn, close2)
	_ = viewerNC.Flush()
}

func TestF16_T1_RdpSessionKindAndPreflightCheck(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "44444444-4444-4444-4444-444444444448"
	)

	blockedPort := startLoopbackTCPBannerServer(t, []byte("NOPE"))
	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	readySub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "proxy.ready"))
	tunnelOutSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "tunnel.out"))

	mgr := newTestSessionManager(t, agentNC, clientID, siteID, agentID)
	ok, errMsg := mgr.HandleCommand(context.Background(), map[string]any{
		"action":    "start",
		"sessionId": sessionID,
		"kind":      "rdp",
	})
	if !ok {
		t.Fatalf("HandleCommand(start rdp) falhou: %s", errMsg)
	}
	_ = waitSubMsg(t, readySub, 5*time.Second)

	tunnelIn := sessionSubject(clientID, siteID, agentID, sessionID, "tunnel.in")
	preflightReq, _ := json.Marshal(map[string]any{"streamId": 0, "op": "preflight"})
	_ = viewerNC.Publish(tunnelIn, preflightReq)
	_ = viewerNC.Flush()

	outMsg := waitSubMsg(t, tunnelOutSub, 5*time.Second)
	isBin, _, _, _, ctrl, err := remotesession.DecodeTunnelFrame(outMsg.Data)
	if err != nil || isBin || ctrl == nil || ctrl.Op != "preflight" || ctrl.RdpStatus == nil {
		t.Fatalf("resposta preflight inesperada: err=%v isBin=%v ctrl=%+v", err, isBin, ctrl)
	}
	if ctrl.RdpStatus.Port != 3389 {
		t.Fatalf("porta RDP esperada 3389, obteve %d", ctrl.RdpStatus.Port)
	}

	// Em sessao kind=rdp, abrir porta arbitraria que nao seja a porta RDP travada deve falhar
	badOpen, _ := json.Marshal(map[string]any{"streamId": 10, "op": "open", "targetHost": "127.0.0.1", "targetPort": blockedPort, "protocol": "tcp"})
	_ = viewerNC.Publish(tunnelIn, badOpen)
	_ = viewerNC.Flush()
	errResp := waitSubMsg(t, tunnelOutSub, 5*time.Second)
	_, _, _, _, errCtrl, _ := remotesession.DecodeTunnelFrame(errResp.Data)
	if errCtrl == nil || errCtrl.Op != "error" {
		t.Fatalf("esperava op=error ao tentar porta arbitraria (%d) em sessao rdp, obteve %+v", blockedPort, errCtrl)
	}
}

func TestF16_T1_NatsPermissionsGateNoProxyAndNoRdp(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "44444444-4444-4444-4444-444444444449"
	)

	// 1. RightsNoProxy & RightsNoRdp bloqueiam start no Manager
	_, natsURL := startEmbeddedNATS(t, false, nil)
	nc := newTestNATSClient(t, natsURL, "")
	mgr := newTestSessionManager(t, nc, clientID, siteID, agentID)

	if ok, _ := mgr.HandleCommand(context.Background(), map[string]any{
		"action":    "start",
		"sessionId": sessionID,
		"kind":      "proxy",
		"rights":    int(remotesession.RightsNoProxy),
	}); ok {
		t.Fatalf("Manager aceitou kind=proxy mesmo com RightsNoProxy")
	}

	if ok, _ := mgr.HandleCommand(context.Background(), map[string]any{
		"action":    "start",
		"sessionId": sessionID,
		"kind":      "rdp",
		"rights":    int(remotesession.RightsNoRdp),
	}); ok {
		t.Fatalf("Manager aceitou kind=rdp mesmo com RightsNoRdp")
	}

	// 2. ACL NATS negando *.proxy.* para viewer com NoProxy
	auth := tenantAuthFunc(func(c server.ClientAuthentication) bool {
		switch c.GetOpts().Token {
		case "viewer_no_proxy":
			c.RegisterUser(&server.User{
				Permissions: &server.Permissions{
					Publish: &server.SubjectPermission{
						Allow: []string{sessionSubject(clientID, siteID, agentID, sessionID, "input")},
						Deny:  []string{sessionSubject(clientID, siteID, agentID, sessionID, "proxy.>")},
					},
					Subscribe: &server.SubjectPermission{Allow: []string{">"}},
				},
			})
			return true
		case "agent_ok":
			c.RegisterUser(&server.User{
				Permissions: &server.Permissions{
					Publish:   &server.SubjectPermission{Allow: []string{">"}},
					Subscribe: &server.SubjectPermission{Allow: []string{">"}},
				},
			})
			return true
		}
		return false
	})
	_, aclURL := startEmbeddedNATS(t, false, auth)
	vNC := newTestNATSClient(t, aclURL, "viewer_no_proxy")
	agentSub := subscribeSync(t, newTestNATSClient(t, aclURL, "agent_ok"), ">")
	_ = agentSub

	_ = vNC.Publish(sessionSubject(clientID, siteID, agentID, sessionID, "proxy.req"), []byte(`{}`))
	_ = vNC.Flush()
	if err := vNC.LastError(); err == nil || !strings.Contains(err.Error(), "Permissions Violation") {
		t.Fatalf("esperava Permissions Violation ao publicar proxy.req sem permissao, got=%v", err)
	}
}

func TestF16_T1_CliRdpTunnelLoopbackForwarder(t *testing.T) {
	// Invoca discovery-agent.exe --rdp-tunnel sem parametros obrigatorios.
	// Deve validar argumentos e sair com codigo != 0 sem abrir GUI ou travar.
	code, out, errOut, err := execCLI(t, 3*time.Second, nil, "discovery-agent.exe", "--rdp-tunnel")
	if err == context.DeadlineExceeded || code == -1 {
		t.Fatalf("F16 violacao: `discovery-agent.exe --rdp-tunnel` travou / abriu GUI ao inves de validar CLI e sair imediatamente (stdout=%q stderr=%q)", out, errOut)
	}
	if code == 0 {
		t.Fatalf("esperava exit code != 0 para `--rdp-tunnel` sem flags obrigatorias, obteve 0 (stdout=%q stderr=%q)", out, errOut)
	}
	combined := strings.ToLower(out + "\n" + errOut)
	if !strings.Contains(combined, "rdp") && !strings.Contains(combined, "tunnel") && !strings.Contains(combined, "uso") {
		t.Fatalf("esperava mensagem de uso/validacao de --rdp-tunnel, obteve: %q", combined)
	}
}
