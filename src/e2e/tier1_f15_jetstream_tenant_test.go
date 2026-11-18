package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"discovery/app/core/agentconn"
)

func startTenantIsolatedNATSServer(t testing.TB, clientA, siteA, agentA, clientB, siteB, agentB string) (*server.Server, string, *nats.Conn) {
	t.Helper()
	clientAN := stripHyphens(clientA)
	clientBN := stripHyphens(clientB)
	siteAN := stripHyphens(siteA)
	siteBN := stripHyphens(siteB)
	agentAN := stripHyphens(agentA)
	agentBN := stripHyphens(agentB)

	buildTenantPerms := func(c, cN, s, sN, a, aN string) *server.Permissions {
		fanoutStream := "DISCOVERY_FANOUT_COMMANDS_" + cN
		chatStream := "DISCOVERY_SUPPORT_CHAT_" + cN
		return &server.Permissions{
			Publish: &server.SubjectPermission{
				Allow: []string{
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.heartbeat", c, s, a),
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.result", c, s, a),
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.hardware", c, s, a),
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote-debug.>", c, s, a),
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.supportchat.agent", c, s, a),
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote.session.>", cN, sN, aN),
					"_INBOX.>",
					"$JS.API.STREAM.NAMES",
					"$JS.API.STREAM.INFO." + fanoutStream,
					"$JS.API.STREAM.INFO." + chatStream,
					"$JS.API.STREAM.INFO.DISCOVERY_FANOUT_COMMANDS_GLOBAL",
					"$JS.API.CONSUMER.*." + fanoutStream + ".>",
					"$JS.API.CONSUMER.*." + chatStream + ".>",
					"$JS.API.CONSUMER.*.DISCOVERY_FANOUT_COMMANDS_GLOBAL.>",
					"$JS.API.CONSUMER.DURABLE.CREATE." + fanoutStream + ".>",
					"$JS.API.CONSUMER.DURABLE.CREATE." + chatStream + ".>",
					"$JS.API.CONSUMER.DURABLE.CREATE.DISCOVERY_FANOUT_COMMANDS_GLOBAL.>",
					"$JS.API.CONSUMER.MSG.NEXT." + fanoutStream + ".>",
					"$JS.API.CONSUMER.MSG.NEXT." + chatStream + ".>",
					"$JS.API.CONSUMER.MSG.NEXT.DISCOVERY_FANOUT_COMMANDS_GLOBAL.>",
					"$JS.ACK." + fanoutStream + ".>",
					"$JS.ACK." + chatStream + ".>",
					"$JS.ACK.DISCOVERY_FANOUT_COMMANDS_GLOBAL.>",
				},
			},
			Subscribe: &server.SubjectPermission{
				Allow: []string{
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.command", c, s, a),
					fmt.Sprintf("tenant.%s.site.%s.agents.command", c, s),
					fmt.Sprintf("tenant.%s.agents.command", c),
					"tenant.global.agents.command",
					"tenant.global.pong",
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.sync.ping", c, s, a),
					fmt.Sprintf("tenant.%s.site.%s.p2p.discovery", c, s),
					fmt.Sprintf("tenant.%s.p2p.events", c),
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote-debug.control", c, s, a),
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.supportchat.server", c, s, a),
					fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote.session.>", cN, sN, aN),
					"_INBOX.>",
				},
			},
		}
	}

	auth := tenantAuthFunc(func(c server.ClientAuthentication) bool {
		tok := strings.TrimSpace(c.GetOpts().Token)
		switch tok {
		case "mdz_api_admin":
			c.RegisterUser(&server.User{
				Permissions: &server.Permissions{
					Publish:   &server.SubjectPermission{Allow: []string{">"}},
					Subscribe: &server.SubjectPermission{Allow: []string{">"}},
				},
			})
			return true
		case "mdz_tenantA":
			c.RegisterUser(&server.User{Permissions: buildTenantPerms(clientA, clientAN, siteA, siteAN, agentA, agentAN)})
			return true
		case "mdz_tenantB":
			c.RegisterUser(&server.User{Permissions: buildTenantPerms(clientB, clientBN, siteB, siteBN, agentB, agentBN)})
			return true
		default:
			return false
		}
	})

	s, url := startEmbeddedNATS(t, true, auth)
	adminNC := newTestNATSClient(t, url, "mdz_api_admin")
	js, err := adminNC.JetStream()
	if err != nil {
		t.Fatalf("admin JetStream: %v", err)
	}

	ensureStream := func(name string, subjects []string) {
		if _, err := js.AddStream(&nats.StreamConfig{
			Name:     name,
			Subjects: subjects,
			Storage:  nats.MemoryStorage,
		}); err != nil {
			t.Fatalf("AddStream(%s): %v", name, err)
		}
	}

	ensureStream("DISCOVERY_FANOUT_COMMANDS_"+clientAN, []string{
		fmt.Sprintf("tenant.%s.agents.command", clientA),
		fmt.Sprintf("tenant.%s.site.*.agents.command", clientA),
	})
	ensureStream("DISCOVERY_FANOUT_COMMANDS_"+clientBN, []string{
		fmt.Sprintf("tenant.%s.agents.command", clientB),
		fmt.Sprintf("tenant.%s.site.*.agents.command", clientB),
	})
	ensureStream("DISCOVERY_SUPPORT_CHAT_"+clientAN, []string{
		fmt.Sprintf("tenant.%s.site.*.agent.*.supportchat.server", clientA),
	})
	ensureStream("DISCOVERY_SUPPORT_CHAT_"+clientBN, []string{
		fmt.Sprintf("tenant.%s.site.*.agent.*.supportchat.server", clientB),
	})
	ensureStream("DISCOVERY_FANOUT_COMMANDS_GLOBAL", []string{
		"tenant.global.agents.command",
	})

	return s, url, adminNC
}

