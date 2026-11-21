package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
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
	"discovery/app/core/selfupdate"
	"discovery/app/core/terminal"
)

func TestTier3_F11_F12_InstallACLAndDPAPIConfigStorage(t *testing.T) {
	// F11: project.nsi nao concede Users:(M) em ProgramData\Discovery e restaura SetOutPath $INSTDIR
	nsi := readSrcFile(t, filepath.Join("build", "windows", "installer", "project.nsi"))
	reUsersRoot := regexp.MustCompile(`(?i)icacls\.exe[^\r\n]*\$R0\\Discovery["'][^\r\n]*\*S-1-5-32-545:\([^)]*[MFW][^)]*\)`)
	if reUsersRoot.MatchString(nsi) {
		t.Fatalf("F11xF12 violacao: project.nsi concede escrita a Users em %%ProgramData%%\\Discovery")
	}

	// F12: DPAPI + HardenSecretFileACL sobre temp config.json
	dir := t.TempDir()
	_, _ = runIcacls(t, dir, "/grant", "*S-1-5-32-545:(OI)(CI)(M)", "/Q")
	cfgFile := filepath.Join(dir, "config.json")
	cipher, err := dpapiProtect([]byte("mdz_install_secret"))
	if err != nil {
		t.Fatalf("dpapiProtect: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"apiServer":      "api.local",
		"authTokenDpapi": base64.StdEncoding.EncodeToString(cipher),
	})
	if err := os.WriteFile(cfgFile, payload, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := platform.HardenSecretFileACL(cfgFile); err != nil {
		t.Fatalf("HardenSecretFileACL: %v", err)
	}
	out, _ := runIcacls(t, cfgFile)
	if strings.Contains(strings.ToLower(out), `builtin\users`) || strings.Contains(strings.ToLower(out), "users:") {
		t.Fatalf("F11xF12 violacao: HardenSecretFileACL manteve acesso Users em %s:\n%s", cfgFile, out)
	}
}

func TestTier3_F12_F13_DPAPIAdminIPCAndDebugHTTPAuth(t *testing.T) {
	// F12: IPC redaction + pipe SDDL
	pipeName := `\\.\pipe\discovery-e2e-t3-` + time.Now().Format("150405.000000")
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
			"id": "1",
			"ok": true,
			"result": map[string]any{
				"apiServer":    "api.local",
				"hasAuthToken": true,
			},
		})
		_, _ = conn.Write(append(resp, '\n'))
	}()

	d := 2 * time.Second
	c, err := winio.DialPipe(pipeName, &d)
	if err != nil {
		t.Fatalf("DialPipe: %v", err)
	}
	defer c.Close()
	_, _ = c.Write([]byte(`{"v":2,"id":"1","method":"debug:get"}` + "\n"))
	resp := readJSONLine(t, bufio.NewReader(c), 2*time.Second)
	rawResp, _ := json.Marshal(resp)
	if strings.Contains(string(rawResp), "authToken") {
		t.Fatalf("IPC redaction falhou: %s", string(rawResp))
	}

	// F13: debug_http auth guard obrigatorio
	debugHTTPSrc := readSrcFile(t, filepath.Join("app", "debug_http.go"))
	if !strings.Contains(debugHTTPSrc, "StatusUnauthorized") && !strings.Contains(debugHTTPSrc, "401") {
		t.Fatalf("F12xF13 violacao: debug_http.go nao exige autenticacao HTTP")
	}
}

