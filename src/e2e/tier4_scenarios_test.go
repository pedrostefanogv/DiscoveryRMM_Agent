package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/nats-io/nats.go"

	"discovery/app/core/agentconn"
	"discovery/app/core/automation"
	"discovery/app/core/platform"
	"discovery/app/core/remotesession"
	"discovery/app/core/screen"
	"discovery/app/core/selfupdate"
	"discovery/app/core/terminal"
)

// Scenario 1 (TEST_INFRA Scenario 7): Clean Windows Install & Decommission Security Chain (F11, F12, F14, F18)
func TestTier4_Scenario1_CleanInstallMigrationAndIPCCompliance(t *testing.T) {
	// 1. Invariantes NSIS (F11)
	nsi := readSrcFile(t, filepath.Join("build", "windows", "installer", "project.nsi"))
	idxWebView := strings.Index(nsi, "!insertmacro wails.webview2runtime")
	idxFiles := strings.Index(nsi, "!insertmacro wails.files")
	if idxWebView < 0 || idxFiles < 0 || !regexp.MustCompile(`(?im)^\s*SetOutPath\s+"?\$INSTDIR"?\s*$`).MatchString(nsi[idxWebView:idxFiles]) {
		t.Fatalf("Scenario1/F11: project.nsi nao restaura SetOutPath $INSTDIR apos wails.webview2runtime")
	}
	if regexp.MustCompile(`(?i)icacls\.exe[^\r\n]*\$R0\\Discovery["'][^\r\n]*\*S-1-5-32-545:\([^)]*[MFW][^)]*\)`).MatchString(nsi) {
		t.Fatalf("Scenario1/F11: project.nsi concede escrita a Users em %%ProgramData%%\\Discovery")
	}

	// 2. Provisiona config.json com migracao DPAPI + HardenSecretFileACL (F12)
	progDataSim := t.TempDir()
	_, _ = runIcacls(t, progDataSim, "/grant", "*S-1-5-32-545:(OI)(CI)(M)", "/Q")
	cfgPath := filepath.Join(progDataSim, "config.json")
	secretTok := "mdz_prod_agent_token_scenario1"
	encBytes, err := dpapiProtect([]byte(secretTok))
	if err != nil {
		t.Fatalf("dpapiProtect: %v", err)
	}
	cfgJSON, _ := json.Marshal(map[string]any{
		"apiServer":      "api.discovery.local",
		"agentId":        "11111111-1111-1111-1111-111111111111",
		"authTokenDpapi": base64.StdEncoding.EncodeToString(encBytes),
	})
	if err := os.WriteFile(cfgPath, cfgJSON, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := platform.HardenSecretFileACL(cfgPath); err != nil {
		t.Fatalf("HardenSecretFileACL: %v", err)
	}
	aclOut, _ := runIcacls(t, cfgPath)
	if strings.Contains(strings.ToLower(aclOut), "users:") || strings.Contains(strings.ToLower(aclOut), `builtin\users`) {
		t.Fatalf("Scenario1/F12: Users manteve acesso em config.json:\n%s", aclOut)
	}
	plain, err := dpapiUnprotect(encBytes)
	if err != nil || string(plain) != secretTok {
		t.Fatalf("Scenario1/F12: DPAPI round-trip falhou: err=%v", err)
	}

	// 3. Pipe IPC com SDDL e redacao de authToken (F12)
	pipeName := `\\.\pipe\discovery-e2e-s1-` + time.Now().Format("150405.000000")
	ln, err := winio.ListenPipe(pipeName, &winio.PipeConfig{SecurityDescriptor: "D:(A;;GA;;;SY)(A;;GRGW;;;BU)"})
	if err != nil {
		t.Fatalf("ListenPipe: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadBytes('\n')
		resp, _ := json.Marshal(map[string]any{
			"id":     "s1",
			"ok":     true,
			"result": map[string]any{"apiServer": "api.discovery.local", "hasAuthToken": true},
		})
		_, _ = conn.Write(append(resp, '\n'))
	}()

	d := 2 * time.Second
	c, err := winio.DialPipe(pipeName, &d)
	if err != nil {
		t.Fatalf("DialPipe: %v", err)
	}
	defer c.Close()
	_, _ = c.Write([]byte(`{"v":2,"id":"s1","method":"config:get"}` + "\n"))
	ipcResp := readJSONLine(t, bufio.NewReader(c), 2*time.Second)
	rawIPC, _ := json.Marshal(ipcResp)
	if strings.Contains(string(rawIPC), secretTok) || strings.Contains(string(rawIPC), "authToken") {
		t.Fatalf("Scenario1/F12: IPC vazou segredo: %s", string(rawIPC))
	}

	// 4. CLIs & Service decoupling (F14, F18)
	if code, _, _, _ := execCLI(t, 5*time.Second, nil, "discovery-agent.exe", "--agent-delete-cleanup"); code == 0 {
		t.Fatalf("Scenario1/F18: --agent-delete-cleanup nao falhou fechado sem elevacao")
	}
	if code, _, _, _ := execCLI(t, 5*time.Second, []byte("bad"), "discovery-service.exe", "--remote-session-worker"); code != 2 {
		t.Fatalf("Scenario1/F18: --remote-session-worker stdin invalido retornou %d (want 2)", code)
	}
	if code, out, err := runGoCmd(t, 30*time.Second, "list", "-deps", "./cmd/discovery-service"); err != nil || code != 0 || strings.Contains(out, "wailsapp/wails") {
		t.Fatalf("Scenario1/F14: discovery-service ainda acoplado ao Wails (err=%v code=%d)", err, code)
	}
}

// Scenario 2: Multi-Tenant Isolation & Agent Command Pipeline (F13, F15)
func TestTier4_Scenario2_MultiTenantIsolationAndAgentCommandPipeline(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"
		clientB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		siteB   = "22222222-2222-2222-2222-222222222222"
		agentB  = "bbbb2222-2222-2222-2222-222222222222"
	)

	var gotNote atomic.Value
	scriptDone := make(chan struct{}, 1)
	pwnPath := filepath.Join(t.TempDir(), "s2_pwn.txt")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/automation/policy-sync"):
			script := "Write-Output $env:MDZ_CUSTOM_FIELDS\n"
			sum := sha256.Sum256([]byte(script))
			_ = json.NewEncoder(w).Encode(automation.PolicySyncResponse{
				PolicyFingerprint: "fp-s2", TaskCount: 1,
				Tasks: []automation.AutomationTask{{
					CommandID: "cmd-s2", TaskID: "task-s2", Name: "S2", ActionType: automation.ActionRunScript,
					ScopeType: automation.ApprovalScopeAgent, NotificationMode: automation.NotificationModeSilent,
					WriteOutputToNote: true, TriggerOnAgentCheckIn: true,
					Script: &automation.AutomationScript{
						ScriptID: "sc-s2", Name: "sc-s2", Version: "1", ScriptType: automation.ScriptPowerShell,
						ContentHashSHA256: hex.EncodeToString(sum[:]), Content: script,
					},
				}},
			})
		case strings.HasSuffix(r.URL.Path, "/custom-fields/runtime"):
			_ = json.NewEncoder(w).Encode([]automation.RuntimeCustomField{{
				DefinitionID: "df-1", Name: "evil_input", ScopeType: automation.FieldScopeAgent,
				ValueJson: mustMarshalJSON(t, `$(calc.exe) & echo x > "`+pwnPath+`"`),
			}})
		case strings.HasSuffix(r.URL.Path, "/notes"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotNote.Store(fmt.Sprint(body["content"]))
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/result"):
			select {
			case scriptDone <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	svc := automation.NewService(func() automation.RuntimeConfig {
		return automation.RuntimeConfig{BaseURL: srv.URL, Token: "mdz_s2", AgentID: agentA}
	}, nil)

	_, natsURL, adminNC := startTenantIsolatedNATSServer(t, clientA, siteA, agentA, clientB, siteB, agentB)
	jsAdmin, _ := adminNC.JetStream()

	var bCommands atomic.Int32
	connA := make(chan struct{}, 1)
	chatA := make(chan string, 2)

	startTestAgentRuntime(t, agentconn.Options{
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{NatsServer: natsURL, AuthToken: "mdz_tenantA", ClientID: clientA, SiteID: siteA, AgentID: agentA}
		},
		OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
			select {
			case connA <- struct{}{}:
			default:
			}
		},
		HandleCommand: func(ctx context.Context, cmdType string, _ any) (bool, int, string, string) {
			if cmdType == "execpolicy" {
				_, _ = svc.RefreshPolicy(ctx, true)
				svc.TriggerAgentCheckInTasks(ctx)
				return true, 0, "ok", ""
			}
			return false, 0, "", ""
		},
		HandleSupportChatEnvelope: func(_ context.Context, data []byte) error {
			chatA <- string(data)
			return nil
		},
	})
	startTestAgentRuntime(t, agentconn.Options{
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{NatsServer: natsURL, AuthToken: "mdz_tenantB", ClientID: clientB, SiteID: siteB, AgentID: agentB}
		},
		HandleCommand: func(_ context.Context, _ string, _ any) (bool, int, string, string) {
			bCommands.Add(1)
			return true, 0, "b", ""
		},
	})

	<-connA
	time.Sleep(150 * time.Millisecond)

	// Rogue probe de Tenant A tentando criar consumer no stream do Tenant B
	ncA := newTestNATSClient(t, natsURL, "mdz_tenantA")
	jsA, _ := ncA.JetStream()
	if _, err := jsA.AddConsumer("DISCOVERY_FANOUT_COMMANDS_"+stripHyphens(clientB), &nats.ConsumerConfig{
		Durable: "rogue", AckPolicy: nats.AckExplicitPolicy,
	}); err == nil {
		t.Fatalf("Scenario2/F15: Tenant A criou consumer no stream do Tenant B")
	}

	now := time.Now().UTC()
	cmdEnv, _ := json.Marshal(map[string]any{
		"dispatchId": "d-s2", "commandId": "c-s2", "commandType": "ExecPolicy",
		"targetScope": "client", "targetClientId": clientA,
		"issuedAtUtc": now.Format(time.RFC3339), "idempotencyKey": "k-s2",
	})
	_, _ = jsAdmin.Publish(fmt.Sprintf("tenant.%s.agents.command", clientA), cmdEnv)
	_, _ = jsAdmin.Publish(fmt.Sprintf("tenant.%s.site.%s.agent.%s.supportchat.server", clientA, siteA, agentA), []byte(`{"text":"support-s2"}`))
	_ = adminNC.Flush()

	select {
	case <-scriptDone:
	case <-time.After(20 * time.Second):
		t.Fatalf("timeout aguardando execucao de automacao em Scenario2")
	}
	select {
	case msg := <-chatA:
		if !strings.Contains(msg, "support-s2") {
			t.Fatalf("supportchat inesperado: %s", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout aguardando supportchat A")
	}
	if bCommands.Load() != 0 {
		t.Fatalf("Tenant B recebeu comando indevidamente")
	}
	if _, err := os.Stat(pwnPath); err == nil {
		t.Fatalf("Scenario2/F13: injecao de comando em custom field criou %s", pwnPath)
	}
	note, _ := gotNote.Load().(string)
	if !strings.Contains(note, "evil_input") {
		t.Fatalf("MDZ_CUSTOM_FIELDS nao propagado: %q", note)
	}
}

// Scenario 3: LAN Proxy HTTP/HTTPS + Multiplexed TCP/SSH + RDP Tunnel End-to-End (F15, F16, F17)
func TestTier4_Scenario3_LanProxyAndMultiplexedRdpTunnelEndToEnd(t *testing.T) {
	const (
		clientID = "11111111-1111-1111-1111-111111111111"
		siteID   = "22222222-2222-2222-2222-222222222222"
		agentID  = "33333333-3333-3333-3333-333333333333"
		sidProxy = "88888888-8888-8888-8888-888888888881"
		sidRdp   = "88888888-8888-8888-8888-888888888882"
	)

	_, httpURL, httpPort := startLoopbackHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lan":
			_, _ = w.Write([]byte("lan-device-ui"))
		case "/ssrf-redir":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		}
	}))
	sshPort := startLoopbackTCPBannerServer(t, []byte("SSH-2.0-"))
	rdpMockPort := startLoopbackTCPBannerServer(t, []byte("TPKT:"))

	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	mgr := newTestSessionManager(t, agentNC, clientID, siteID, agentID)

	// 1. Proxy session (HTTP + SSRF block + SSH multiplexed tunnel)
	proxyReady := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidProxy, "proxy.ready"))
	proxyResp := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidProxy, "proxy.resp"))
	proxyOut := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidProxy, "tunnel.out"))

	ok, errMsg := mgr.HandleCommand(context.Background(), map[string]any{
		"action": "start", "sessionId": sidProxy, "kind": "proxy",
		"proxy": map[string]any{"allowlist": []string{"127.0.0.1/32"}, "allowedPorts": []int{httpPort, sshPort}, "allowLoopback": true},
	})
	if !ok {
		t.Fatalf("start proxy: %s", errMsg)
	}
	_ = waitSubMsg(t, proxyReady, 5*time.Second)

	// HTTP allowed + SSRF redirect blocked
	rOK, _ := json.Marshal(map[string]any{"requestId": "lan-1", "method": "GET", "url": httpURL + "/lan"})
	rSSRF, _ := json.Marshal(map[string]any{"requestId": "ssrf-1", "method": "GET", "url": httpURL + "/ssrf-redir"})
	_ = viewerNC.Publish(sessionSubject(clientID, siteID, agentID, sidProxy, "proxy.req"), rOK)
	_ = viewerNC.Publish(sessionSubject(clientID, siteID, agentID, sidProxy, "proxy.req"), rSSRF)
	_ = viewerNC.Flush()

	seenProxy := map[string]map[string]any{}
	for len(seenProxy) < 2 {
		m := waitSubMsg(t, proxyResp, 5*time.Second)
		var r map[string]any
		_ = json.Unmarshal(m.Data, &r)
		seenProxy[fmt.Sprint(r["requestId"])] = r
	}
	if seenProxy["lan-1"]["success"] != true {
		t.Fatalf("GET /lan falhou: %v", seenProxy["lan-1"])
	}
	if seenProxy["ssrf-1"]["success"] != false {
		t.Fatalf("redirect SSRF para metadata nao foi bloqueado: %v", seenProxy["ssrf-1"])
	}

	// Multiplexed TCP tunnel SSH stream
	tIn := sessionSubject(clientID, siteID, agentID, sidProxy, "tunnel.in")
	openSSH, _ := json.Marshal(map[string]any{"streamId": 5, "op": "open", "targetHost": "127.0.0.1", "targetPort": sshPort, "protocol": "ssh"})
	_ = viewerNC.Publish(tIn, openSSH)
	_ = viewerNC.Flush()
	_ = waitSubMsg(t, proxyOut, 5*time.Second) // opened
	_ = viewerNC.Publish(tIn, remotesession.EncodeTunnelBinaryData(nil, 5, []byte("OpenSSH")))
	_ = viewerNC.Flush()
	dataMsg := waitSubMsg(t, proxyOut, 5*time.Second)
	isBin, _, sid, payload, _, _ := remotesession.DecodeTunnelFrame(dataMsg.Data)
	if !isBin || sid != 5 || string(payload) != "SSH-2.0-OpenSSH" {
		t.Fatalf("tunel SSH inesperado: isBin=%v sid=%d payload=%q", isBin, sid, string(payload))
	}
	_, _ = mgr.HandleCommand(context.Background(), map[string]any{"action": "stop", "sessionId": sidProxy})

	// 2. RDP session preflight + binary 0x01 data exchange
	rdpReady := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidRdp, "proxy.ready"))
	rdpOut := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidRdp, "tunnel.out"))
	ok, errMsg = mgr.HandleCommand(context.Background(), map[string]any{
		"action": "start", "sessionId": sidRdp, "kind": "rdp", "rdpPort": rdpMockPort,
	})
	if !ok {
		t.Fatalf("start rdp: %s", errMsg)
	}
	_ = waitSubMsg(t, rdpReady, 5*time.Second)

	rdpIn := sessionSubject(clientID, siteID, agentID, sidRdp, "tunnel.in")
	prefReq, _ := json.Marshal(map[string]any{"streamId": 0, "op": "preflight"})
	_ = viewerNC.Publish(rdpIn, prefReq)
	_ = viewerNC.Flush()
	prefMsg := waitSubMsg(t, rdpOut, 5*time.Second)
	_, _, _, _, prefCtrl, _ := remotesession.DecodeTunnelFrame(prefMsg.Data)
	if prefCtrl == nil || prefCtrl.Op != "preflight" || prefCtrl.RdpStatus == nil {
		t.Fatalf("preflight RDP invalido: %+v", prefCtrl)
	}

	openRdp, _ := json.Marshal(map[string]any{"streamId": 9, "op": "open", "targetHost": "127.0.0.1", "targetPort": rdpMockPort, "protocol": "rdp"})
	_ = viewerNC.Publish(rdpIn, openRdp)
	_ = viewerNC.Flush()
	_ = waitSubMsg(t, rdpOut, 5*time.Second) // opened
	_ = viewerNC.Publish(rdpIn, remotesession.EncodeTunnelBinaryData(nil, 9, []byte{0x03, 0x00, 0x00, 0x13}))
	_ = viewerNC.Flush()
	rdpData := waitSubMsg(t, rdpOut, 5*time.Second)
	isBinR, _, sidR, payR, _, _ := remotesession.DecodeTunnelFrame(rdpData.Data)
	if !isBinR || sidR != 9 || !bytes.HasPrefix(payR, []byte("TPKT:")) {
		t.Fatalf("tunel RDP payload inesperado: isBin=%v sid=%d payload=%x", isBinR, sidR, payR)
	}
}

