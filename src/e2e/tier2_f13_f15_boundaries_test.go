package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/core/agentconn"
	"discovery/app/core/automation"
	"discovery/app/core/selfupdate"
	"discovery/app/core/terminal"
	"discovery/app/debughttp"
)

// ── F13 Boundaries (5 tests) ────────────────────────────────────────────────

func TestF13_T2_DebugHTTP_OriginAndBrokerQueueBoundaries(t *testing.T) {
	// 1. Verifica que setSSECORSOrigin nao ecoa "null" (iframe sandbox CSRF) e valida origins estritamente
	debugHTTPSrc := readSrcFile(t, filepath.Join("app", "debug_http.go"))
	if strings.Contains(debugHTTPSrc, `origin == "null"`) {
		t.Fatalf(`F13 violacao: setSSECORSOrigin em debug_http.go ainda permite Origin: "null" (CSRF via sandboxed iframe/file://)`)
	}

	// 2. ChatEventBroker overflow boundaries: pollMax=512 (522 -> 10 overwritten) & slow subscriber drop
	broker := debughttp.NewChatEventBroker()
	slowCh := broker.Subscribe()
	defer broker.Unsubscribe(slowCh)

	for i := 0; i < 522; i++ {
		broker.Publish("chat:token", fmt.Sprintf(`{"seq":%d}`, i))
	}
	subDropped, pollOverwritten := broker.DrainDrops()
	if pollOverwritten != 10 {
		t.Fatalf("pollOverwritten esperado=10, obteve=%d", pollOverwritten)
	}
	if subDropped == 0 {
		t.Fatalf("subDropped esperado > 0 para subscriber lento com 522 eventos")
	}
	drained := broker.DrainPollBuffer()
	if len(drained) != 512 {
		t.Fatalf("DrainPollBuffer esperado=512, obteve=%d", len(drained))
	}
}

func TestF13_T2_TerminalRunAs_BoundaryInputs(t *testing.T) {
	cases := []struct {
		in        string
		wantParse terminal.RunAs
		wantValid bool
	}{
		{"  USER  ", terminal.RunAsUser, true},
		{"System", terminal.RunAsSystem, true},
		{"", terminal.RunAsInherit, true},
		{"inherit", terminal.RunAsInherit, true},
		{"administrator", terminal.RunAsInherit, false},
		{"system;whoami", terminal.RunAsInherit, false},
		{"\x00", terminal.RunAsInherit, false},
	}
	for _, tc := range cases {
		if got := terminal.ParseRunAs(tc.in); got != tc.wantParse {
			t.Fatalf("ParseRunAs(%q) = %q, want %q", tc.in, got, tc.wantParse)
		}
		if got := terminal.ValidRunAs(tc.in); got != tc.wantValid {
			t.Fatalf("ValidRunAs(%q) = %v, want %v", tc.in, got, tc.wantValid)
		}
		_ = terminal.EffectiveRunAs(tc.wantParse)
	}
}