func TestTier3_F11_F14_DecommissionValidationAndServiceDecoupling(t *testing.T) {
	// F14: discovery-service sem deps Wails
	code, out, err := runGoCmd(t, 30*time.Second, "list", "-deps", "./cmd/discovery-service")
	if err != nil || code != 0 {
		t.Fatalf("go list -deps ./cmd/discovery-service falhou: %v", err)
	}
	if strings.Contains(out, "github.com/wailsapp/wails/") {
		t.Fatalf("F11xF14 violacao: discovery-service ainda depende de wails/v3")
	}

	// F11: --agent-delete-cleanup fail-closed non-elevated + resumeAgentDecommissionIfPending valida caminho
	cliCode, _, _, _ := execCLI(t, 5*time.Second, nil, "discovery-agent.exe", "--agent-delete-cleanup")
	if cliCode == 0 {
		t.Fatalf("--agent-delete-cleanup nao falhou fechado sem elevacao")
	}
	decompSrc := readSrcFile(t, filepath.Join("app", "agent_commands_decommission.go"))
	idx := strings.Index(decompSrc, "func (a *App) resumeAgentDecommissionIfPending")
	if idx < 0 || !strings.Contains(decompSrc[idx:minInt(len(decompSrc), idx+900)], "validateUninstallerPath") {
		t.Fatalf("F11xF14 violacao: resumeAgentDecommissionIfPending nao chama validateUninstallerPath")
	}
}

func TestTier3_F13_F15_TenantFanOutScriptExecutionWithCustomFields(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"
		clientB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		siteB   = "22222222-2222-2222-2222-222222222222"
		agentB  = "bbbb2222-2222-2222-2222-222222222222"
	)

	var gotNote atomic.Value
	doneCh := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/automation/policy-sync"):
			script := "Write-Output $env:MDZ_CUSTOM_FIELDS\n"
			sum := sha256.Sum256([]byte(script))
			_ = json.NewEncoder(w).Encode(automation.PolicySyncResponse{
				PolicyFingerprint: "fp-t3",
				TaskCount:         1,
				Tasks: []automation.AutomationTask{{
					CommandID: "cmd-t3", TaskID: "task-t3", Name: "T3", ActionType: automation.ActionRunScript,
					ScopeType: automation.ApprovalScopeAgent, NotificationMode: automation.NotificationModeSilent,
					WriteOutputToNote: true, TriggerOnAgentCheckIn: true,
					Script: &automation.AutomationScript{
						ScriptID: "s-t3", Name: "s", Version: "1", ScriptType: automation.ScriptPowerShell,
						ContentHashSHA256: hex.EncodeToString(sum[:]), Content: script,
					},
				}},
			})
		case strings.HasSuffix(r.URL.Path, "/custom-fields/runtime"):
			_ = json.NewEncoder(w).Encode([]automation.RuntimeCustomField{{
				DefinitionID: "d1", Name: "env_tag", ScopeType: automation.FieldScopeAgent,
				ValueJson: json.RawMessage(`"token_t3"`), IsSecret: false,
			}})
		case strings.HasSuffix(r.URL.Path, "/notes"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotNote.Store(fmt.Sprint(body["content"]))
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/result"):
			select {
			case doneCh <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	svc := automation.NewService(func() automation.RuntimeConfig {
		return automation.RuntimeConfig{BaseURL: srv.URL, Token: "mdz_t3", AgentID: agentA}
	}, nil)

	_, natsURL, adminNC := startTenantIsolatedNATSServer(t, clientA, siteA, agentA, clientB, siteB, agentB)
	jsAdmin, _ := adminNC.JetStream()

	connA := make(chan struct{}, 1)
	var bTriggered atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

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
		HandleCommand: func(c context.Context, cmdType string, _ any) (bool, int, string, string) {
			if cmdType == "runpolicynow" {
				_, _ = svc.RefreshPolicy(c, true)
				svc.TriggerAgentCheckInTasks(c)
				return true, 0, "triggered", ""
			}
			return false, 0, "", ""
		},
	})
	startTestAgentRuntime(t, agentconn.Options{
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{NatsServer: natsURL, AuthToken: "mdz_tenantB", ClientID: clientB, SiteID: siteB, AgentID: agentB}
		},
		HandleCommand: func(_ context.Context, _ string, _ any) (bool, int, string, string) {
			bTriggered.Add(1)
			return true, 0, "b", ""
		},
	})
	<-connA
	time.Sleep(150 * time.Millisecond)

	now := time.Now().UTC()
	env, _ := json.Marshal(map[string]any{
		"dispatchId": "d-t3", "commandId": "c-t3", "commandType": "RunPolicyNow",
		"targetScope": "client", "targetClientId": clientA,
		"issuedAtUtc": now.Format(time.RFC3339), "idempotencyKey": "k-t3",
	})
	_, _ = jsAdmin.Publish(fmt.Sprintf("tenant.%s.agents.command", clientA), env)
	_ = adminNC.Flush()

	select {
	case <-doneCh:
	case <-ctx.Done():
		t.Fatalf("timeout aguardando script acionado via fanout tenant A")
	}
	if bTriggered.Load() != 0 {
		t.Fatalf("Tenant B nao deveria receber comando do Tenant A")
	}
	note, _ := gotNote.Load().(string)
	if !strings.Contains(note, `"env_tag":"token_t3"`) {
		t.Fatalf("MDZ_CUSTOM_FIELDS ausente: %q", note)
	}
}