// Scenario 4: Headless Files Binary Transfer + Terminal Ops (F13, F16, F17)
func TestTier4_Scenario4_HeadlessFilesBinaryTransferAndTerminalOps(t *testing.T) {
	const (
		clientID = "11111111-1111-1111-1111-111111111111"
		siteID   = "22222222-2222-2222-2222-222222222222"
		agentID  = "33333333-3333-3333-3333-333333333333"
		sidFiles = "88888888-8888-8888-8888-888888888891"
		sidTerm  = "88888888-8888-8888-8888-888888888892"
	)

	rootDir := t.TempDir()
	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	mgr := newTestSessionManager(t, agentNC, clientID, siteID, agentID)

	filesReady := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidFiles, "files.ready"))
	filesResp := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidFiles, "files.resp"))

	if ok, e := mgr.HandleCommand(context.Background(), map[string]any{
		"action": "start", "sessionId": sidFiles, "kind": "files", "rootPath": rootDir,
	}); !ok {
		t.Fatalf("start files: %s", e)
	}
	_ = waitSubMsg(t, filesReady, 5*time.Second)

	// 1 MiB binario via 'DF' + list concorrente
	blob := make([]byte, 1024*1024)
	_, _ = rand.Read(blob)
	const chunkSz = 256 * 1024
	reqSubj := sessionSubject(clientID, siteID, agentID, sidFiles, "files.req")
	for ci := 0; ci < 4; ci++ {
		frame := encodeBinaryFileFrame(map[string]any{
			"version": 1, "requestId": fmt.Sprintf("s4-put-%d", ci), "transferId": "tx-s4",
			"action": "put", "path": "big.bin", "chunkIndex": ci, "totalChunks": 4, "chunkSize": chunkSz,
		}, blob[ci*chunkSz:(ci+1)*chunkSz])
		_ = viewerNC.Publish(reqSubj, frame)
		_ = viewerNC.Flush()
		m := waitSubMsg(t, filesResp, 5*time.Second)
		var r map[string]any
		if err := json.Unmarshal(m.Data, &r); err != nil || r["success"] != true {
			t.Fatalf("Scenario4/F17 upload binario 'DF' falhou: err=%v data=%s", err, string(m.Data))
		}
	}

	// Terminal + RunAsUser check
	if terminal.EffectiveRunAs(terminal.RunAsUser) == "system" {
		t.Fatalf("Scenario4/F13: EffectiveRunAs(RunAsUser) retornou system")
	}
	ns := remotesession.NewNatsStreamHandler(agentNC, clientID, siteID, agentID)
	st := remotesession.NewSessionTerminal(sidTerm, ns, false)
	if _, err := st.Start(context.Background(), terminal.ShellCmd, 80, 24); err != nil {
		t.Fatalf("SessionTerminal.Start: %v", err)
	}
	defer st.Stop()
	termOut := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidTerm, "term.out"))
	_ = viewerNC.Publish(sessionSubject(clientID, siteID, agentID, sidTerm, "term.in"), []byte(`{"data":"echo s4_ok\r\n","pv":1}`))
	_ = viewerNC.Flush()
	_ = waitSubMsg(t, termOut, 5*time.Second)
}