func startTestAgentRuntime(t testing.TB, opts agentconn.Options) (*agentconn.Runtime, context.CancelFunc) {
	t.Helper()
	rt := agentconn.NewRuntime(opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return rt, cancel
}

func TestF15_T1_AgentConn_ClientAndSiteFanoutDelivery_IsolatedPerTenant(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"

		clientB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		siteB   = "22222222-2222-2222-2222-222222222222"
		agentB  = "bbbb2222-2222-2222-2222-222222222222"
	)

	_, natsURL, adminNC := startTenantIsolatedNATSServer(t, clientA, siteA, agentA, clientB, siteB, agentB)
	jsAdmin, err := adminNC.JetStream()
	if err != nil {
		t.Fatalf("admin JetStream: %v", err)
	}

	resultSubA := subscribeSync(t, adminNC, fmt.Sprintf("tenant.%s.site.%s.agent.%s.result", clientA, siteA, agentA))
	resultSubB := subscribeSync(t, adminNC, fmt.Sprintf("tenant.%s.site.%s.agent.%s.result", clientB, siteB, agentB))

	connA := make(chan struct{}, 1)
	connB := make(chan struct{}, 1)
	var cmdsA, cmdsB []string
	var mu sync.Mutex

	startTestAgentRuntime(t, agentconn.Options{
		Logf: t.Logf,
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{
				NatsServer: natsURL,
				AuthToken:  "mdz_tenantA",
				ClientID:   clientA,
				SiteID:     siteA,
				AgentID:    agentA,
			}
		},
		OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
			select {
			case connA <- struct{}{}:
			default:
			}
		},
		HandleCommand: func(_ context.Context, cmdType string, _ any) (bool, int, string, string) {
			mu.Lock()
			cmdsA = append(cmdsA, cmdType)
			mu.Unlock()
			return true, 0, "ok-A", ""
		},
	})

	startTestAgentRuntime(t, agentconn.Options{
		Logf: t.Logf,
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{
				NatsServer: natsURL,
				AuthToken:  "mdz_tenantB",
				ClientID:   clientB,
				SiteID:     siteB,
				AgentID:    agentB,
			}
		},
		OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
			select {
			case connB <- struct{}{}:
			default:
			}
		},
		HandleCommand: func(_ context.Context, cmdType string, _ any) (bool, int, string, string) {
			mu.Lock()
			cmdsB = append(cmdsB, cmdType)
			mu.Unlock()
			return true, 0, "ok-B", ""
		},
	})

	select {
	case <-connA:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout aguardando Tenant A conectar no NATS")
	}
	select {
	case <-connB:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout aguardando Tenant B conectar no NATS")
	}
	// Aguardar subscricoes de fan-out ficarem ativas
	time.Sleep(150 * time.Millisecond)

	now := time.Now().UTC()
	envA, _ := json.Marshal(map[string]any{
		"dispatchId":     "disp-A-1",
		"commandId":      "cmd-A-1",
		"commandType":    "CustomCommandA",
		"targetScope":    "client",
		"targetClientId": clientA,
		"issuedAtUtc":    now.Format(time.RFC3339),
		"expiresAtUtc":   now.Add(5 * time.Minute).Format(time.RFC3339),
		"idempotencyKey": "idem-A-1",
	})
	envB, _ := json.Marshal(map[string]any{
		"dispatchId":     "disp-B-1",
		"commandId":      "cmd-B-1",
		"commandType":    "CustomCommandB",
		"targetScope":    "site",
		"targetClientId": clientB,
		"targetSiteId":   siteB,
		"issuedAtUtc":    now.Format(time.RFC3339),
		"expiresAtUtc":   now.Add(5 * time.Minute).Format(time.RFC3339),
		"idempotencyKey": "idem-B-1",
	})

	if _, err := jsAdmin.Publish(fmt.Sprintf("tenant.%s.agents.command", clientA), envA); err != nil {
		t.Fatalf("Publish fanout A: %v", err)
	}
	if _, err := jsAdmin.Publish(fmt.Sprintf("tenant.%s.site.%s.agents.command", clientB, siteB), envB); err != nil {
		t.Fatalf("Publish fanout B: %v", err)
	}

	resA := waitSubMsg(t, resultSubA, 5*time.Second)
	resB := waitSubMsg(t, resultSubB, 5*time.Second)
	if !strings.Contains(string(resA.Data), "ok-A") {
		t.Fatalf("resultado A inesperado: %s", string(resA.Data))
	}
	if !strings.Contains(string(resB.Data), "ok-B") {
		t.Fatalf("resultado B inesperado: %s", string(resB.Data))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(cmdsA) != 1 || cmdsA[0] != "customcommanda" {
		t.Fatalf("comandos Tenant A inesperados: %v", cmdsA)
	}
	if len(cmdsB) != 1 || cmdsB[0] != "customcommandb" {
		t.Fatalf("comandos Tenant B inesperados: %v", cmdsB)
	}
}