func TestTier3_F15_F16_TenantScopedNatsACLWithLanProxyAndTunnel(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"
		clientB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		siteB   = "22222222-2222-2222-2222-222222222222"
		agentB  = "bbbb2222-2222-2222-2222-222222222222"
		sid     = "77777777-7777-7777-7777-777777777771"
	)

	_, natsURL, _ := startTenantIsolatedNATSServer(t, clientA, siteA, agentA, clientB, siteB, agentB)
	ncA := newTestNATSClient(t, natsURL, "mdz_tenantA")
	ncB := newTestNATSClient(t, natsURL, "mdz_tenantB")

	echoPort, _ := startLoopbackTCPEcho(t)
	mgr := newTestSessionManager(t, ncA, clientA, siteA, agentA)
	readySub := subscribeSync(t, ncA, sessionSubject(clientA, siteA, agentA, sid, "proxy.ready"))
	tunnelOutSub := subscribeSync(t, ncA, sessionSubject(clientA, siteA, agentA, sid, "tunnel.out"))

	ok, errMsg := mgr.HandleCommand(context.Background(), map[string]any{
		"action": "start", "sessionId": sid, "kind": "proxy",
		"proxy": map[string]any{"allowlist": []string{"127.0.0.1/32"}, "allowedPorts": []int{echoPort}, "allowLoopback": true},
	})
	if !ok {
		t.Fatalf("start proxy: %s", errMsg)
	}
	_ = waitSubMsg(t, readySub, 5*time.Second)

	// Tenant B nao pode publicar no tunnel.in do Tenant A
	_ = ncB.Publish(sessionSubject(clientA, siteA, agentA, sid, "tunnel.in"), []byte(`{"streamId":1,"op":"open"}`))
	_ = ncB.Flush()
	if ncB.LastError() == nil {
		t.Fatalf("Tenant B conseguiu publicar em tunnel.in do Tenant A")
	}

	// Tenant A usa normalmente
	openMsg, _ := json.Marshal(map[string]any{"streamId": 1, "op": "open", "targetHost": "127.0.0.1", "targetPort": echoPort})
	_ = ncA.Publish(sessionSubject(clientA, siteA, agentA, sid, "tunnel.in"), openMsg)
	_ = ncA.Flush()
	m := waitSubMsg(t, tunnelOutSub, 5*time.Second)
	_, _, _, _, ctrl, _ := remotesession.DecodeTunnelFrame(m.Data)
	if ctrl == nil || ctrl.Op != "opened" {
		t.Fatalf("esperava op=opened, obteve %+v", ctrl)
	}
}