// Scenario 5: Screen Capture Optimization Pipeline & Clipboard (F17)
func TestTier4_Scenario5_ScreenCaptureOptimizationPipelineAndClipboard(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "88888888-8888-8888-8888-888888888895"
	)

	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	fragSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "frame.frag"))
	ns := remotesession.NewNatsStreamHandler(agentNC, clientID, siteID, agentID)
	ns.SetMaxPayloadBytes(4 * 1024)

	// Pipeline: GetPooledFrame -> DirtyDetector -> EncodeDirtyRects -> PublishFrame JUMBO
	det := screen.NewDirtyDetector(32)
	f := screen.GetPooledFrame(320, 240, 320*4)
	defer screen.PutPooledFrame(f)
	for i := 0; i < len(f.Data); i += 4 {
		f.Data[i], f.Data[i+1], f.Data[i+2], f.Data[i+3] = 0x20, 0x40, 0x60, 0xFF
	}
	rects1 := det.Detect(f)
	if len(rects1) != 1 {
		t.Fatalf("primeiro frame esperava 1 rect, got %d", len(rects1))
	}
	if rectsIdle := det.Detect(f); len(rectsIdle) != 0 {
		t.Fatalf("frame identico esperava 0 dirty rects, got %d", len(rectsIdle))
	}
	// Altera bloco 32x32
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			off := y*f.Stride + x*4
			f.Data[off] = 0xFF
		}
	}
	rectsDirty := det.Detect(f)
	if len(rectsDirty) == 0 {
		t.Fatalf("alteracao 32x32 deveria gerar >= 1 dirty rect")
	}
	encoded, err := screen.EncodeDirtyRects(f, rectsDirty, 70)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("EncodeDirtyRects: %v", err)
	}
	// Fragmenta sobre NATS
	big := bytes.Repeat(encoded, 10)
	if len(big) <= 4*1024 {
		big = bytes.Repeat([]byte{0xAA}, 10*1024)
	}
	frameWire, err := remotesession.EncodeFrameMsgPack(1, 123, 320, 240, big)
	if err != nil {
		t.Fatalf("EncodeFrameMsgPack: %v", err)
	}
	if err := ns.PublishFrame(sessionID, frameWire); err != nil {
		t.Fatalf("PublishFrame: %v", err)
	}
	_ = agentNC.Flush()
	_ = waitSubMsg(t, fragSub, 3*time.Second)

	// BandwidthGate & Clipboard contract check
	gate := remotesession.NewBandwidthGate(remotesession.BandwidthGateConfig{Mode: remotesession.BandwidthAuto})
	if snap := gate.Snapshot(); snap.Mode != remotesession.BandwidthAuto {
		t.Fatalf("BandwidthGate modo inesperado: %s", snap.Mode)
	}
	mgrSrc := readSrcFile(t, filepath.Join("app", "core", "remotesession", "manager.go"))
	if strings.Contains(mgrSrc, "time.NewTicker(1 * time.Second)") && strings.Contains(mgrSrc, "handleClipboardUpdate") {
		t.Fatalf("Scenario5/F17: clipboard ainda usa polling de 1s")
	}
}