func TestF15_T1_CrossTenant_JetStreamConsumerCreationDenied(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"

		clientB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		siteB   = "22222222-2222-2222-2222-222222222222"
		agentB  = "bbbb2222-2222-2222-2222-222222222222"
	)

	_, natsURL, adminNC := startTenantIsolatedNATSServer(t, clientA, siteA, agentA, clientB, siteB, agentB)
	ncA := newTestNATSClient(t, natsURL, "mdz_tenantA")
	jsA, err := ncA.JetStream()
	if err != nil {
		t.Fatalf("Tenant A JetStream: %v", err)
	}

	// 1. Subscricao core cross-tenant deve ser bloqueada ou nao entregar mensagens de Tenant B
	crossSub, err := ncA.SubscribeSync(fmt.Sprintf("tenant.%s.agents.command", clientB))
	if err == nil {
		_ = ncA.Flush()
		_ = adminNC.Publish(fmt.Sprintf("tenant.%s.agents.command", clientB), []byte(`{"secret":"tenantB"}`))
		_ = adminNC.Flush()
		assertNoSubMsg(t, crossSub, 300*time.Millisecond)
		_ = crossSub.Unsubscribe()
	}

	// 2. Criacao de consumer JetStream no stream do Tenant B deve ser negada pelas ACLs do broker
	streamB := "DISCOVERY_FANOUT_COMMANDS_" + stripHyphens(clientB)
	_, addErr := jsA.AddConsumer(streamB, &nats.ConsumerConfig{
		Durable:   "evil-cross-tenant",
		AckPolicy: nats.AckExplicitPolicy,
	})
	if addErr == nil {
		t.Fatalf("F15 violacao CRÍTICA: Tenant A conseguiu criar consumer JetStream no stream %s do Tenant B", streamB)
	}

	// 3. Probe direto via $JS.API.CONSUMER.CREATE do stream B deve falhar por permissao/timeout
	_, reqErr := ncA.Request("$JS.API.CONSUMER.CREATE."+streamB+".evil", []byte(`{}`), 500*time.Millisecond)
	if reqErr == nil {
		t.Fatalf("F15 violacao CRÍTICA: Tenant A obteve resposta em $JS.API.CONSUMER.CREATE.%s", streamB)
	}
}