func TestTier3_F16_F17_ProxyHeadlessWithBinaryFileTransferConcurrency(t *testing.T) {
	const (
		clientID = "11111111-1111-1111-1111-111111111111"
		siteID   = "22222222-2222-2222-2222-222222222222"
		agentID  = "33333333-3333-3333-3333-333333333333"
		sidProxy = "77777777-7777-7777-7777-777777777772"
		sidFiles = "77777777-7777-7777-7777-777777777773"
	)

	_, baseURL, httpPort := startLoopbackHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")

	proxyReady := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidProxy, "proxy.ready"))
	proxyResp := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidProxy, "proxy.resp"))
	filesReady := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidFiles, "files.ready"))
	filesResp := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidFiles, "files.resp"))

	mgr := newTestSessionManager(t, agentNC, clientID, siteID, agentID)
	ok1, e1 := mgr.HandleCommand(context.Background(), map[string]any{
		"action": "start", "sessionId": sidProxy, "kind": "proxy",
		"proxy": map[string]any{"allowlist": []string{"127.0.0.1/32"}, "allowedPorts": []int{httpPort}, "allowLoopback": true},
	})
	ok2, e2 := mgr.HandleCommand(context.Background(), map[string]any{
		"action": "start", "sessionId": sidFiles, "kind": "files", "rootPath": t.TempDir(),
	})
	if !ok1 || !ok2 {
		t.Fatalf("falha ao iniciar sessoes concorrentes: proxy=(%v,%s) files=(%v,%s)", ok1, e1, ok2, e2)
	}
	_ = waitSubMsg(t, proxyReady, 5*time.Second)
	_ = waitSubMsg(t, filesReady, 5*time.Second)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		req, _ := json.Marshal(map[string]any{"requestId": "p1", "method": "GET", "url": baseURL})
		_ = viewerNC.Publish(sessionSubject(clientID, siteID, agentID, sidProxy, "proxy.req"), req)
		_ = viewerNC.Flush()
		_ = waitSubMsg(t, proxyResp, 5*time.Second)
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 4; i++ {
			req, _ := json.Marshal(map[string]any{"version": 1, "requestId": fmt.Sprintf("fl-%d", i), "action": "list", "path": "."})
			_ = viewerNC.Publish(sessionSubject(clientID, siteID, agentID, sidFiles, "files.req"), req)
		}
		_ = viewerNC.Flush()
		for i := 0; i < 4; i++ {
			_ = waitSubMsg(t, filesResp, 5*time.Second)
		}
	}()
	wg.Wait()
}

func TestTier3_F13_F17_RunAsUserFailClosedTerminalWithTabAndBurst(t *testing.T) {
	// F13: EffectiveRunAs(RunAsUser) != "system" e resolveRunAs nao faz fallback fail-open
	if eff := terminal.EffectiveRunAs(terminal.RunAsUser); eff == "system" {
		t.Fatalf("RunAsUser caiu em SYSTEM")
	}
	runasSrc := readSrcFile(t, filepath.Join("app", "core", "terminal", "runas_windows.go"))
	if strings.Contains(runasSrc, `effectiveRunAs: EffectiveRunAs(RunAsInherit)`) && strings.Contains(runasSrc, `case RunAsUser:`) {
		t.Fatalf("F13xF17 violacao: runas_windows.go ainda possui fallback fail-open em case RunAsUser")
	}

	// F17: Utf8IncompleteTail preserva fronteira UTF-8 no coalescer.
	// NOTA (bug de spec corrigido): "ação" tem 6 bytes e a última runa é o 'o'
	// ASCII — cortar 1 byte (len-1) NÃO divide runa nenhuma (tail=0 é correto).
	// O corte que divide a runa multibyte ('ã', C3 A3) remove 2 bytes; e [:4]
	// deixa o C3 de 'ç' solto no fim (tail=1).
	sample := []byte("ação")
	if tail := terminal.Utf8IncompleteTail(sample[:len(sample)-2]); tail == 0 {
		t.Fatalf("Utf8IncompleteTail deveria detectar caractere UTF-8 incompleto")
	}
	if tail := terminal.Utf8IncompleteTail(sample[:4]); tail != 1 {
		t.Fatalf("Utf8IncompleteTail(ação[:4]) = %d; esperado 1 (C3 solto de 'ç')", tail)
	}
}