func TestF13_T2_Automation_CustomFieldsBoundaryPayloads(t *testing.T) {
	largeVal := strings.Repeat("X", 512)
	metaChars := `$(calc.exe) | & ; " ' ` + "`" + "\r\n"
	var gotNote atomic.Value
	resultDone := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/automation/policy-sync"):
			scriptBody := "Write-Output $env:MDZ_CUSTOM_FIELDS\n"
			sum := sha256.Sum256([]byte(scriptBody))
			_ = json.NewEncoder(w).Encode(automation.PolicySyncResponse{
				PolicyFingerprint: "fp-f13-t2",
				TaskCount:         1,
				Tasks: []automation.AutomationTask{
					{
						CommandID:             "cmd-f13-t2",
						TaskID:                "task-f13-t2",
						Name:                  "F13 Boundary CustomFields",
						ActionType:            automation.ActionRunScript,
						ScopeType:             automation.ApprovalScopeAgent,
						NotificationMode:      automation.NotificationModeSilent,
						WriteOutputToNote:     true,
						TriggerOnAgentCheckIn: true,
						Script: &automation.AutomationScript{
							ScriptID:          "script-f13-t2",
							Name:              "boundary",
							Version:           "1",
							ScriptType:        automation.ScriptPowerShell,
							ContentHashSHA256: hex.EncodeToString(sum[:]),
							Content:           scriptBody,
						},
					},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/custom-fields/runtime"):
			_ = json.NewEncoder(w).Encode([]automation.RuntimeCustomField{
				{DefinitionID: "d1", Name: "null_field", ScopeType: automation.FieldScopeAgent, ValueJson: json.RawMessage(`null`)},
				{DefinitionID: "d2", Name: "large_field", ScopeType: automation.FieldScopeAgent, ValueJson: mustMarshalJSON(t, largeVal)},
				{DefinitionID: "d3", Name: "meta_field", ScopeType: automation.FieldScopeAgent, ValueJson: mustMarshalJSON(t, metaChars)},
			})
		case strings.HasSuffix(r.URL.Path, "/notes"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotNote.Store(fmt.Sprint(body["content"]))
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/result"):
			select {
			case resultDone <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	svc := automation.NewService(func() automation.RuntimeConfig {
		return automation.RuntimeConfig{BaseURL: srv.URL, Token: "mdz_boundary", AgentID: "11111111-1111-1111-1111-111111111111"}
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := svc.RefreshPolicy(ctx, true); err != nil {
		t.Fatalf("RefreshPolicy: %v", err)
	}
	svc.TriggerAgentCheckInTasks(ctx)

	select {
	case <-resultDone:
	case <-ctx.Done():
		t.Fatalf("timeout aguardando execucao boundary")
	}
	note, _ := gotNote.Load().(string)
	if !strings.Contains(note, "large_field") || !strings.Contains(note, "meta_field") {
		t.Fatalf("MDZ_CUSTOM_FIELDS boundary ausente do output: %q", note)
	}
}

func TestF13_T2_SelfUpdate_InstallFromURL_BoundaryInputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status/404":
			w.WriteHeader(http.StatusNotFound)
		case "/status/500":
			w.WriteHeader(http.StatusInternalServerError)
		case "/api/v1/download/agent":
			_, _ = w.Write([]byte{}) // 0 bytes
		case "/api/v1/download/agent/sha256":
			_, _ = w.Write([]byte("abc123")) // malformado (< 64 hex)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	var launches atomic.Int32
	u := &selfupdate.Updater{
		ApiScheme:   "http",
		ApiServer:   strings.TrimPrefix(srv.URL, "http://"),
		GetToken:    func() string { return "mdz_test" },
		GetAgentID:  func() string { return "11111111-1111-1111-1111-111111111111" },
		TempDir:     t.TempDir(),
		ServiceMode: true,
		GetPolicy: func() selfupdate.Policy {
			p := selfupdate.DefaultPolicy()
			p.RequireSignatureValidation = true
			return p
		},
		OnSelfUpdateInstall: func(_ context.Context, _ string, _ string) error {
			launches.Add(1)
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, badURL := range []string{"", "   ", srv.URL + "/status/404", srv.URL + "/status/500", "/api/v1/download/agent"} {
		if err := u.InstallFromURL(ctx, "2.0.0", badURL); err == nil {
			t.Fatalf("InstallFromURL(%q) deveria falhar", badURL)
		}
	}
	if launches.Load() != 0 || u.LaunchOKCount() != 0 {
		t.Fatalf("nenhum launch deveria ter ocorrido em inputs invalidos")
	}
}

func TestF13_T2_LinuxScript_LineEndingsAndVersionRegexBoundaries(t *testing.T) {
	scriptPath := filepath.Join(repoRoot(t), "build", "server-api", "linux", "build-agent-server-linux.sh")
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", scriptPath, err)
	}
	if strings.Contains(string(raw), "\r\n") {
		t.Fatalf("build-agent-server-linux.sh contem CRLF (\\r\\n) que quebra execucao bash no Linux")
	}

	// Verifica que config.yml contem info.version parseavel
	cfg := readSrcFile(t, filepath.Join("build", "config.yml"))
	reVer := regexp.MustCompile(`(?m)^\s*version:\s*["']?([0-9]+\.[0-9]+\.[0-9]+[^"'\s#]*)["']?`)
	if !reVer.MatchString(cfg) {
		t.Fatalf("src/build/config.yml nao possui campo version valido")
	}
}

// ── F15 Boundaries (5 tests) ────────────────────────────────────────────────

func TestF15_T2_InvalidCanonicalContextAndWildcardInjection(t *testing.T) {
	_, natsURL := startEmbeddedNATS(t, true, nil)

	badConfigs := []agentconn.Config{
		{NatsServer: natsURL, AuthToken: "mdz_x", ClientID: "client.A", SiteID: "11111111-1111-1111-1111-111111111111", AgentID: "aaaa1111-1111-1111-1111-111111111111"},
		{NatsServer: natsURL, AuthToken: "mdz_x", ClientID: "client*", SiteID: "11111111-1111-1111-1111-111111111111", AgentID: "aaaa1111-1111-1111-1111-111111111111"},
		{NatsServer: natsURL, AuthToken: "mdz_x", ClientID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", SiteID: "site>", AgentID: "aaaa1111-1111-1111-1111-111111111111"},
		{NatsServer: natsURL, AuthToken: "mdz_x", ClientID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", SiteID: "11111111-1111-1111-1111-111111111111", AgentID: "not-a-guid"},
		{NatsServer: natsURL, AuthToken: "mdz_x", ClientID: "", SiteID: "", AgentID: "aaaa1111-1111-1111-1111-111111111111"},
	}

	for i, cfg := range badConfigs {
		var connected atomic.Bool
		rt, cancel := startTestAgentRuntime(t, agentconn.Options{
			LoadConfig: func() agentconn.Config { return cfg },
			OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
				connected.Store(true)
			},
		})
		time.Sleep(150 * time.Millisecond)
		cancel()
		if connected.Load() || rt.GetStatus().Connected {
			t.Fatalf("config invalida %d (%+v) conectou indevidamente", i, cfg)
		}
	}
}

func TestF15_T2_FanoutCommand_ExpirationAndMalformedEnvelopeBoundaries(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"
	)

	_, natsURL, adminNC := startTenantIsolatedNATSServer(t, clientA, siteA, agentA,
		"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "22222222-2222-2222-2222-222222222222", "bbbb2222-2222-2222-2222-222222222222")
	jsAdmin, _ := adminNC.JetStream()

	connCh := make(chan struct{}, 1)
	var cmds []string
	var mu = &atomic.Int32{}
	startTestAgentRuntime(t, agentconn.Options{
		Logf: t.Logf,
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{NatsServer: natsURL, AuthToken: "mdz_tenantA", ClientID: clientA, SiteID: siteA, AgentID: agentA}
		},
		OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
			select {
			case connCh <- struct{}{}:
			default:
			}
		},
		HandleCommand: func(_ context.Context, cmdType string, _ any) (bool, int, string, string) {
			mu.Add(1)
			cmds = append(cmds, cmdType)
			return true, 0, "ok", ""
		},
	})
	<-connCh
	time.Sleep(150 * time.Millisecond)

	now := time.Now().UTC()
	subj := fmt.Sprintf("tenant.%s.agents.command", clientA)

	invalidEnvelopes := []map[string]any{
		// Expirado 2s no passado
		{"dispatchId": "d-exp", "commandId": "c-exp", "commandType": "ShouldSkipExpired", "targetScope": "client", "targetClientId": clientA, "issuedAtUtc": now.Add(-10 * time.Second).Format(time.RFC3339), "expiresAtUtc": now.Add(-2 * time.Second).Format(time.RFC3339), "idempotencyKey": "k1"},
		// Sem dispatchId
		{"commandId": "c-nodisp", "commandType": "ShouldSkipNoDisp", "targetScope": "client", "targetClientId": clientA, "issuedAtUtc": now.Format(time.RFC3339), "idempotencyKey": "k2"},
		// Sem idempotencyKey
		{"dispatchId": "d-nokey", "commandId": "c-nokey", "commandType": "ShouldSkipNoKey", "targetScope": "client", "targetClientId": clientA, "issuedAtUtc": now.Format(time.RFC3339)},
		// TargetScope incoerente ("global" em subject client)
		{"dispatchId": "d-scope", "commandId": "c-scope", "commandType": "ShouldSkipScope", "targetScope": "global", "targetClientId": clientA, "issuedAtUtc": now.Format(time.RFC3339), "idempotencyKey": "k3"},
	}
	for _, env := range invalidEnvelopes {
		b, _ := json.Marshal(env)
		_, _ = jsAdmin.Publish(subj, b)
	}
	// Envelope valido com expiracao futura
	validEnv, _ := json.Marshal(map[string]any{
		"dispatchId": "d-ok", "commandId": "c-ok", "commandType": "ValidFutureCmd",
		"targetScope": "client", "targetClientId": clientA,
		"issuedAtUtc": now.Format(time.RFC3339), "expiresAtUtc": now.Add(1 * time.Minute).Format(time.RFC3339), "idempotencyKey": "k-ok",
	})
	_, _ = jsAdmin.Publish(subj, validEnv)
	_ = adminNC.Flush()

	deadline := time.Now().Add(4 * time.Second)
	for mu.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if mu.Load() != 1 {
		t.Fatalf("esperava exatamente 1 comando valido executado, obteve %d", mu.Load())
	}
}

func TestF15_T2_FanoutDedupe_RepeatedDeliveryAndReplay(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"
	)

	_, natsURL, adminNC := startTenantIsolatedNATSServer(t, clientA, siteA, agentA,
		"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "22222222-2222-2222-2222-222222222222", "bbbb2222-2222-2222-2222-222222222222")
	jsAdmin, _ := adminNC.JetStream()
	resultSub := subscribeSync(t, adminNC, fmt.Sprintf("tenant.%s.site.%s.agent.%s.result", clientA, siteA, agentA))

	connCh := make(chan struct{}, 1)
	var execCount atomic.Int32
	startTestAgentRuntime(t, agentconn.Options{
		Logf: t.Logf,
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{NatsServer: natsURL, AuthToken: "mdz_tenantA", ClientID: clientA, SiteID: siteA, AgentID: agentA}
		},
		OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
			select {
			case connCh <- struct{}{}:
			default:
			}
		},
		HandleCommand: func(_ context.Context, _ string, _ any) (bool, int, string, string) {
			execCount.Add(1)
			return true, 0, "once-only", ""
		},
	})
	<-connCh
	time.Sleep(150 * time.Millisecond)

	now := time.Now().UTC()
	dupEnv, _ := json.Marshal(map[string]any{
		"dispatchId": "disp-dup-1", "commandId": "cmd-dup-1", "commandType": "DedupeCmd",
		"targetScope": "client", "targetClientId": clientA,
		"issuedAtUtc": now.Format(time.RFC3339), "expiresAtUtc": now.Add(5 * time.Minute).Format(time.RFC3339), "idempotencyKey": "idem-dup-1",
	})
	subj := fmt.Sprintf("tenant.%s.agents.command", clientA)
	_, _ = jsAdmin.Publish(subj, dupEnv)
	_, _ = jsAdmin.Publish(subj, dupEnv)
	_ = adminNC.Flush()

	_ = waitSubMsg(t, resultSub, 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	if execCount.Load() != 1 {
		t.Fatalf("comando duplicado executou %d vezes (esperado 1)", execCount.Load())
	}
}

func TestF15_T2_SupportChat_HandlerErrorTriggersNakRedelivery(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"
	)

	_, natsURL, adminNC := startTenantIsolatedNATSServer(t, clientA, siteA, agentA,
		"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "22222222-2222-2222-2222-222222222222", "bbbb2222-2222-2222-2222-222222222222")
	jsAdmin, _ := adminNC.JetStream()

	var attempts atomic.Int32
	deliveredTwice := make(chan struct{}, 1)
	connCh := make(chan struct{}, 1)
	startTestAgentRuntime(t, agentconn.Options{
		Logf: t.Logf,
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{NatsServer: natsURL, AuthToken: "mdz_tenantA", ClientID: clientA, SiteID: siteA, AgentID: agentA}
		},
		OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
			select {
			case connCh <- struct{}{}:
			default:
			}
		},
		HandleSupportChatEnvelope: func(_ context.Context, _ []byte) error {
			n := attempts.Add(1)
			if n == 1 {
				return errors.New("transient failure")
			}
			select {
			case deliveredTwice <- struct{}{}:
			default:
			}
			return nil
		},
	})
	<-connCh
	time.Sleep(150 * time.Millisecond)

	chatSubj := fmt.Sprintf("tenant.%s.site.%s.agent.%s.supportchat.server", clientA, siteA, agentA)
	if _, err := jsAdmin.Publish(chatSubj, []byte(`{"body":"retry-me"}`)); err != nil {
		t.Fatalf("Publish chat: %v", err)
	}
	select {
	case <-deliveredTwice:
	case <-time.After(6 * time.Second):
		t.Fatalf("timeout aguardando redelivery apos Nak (attempts=%d)", attempts.Load())
	}
	if attempts.Load() < 2 {
		t.Fatalf("esperava >= 2 tentativas apos Nak, obteve %d", attempts.Load())
	}
}

func TestF15_T2_CoreFallback_WhenJetStreamStreamAbsent(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"
	)

	// JetStream desabilitado no broker: Runtime deve cair graciosamente para Core NATS
	_, natsURL := startEmbeddedNATS(t, false, nil)
	adminNC := newTestNATSClient(t, natsURL, "")

	connCh := make(chan struct{}, 1)
	cmdCh := make(chan string, 1)
	startTestAgentRuntime(t, agentconn.Options{
		Logf: t.Logf,
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{NatsServer: natsURL, AuthToken: "mdz_core", ClientID: clientA, SiteID: siteA, AgentID: agentA}
		},
		OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
			select {
			case connCh <- struct{}{}:
			default:
			}
		},
		HandleCommand: func(_ context.Context, cmdType string, _ any) (bool, int, string, string) {
			cmdCh <- cmdType
			return true, 0, "ok", ""
		},
	})
	<-connCh
	time.Sleep(150 * time.Millisecond)

	now := time.Now().UTC()
	env, _ := json.Marshal(map[string]any{
		"dispatchId": "d-core", "commandId": "c-core", "commandType": "CoreFallbackCmd",
		"targetScope": "client", "targetClientId": clientA,
		"issuedAtUtc": now.Format(time.RFC3339), "idempotencyKey": "k-core",
	})
	_ = adminNC.Publish(fmt.Sprintf("tenant.%s.agents.command", clientA), env)
	_ = adminNC.Flush()

	select {
	case got := <-cmdCh:
		if got != "corefallbackcmd" {
			t.Fatalf("comando inesperado: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout aguardando entrega em fallback Core NATS")
	}
}