func TestF15_T1_CrossTenant_SupportChat_StreamAndSubjectIsolation(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"

		clientB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		siteB   = "22222222-2222-2222-2222-222222222222"
		agentB  = "bbbb2222-2222-2222-2222-222222222222"
	)

	_, natsURL, adminNC := startTenantIsolatedNATSServer(t, clientA, siteA, agentA, clientB, siteB, agentB)
	jsAdmin, err := adminNC.JetStream()
	if err != nil {
		t.Fatalf("admin JetStream: %v", err)
	}

	// Publica mensagem ANTES do agente A conectar (testando replay duravel por tenant)
	chatSubjA := fmt.Sprintf("tenant.%s.site.%s.agent.%s.supportchat.server", clientA, siteA, agentA)
	chatSubjB := fmt.Sprintf("tenant.%s.site.%s.agent.%s.supportchat.server", clientB, siteB, agentB)
	if _, err := jsAdmin.Publish(chatSubjA, []byte(`{"msg":"hello-A-offline"}`)); err != nil {
		t.Fatalf("Publish chat A: %v", err)
	}
	if _, err := jsAdmin.Publish(chatSubjB, []byte(`{"msg":"secret-B"}`)); err != nil {
		t.Fatalf("Publish chat B: %v", err)
	}

	gotA := make(chan string, 4)
	startTestAgentRuntime(t, agentconn.Options{
		Logf: t.Logf,
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{
				NatsServer: natsURL,
				AuthToken:  "mdz_tenantA",
				ClientID:   clientA,
				SiteID:     siteA,
				AgentID:    agentA,
			}
		},
		HandleSupportChatEnvelope: func(_ context.Context, data []byte) error {
			gotA <- string(data)
			return nil
		},
	})

	select {
	case msg := <-gotA:
		if !strings.Contains(msg, "hello-A-offline") {
			t.Fatalf("chat replay A inesperado: %s", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout aguardando replay JetStream do supportchat do Tenant A")
	}

	// Tenant A nao pode criar consumer em DISCOVERY_SUPPORT_CHAT_{clientBN}
	ncA := newTestNATSClient(t, natsURL, "mdz_tenantA")
	jsA, _ := ncA.JetStream()
	if _, err := jsA.AddConsumer("DISCOVERY_SUPPORT_CHAT_"+stripHyphens(clientB), &nats.ConsumerConfig{
		Durable:   "steal-chat",
		AckPolicy: nats.AckExplicitPolicy,
	}); err == nil {
		t.Fatalf("F15 violacao: Tenant A criou consumer no stream de supportchat do Tenant B")
	}

	// Garante que nenhuma mensagem do Tenant B vazou para Tenant A
	select {
	case leaked := <-gotA:
		t.Fatalf("vazamento cross-tenant no supportchat de A: %s", leaked)
	case <-time.After(250 * time.Millisecond):
	}
}

func TestF15_T1_AgentConn_SpoofedEnvelopeTargetClientOrSiteRejected(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"
		clientB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		siteB   = "22222222-2222-2222-2222-222222222222"
	)

	_, natsURL, adminNC := startTenantIsolatedNATSServer(t, clientA, siteA, agentA, clientB, siteB, "bbbb2222-2222-2222-2222-222222222222")
	jsAdmin, _ := adminNC.JetStream()

	connA := make(chan struct{}, 1)
	var executed atomic.Int32
	startTestAgentRuntime(t, agentconn.Options{
		Logf: t.Logf,
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{
				NatsServer: natsURL,
				AuthToken:  "mdz_tenantA",
				ClientID:   clientA,
				SiteID:     siteA,
				AgentID:    agentA,
			}
		},
		OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
			select {
			case connA <- struct{}{}:
			default:
			}
		},
		HandleCommand: func(_ context.Context, _ string, _ any) (bool, int, string, string) {
			executed.Add(1)
			return true, 0, "ran", ""
		},
	})

	select {
	case <-connA:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout aguardando conexao NATS")
	}
	time.Sleep(150 * time.Millisecond)

	now := time.Now().UTC()
	// Envelope no subject do Tenant A mas com targetClientId = clientB (spoofed)
	spoofClient, _ := json.Marshal(map[string]any{
		"dispatchId":     "disp-spoof-1",
		"commandId":      "cmd-spoof-1",
		"commandType":    "RunScript",
		"targetScope":    "client",
		"targetClientId": clientB,
		"issuedAtUtc":    now.Format(time.RFC3339),
		"idempotencyKey": "idem-spoof-1",
	})
	// Envelope no subject do Site A mas com targetSiteId = siteB (spoofed)
	spoofSite, _ := json.Marshal(map[string]any{
		"dispatchId":     "disp-spoof-2",
		"commandId":      "cmd-spoof-2",
		"commandType":    "RunScript",
		"targetScope":    "site",
		"targetClientId": clientA,
		"targetSiteId":   siteB,
		"issuedAtUtc":    now.Format(time.RFC3339),
		"idempotencyKey": "idem-spoof-2",
	})

	_, _ = jsAdmin.Publish(fmt.Sprintf("tenant.%s.agents.command", clientA), spoofClient)
	_, _ = jsAdmin.Publish(fmt.Sprintf("tenant.%s.site.%s.agents.command", clientA, siteA), spoofSite)
	_ = adminNC.Flush()
	time.Sleep(300 * time.Millisecond)

	if executed.Load() != 0 {
		t.Fatalf("F15 violacao: agente executou %d envelopes com targetClientId/targetSiteId divergentes", executed.Load())
	}
}