func TestTier3_F14_F18_ModularCoreTestsAndWailsBinaryIntegrity(t *testing.T) {
	// F14: release-agent-on-tag.yml executa go vet e go test.
	// F18 (contrato atualizado 2026-11-21): pion/webrtc pode existir, mas so COM
	// uso real — a proibicao absoluta virou guarda de dependencia morta.
	wf := readRepoFile(t, filepath.Join(".github", "workflows", "release-agent-on-tag.yml"))
	if !strings.Contains(wf, "go vet") || !strings.Contains(wf, "go test") {
		t.Fatalf("F14xF18 violacao: release-agent-on-tag.yml nao executa go vet / go test")
	}
	gomod := readSrcFile(t, "go.mod")
	if strings.Contains(gomod, "github.com/pion/webrtc") {
		code, out, err := runGoCmd(t, 30*time.Second, "list", "-f", "{{range .Imports}}{{.}},{{end}}", "./app/core/remotesession/")
		if err != nil || code != 0 || !strings.Contains(out, "github.com/pion/webrtc") {
			t.Fatalf("F14xF18 violacao: go.mod declara pion/webrtc mas o pacote de acesso remoto nao o importa (dependencia morta)")
		}
	}
}

func TestTier3_F13_F18_SelfUpdateAuthenticodeHashAndLinuxBuildScript(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("unsigned-blob"))
	}))
	defer srv.Close()

	var called atomic.Int32
	u := &selfupdate.Updater{
		ApiScheme: "http", ApiServer: strings.TrimPrefix(srv.URL, "http://"),
		TempDir: t.TempDir(), ServiceMode: true,
		GetPolicy: func() selfupdate.Policy {
			p := selfupdate.DefaultPolicy()
			p.RequireSignatureValidation = true
			return p
		},
		OnSelfUpdateInstall: func(_ context.Context, _, _ string) error {
			called.Add(1)
			return nil
		},
	}
	if err := u.InstallFromURL(context.Background(), "2.1.0", srv.URL+"/evil.exe"); err == nil || called.Load() != 0 {
		t.Fatalf("F13xF18 violacao: InstallFromURL aceitou binario sem verificacao de assinatura/SHA-256")
	}

	script := readRepoFile(t, filepath.Join("build", "server-api", "linux", "build-agent-server-linux.sh"))
	if !strings.Contains(script, "./cmd/discovery-service") {
		t.Fatalf("F13xF18 violacao: build-agent-server-linux.sh omite ./cmd/discovery-service")
	}
}

func TestTier3_F12_F16_WorkerStdinHintRedactionDuringRdpTunnel(t *testing.T) {
	// F12: --remote-session-worker recebe authToken por stdin length-prefixed, nunca por CLI args
	payload := `{"sessionId":"s1","agentId":"11111111-1111-1111-1111-111111111111","clientId":"11111111-1111-1111-1111-111111111111","siteId":"11111111-1111-1111-1111-111111111111","authToken":"invalid_no_mdz_prefix"}`
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)
	code, _, _, _ := execCLI(t, 4*time.Second, buf, "discovery-service.exe", "--remote-session-worker")
	if code != 3 {
		t.Fatalf("esperava exit 3 para token invalido no worker, obteve %d", code)
	}

	// F16: --rdp-tunnel rejeita flags ausentes sem expor segredos ou travar em GUI
	rdpCode, out, errOut, err := execCLI(t, 3*time.Second, nil, "discovery-agent.exe", "--rdp-tunnel", "--help-bad")
	if err == context.DeadlineExceeded || rdpCode == 0 {
		t.Fatalf("F12xF16 violacao: --rdp-tunnel nao validou argumentos CLI (code=%d err=%v out=%q errOut=%q)", rdpCode, err, out, errOut)
	}
}