// Scenario 6: Supply-Chain Self-Update, Decommission & Build Gates (F11, F13, F14, F18)
func TestTier4_Scenario6_SupplyChainSelfUpdateAndBuildGates(t *testing.T) {
	// 1. Self-Update rejeita binario externo nao verificado / SHA divergente
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("MZ-untrusted"))
	}))
	defer srv.Close()

	var launched atomic.Int32
	u := &selfupdate.Updater{
		ApiScheme: "http", ApiServer: strings.TrimPrefix(srv.URL, "http://"),
		TempDir: t.TempDir(), ServiceMode: true,
		GetPolicy: func() selfupdate.Policy {
			p := selfupdate.DefaultPolicy()
			p.RequireSignatureValidation = true
			return p
		},
		OnSelfUpdateInstall: func(_ context.Context, _, _ string) error {
			launched.Add(1)
			return nil
		},
	}
	if err := u.InstallFromURL(context.Background(), "9.9.9", srv.URL+"/agent.exe"); err == nil || launched.Load() != 0 {
		t.Fatalf("Scenario6/F13: InstallFromURL aceitou payload sem SHA-256/Authenticode")
	}

	// 2. Decommission valida caminho ao retomar pendente
	decompSrc := readSrcFile(t, filepath.Join("app", "agent_commands_decommission.go"))
	idx := strings.Index(decompSrc, "func (a *App) resumeAgentDecommissionIfPending")
	if idx < 0 || !strings.Contains(decompSrc[idx:minInt(len(decompSrc), idx+900)], "validateUninstallerPath") {
		t.Fatalf("Scenario6/F11: resumeAgentDecommissionIfPending omite validateUninstallerPath")
	}

	// 3. Script Linux + CI + go.mod
	script := readRepoFile(t, filepath.Join("build", "server-api", "linux", "build-agent-server-linux.sh"))
	if !strings.Contains(script, "./cmd/discovery-service") || strings.Contains(script, "v3.0.0-beta.23") {
		t.Fatalf("Scenario6/F13: build-agent-server-linux.sh desatualizado")
	}
	wf := readRepoFile(t, filepath.Join(".github", "workflows", "release-agent-on-tag.yml"))
	if !strings.Contains(wf, "go vet") || !strings.Contains(wf, "go test") {
		t.Fatalf("Scenario6/F14: release-agent-on-tag.yml sem go vet/go test")
	}
	// F18 (contrato atualizado 2026-11-21): o pion pode existir, mas so COM uso
	// real. O guard de equivalencia vive no tier1; aqui a checagem e a mesma
	// ideia em uma linha, para o cenario nao passar com dependencia morta.
	gomod := readSrcFile(t, "go.mod")
	if strings.Contains(gomod, "github.com/pion/webrtc") {
		code, out, err := runGoCmd(t, 30*time.Second, "list", "-f", "{{range .Imports}}{{.}},{{end}}", "./app/core/remotesession/")
		if err != nil || code != 0 || !strings.Contains(out, "github.com/pion/webrtc") {
			t.Fatalf("Scenario6/F18: go.mod declara pion/webrtc mas o pacote de acesso remoto nao o importa (dependencia morta)")
		}
	}

	// 4. go vet em ./e2e/...
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		code, out, err := runGoCmd(t, 45*time.Second, "vet", "./e2e/...")
		if err != nil || code != 0 {
			t.Errorf("go vet ./e2e/... falhou: %v\n%s", err, out)
		}
	}()
	wg.Wait()
}