func TestF15_T1_AgentConn_JWTClaims_AcceptsTenantScopedAndRejectsMissing(t *testing.T) {
	const (
		clientA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		siteA   = "11111111-1111-1111-1111-111111111111"
		agentA  = "aaaa1111-1111-1111-1111-111111111111"
	)

	_, natsURL := startEmbeddedNATS(t, true, nil)
	prefix := fmt.Sprintf("tenant.%s.site.%s.agent.%s", clientA, siteA, agentA)
	remoteWildcard := fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote.session.>",
		stripHyphens(clientA), stripHyphens(siteA), stripHyphens(agentA))

	validJWT := makeFakeJWT(map[string]any{
		"nats": map[string]any{
			"sub": map[string]any{
				"allow": []string{
					prefix + ".command",
					fmt.Sprintf("tenant.%s.site.%s.agents.command", clientA, siteA),
					fmt.Sprintf("tenant.%s.agents.command", clientA),
					"tenant.global.agents.command",
					"tenant.global.pong",
					prefix + ".sync.ping",
					fmt.Sprintf("tenant.%s.site.%s.p2p.discovery", clientA, siteA),
					prefix + ".remote-debug.control",
					remoteWildcard,
					"$JS.API.CONSUMER.*.DISCOVERY_FANOUT_COMMANDS_" + stripHyphens(clientA) + ".>",
				},
			},
			"pub": map[string]any{
				"allow": []string{
					prefix + ".heartbeat",
					prefix + ".result",
					prefix + ".hardware",
					prefix + ".remote-debug.log",
					prefix + ".remote-debug.control",
					remoteWildcard,
				},
			},
		},
	})

	connectedCh := make(chan struct{}, 1)
	startTestAgentRuntime(t, agentconn.Options{
		Logf: t.Logf,
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{
				NatsServer: natsURL,
				AuthToken:  "mdz_" + validJWT,
				ClientID:   clientA,
				SiteID:     siteA,
				AgentID:    agentA,
			}
		},
		OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
			select {
			case connectedCh <- struct{}{}:
			default:
			}
		},
	})

	select {
	case <-connectedCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("JWT valido com claims tenant-scoped deveria ter conectado")
	}

	// JWT sem CommandClientFanout deve ser rejeitado na validacao ACL local
	missingFanoutJWT := makeFakeJWT(map[string]any{
		"nats": map[string]any{
			"sub": map[string]any{
				"allow": []string{
					prefix + ".command",
					fmt.Sprintf("tenant.%s.site.%s.agents.command", clientA, siteA),
					"tenant.global.agents.command",
					"tenant.global.pong",
					prefix + ".sync.ping",
					fmt.Sprintf("tenant.%s.site.%s.p2p.discovery", clientA, siteA),
					prefix + ".remote-debug.control",
					remoteWildcard,
				},
			},
			"pub": map[string]any{
				"allow": []string{
					prefix + ".heartbeat",
					prefix + ".result",
					prefix + ".hardware",
					prefix + ".remote-debug.log",
					prefix + ".remote-debug.control",
					remoteWildcard,
				},
			},
		},
	})

	var badConnected atomic.Bool
	rtBad, _ := startTestAgentRuntime(t, agentconn.Options{
		LoadConfig: func() agentconn.Config {
			return agentconn.Config{
				NatsServer: natsURL,
				AuthToken:  "mdz_" + missingFanoutJWT,
				ClientID:   clientA,
				SiteID:     siteA,
				AgentID:    agentA,
			}
		},
		OnNatsConnected: func(_ *nats.Conn, _ agentconn.Config) {
			badConnected.Store(true)
		},
	})
	time.Sleep(400 * time.Millisecond)
	if badConnected.Load() || rtBad.GetStatus().Connected {
		t.Fatalf("JWT sem tenant.{clientId}.agents.command deveria falhar na validacao de ACL")
	}
}