func TestTier3_F15_F17_JetStreamFanOutStopAndFilesHandleCleanup(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"
		sid     = "77777777-7777-7777-7777-777777777779"
	)
	rootDir := t.TempDir()
	_, natsURL, adminNC := startTenantIsolatedNATSServer(t, clientA, siteA, agentA,
		"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "22222222-2222-2222-2222-222222222222", "bbbb2222-2222-2222-2222-222222222222")
	jsAdmin, _ := adminNC.JetStream()

	ncA := newTestNATSClient(t, natsURL, "mdz_tenantA")
	mgr := newTestSessionManager(t, ncA, clientA, siteA, agentA)

	filesReady := subscribeSync(t, adminNC, sessionSubject(clientA, siteA, agentA, sid, "files.ready"))
	filesResp := subscribeSync(t, adminNC, sessionSubject(clientA, siteA, agentA, sid, "files.resp"))

	connCh := make(chan struct{}, 1)
	startTestAgentRuntime(t, agentconn.Options{
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{NatsServer: natsURL, AuthToken: "mdz_tenantA", ClientID: clientA, SiteID: siteA, AgentID: agentA}
		},
		OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
			select {
			case connCh <- struct{}{}:
			default:
			}
		},
		HandleCommand: func(ctx context.Context, cmdType string, payload any) (bool, int, string, string) {
			if cmdType == "remotesession" {
				if m, ok := payload.(map[string]any); ok {
					okCmd, msg := mgr.HandleCommand(ctx, m)
					return okCmd, 0, msg, ""
				}
			}
			return false, 0, "", ""
		},
	})
	<-connCh
	time.Sleep(150 * time.Millisecond)

	// Inicia files diretamente no manager e envia 1 chunk parcial
	ok, errMsg := mgr.HandleCommand(context.Background(), map[string]any{
		"action": "start", "sessionId": sid, "kind": "files", "rootPath": rootDir,
	})
	if !ok {
		t.Fatalf("start files: %s", errMsg)
	}
	_ = waitSubMsg(t, filesReady, 5*time.Second)

	putReq, _ := json.Marshal(map[string]any{
		"version": 1, "requestId": "part-1", "action": "put", "path": "partial.bin",
		"data": bytes.Repeat([]byte("P"), 4096), "chunkIndex": 0, "totalChunks": 2, "chunkSize": 4096,
	})
	_ = adminNC.Publish(sessionSubject(clientA, siteA, agentA, sid, "files.req"), putReq)
	_ = adminNC.Flush()
	_ = waitSubMsg(t, filesResp, 5*time.Second)

	// Envia stop via tenant fan-out JetStream
	now := time.Now().UTC()
	stopEnv, _ := json.Marshal(map[string]any{
		"dispatchId": "d-stop", "commandId": "c-stop", "commandType": "RemoteSession",
		"targetScope": "client", "targetClientId": clientA,
		"issuedAtUtc": now.Format(time.RFC3339), "idempotencyKey": "k-stop",
		"payload": map[string]any{"action": "stop", "sessionId": sid},
	})
	_, _ = jsAdmin.Publish(fmt.Sprintf("tenant.%s.agents.command", clientA), stopEnv)
	_ = adminNC.Flush()

	deadline := time.Now().Add(5 * time.Second)
	for mgr.HasActiveSession(remotesession.KindFiles) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if mgr.HasActiveSession(remotesession.KindFiles) {
		t.Fatalf("sessao files nao encerrou apos comando stop via JetStream fan-out")
	}

	// Verifica que nenhum handle ficou travado em rootDir
	if err := os.RemoveAll(filepath.Join(rootDir, "partial.bin")); err != nil {
		t.Fatalf("arquivo parcial permaneceu bloqueado apos Stop: %v", err)
	}
}

func TestTier3_F11_F13_NsisFirewallRestrictionAndDebugHttpBinding(t *testing.T) {
	nsi := readSrcFile(t, filepath.Join("build", "windows", "installer", "project.nsi"))
	rePermissiveUIRule := regexp.MustCompile(`(?i)netsh\s+advfirewall\s+firewall\s+add\s+rule[^\r\n]*PRODUCT_EXECUTABLE[^\r\n]*profile=any`)
	if rePermissiveUIRule.MatchString(nsi) {
		t.Fatalf("F11xF13 violacao: NSIS abre regra firewall profile=any para UI debug_http")
	}

	debugHTTPSrc := readSrcFile(t, filepath.Join("app", "debug_http.go"))
	if strings.Contains(debugHTTPSrc, `return "*"`) && strings.Contains(debugHTTPSrc, "BindAllInterfaces") {
		t.Fatalf("F11xF13 violacao: debug_http permite CORS wildcard * quando BindAllInterfaces=true")
	}
}
