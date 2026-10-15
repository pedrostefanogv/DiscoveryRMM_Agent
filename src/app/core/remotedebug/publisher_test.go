package remotedebug

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

const testDebugSubject = "tenant.client-1.site.site-1.agent.agent-1.remote-debug.log"

func startEmbeddedNATSServer(t *testing.T) *natsserver.Server {
	t.Helper()

	server, err := natsserver.NewServer(&natsserver.Options{
		Host:   "127.0.0.1",
		Port:   -1,
		NoLog:  true,
		NoSigs: true,
	})
	if err != nil {
		t.Fatalf("falha ao criar NATS server de teste: %v", err)
	}

	go server.Start()
	if !server.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS server de teste nao ficou pronto")
	}
	t.Cleanup(server.Shutdown)

	return server
}

func buildTestPublisher(t *testing.T, server *natsserver.Server) Publisher {
	t.Helper()

	publishers, err := BuildPublishers(
		Config{NatsServer: server.ClientURL()},
		StreamConfig{NatsSubject: testDebugSubject},
		"mdz_test_token",
		"client-1",
		"site-1",
	)
	if err != nil {
		t.Fatalf("BuildPublishers: %v", err)
	}
	if len(publishers) != 1 {
		t.Fatalf("esperava 1 publisher (sem WSS configurado), got %d", len(publishers))
	}
	t.Cleanup(func() { _ = publishers[0].Close() })

	return publishers[0]
}

// TestNATSPublisher_DeliversLogMessage cobre o caminho real de publish
// (Publish + FlushTimeout + comparação de LastError) contra um servidor NATS de
// verdade. É o guarda-corpo da correção feita aqui: se a comparação de
// LastError estivesse errada, TODO publish seria reportado como falha e o
// manager aposentaria o transporte no primeiro log — sessão muda.
func TestNATSPublisher_DeliversLogMessage(t *testing.T) {
	server := startEmbeddedNATSServer(t)

	subConn, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("falha ao conectar subscriber: %v", err)
	}
	t.Cleanup(subConn.Close)

	received := make(chan LogMessage, 1)
	if _, err := subConn.Subscribe(testDebugSubject, func(msg *nats.Msg) {
		var out LogMessage
		if err := json.Unmarshal(msg.Data, &out); err != nil {
			return
		}
		received <- out
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := subConn.Flush(); err != nil {
		t.Fatalf("Flush do subscriber: %v", err)
	}

	publisher := buildTestPublisher(t, server)

	sent := LogMessage{
		SessionID:    "sess-1",
		AgentID:      "agent-1",
		Message:      "linha de teste",
		Level:        "info",
		TimestampUTC: time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Sequence:     1,
	}
	if err := publisher.Publish(context.Background(), sent); err != nil {
		t.Fatalf("Publish falhou em conexao saudavel: %v", err)
	}

	select {
	case got := <-received:
		if got.Message != sent.Message || got.Sequence != sent.Sequence || got.SessionID != sent.SessionID {
			t.Fatalf("mensagem inesperada: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mensagem nao chegou ao subscriber")
	}
}

// TestNATSPublisher_PublishAfterCloseIsAnError garante que uma falha REAL
// (conexão fechada) continua sendo reportada: o tratamento dado ao flush
// timeout não pode engolir tudo, senão o manager nunca faria fallback para o
// transporte seguinte nem sinalizaria a sessão morta no log.
func TestNATSPublisher_PublishAfterCloseIsAnError(t *testing.T) {
	server := startEmbeddedNATSServer(t)
	publisher := buildTestPublisher(t, server)

	if err := publisher.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := publisher.Publish(context.Background(), LogMessage{Message: "x"}); err == nil {
		t.Fatal("esperava erro ao publicar em conexao fechada")
	}
}

// TestNewNATSPublisher_RejectsIncompleteConfig garante que um endpoint ausente
// não vira um publisher "meio configurado" que falharia silenciosamente no
// primeiro log.
func TestNewNATSPublisher_RejectsIncompleteConfig(t *testing.T) {
	cases := []struct {
		name    string
		server  string
		token   string
		subject string
	}{
		{name: "sem servidor", server: "", token: "mdz_x", subject: testDebugSubject},
		{name: "sem token", server: "nats://127.0.0.1:4222", token: "", subject: testDebugSubject},
		{name: "sem subject", server: "nats://127.0.0.1:4222", token: "mdz_x", subject: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newNATSPublisher(tc.server, tc.token, tc.subject, "nats"); err == nil {
				t.Fatal("esperava erro de configuracao NATS incompleta")
			}
		})
	}
}

// TestRemoteDebugEndpointCandidates_DerivesFromApiServer cobre o FIX de
// 2026-10-14: com NatsWsServer vazio (o caso real de um deployment que só expõe
// o NATS por WebSocket na 443), os candidatos precisam ser derivados do
// ApiServer — com o wss PRIMEIRO, porta explícita e ProxyPath.
func TestRemoteDebugEndpointCandidates_DerivesFromApiServer(t *testing.T) {
	cands := remoteDebugEndpointCandidates(
		Config{
			AgentID:    "d2719a7d-43bb-4e7e-bbe6-18dce7bf1db7",
			NatsServer: "nats://discovery.pstefano.com.br:4222",
			ApiServer:  "discovery.pstefano.com.br",
		},
		StreamConfig{NatsSubject: testDebugSubject},
	)

	urls := make([]string, 0, len(cands))
	for _, c := range cands {
		urls = append(urls, c.url)
	}
	want := []string{
		"wss://discovery.pstefano.com.br:443/nats/",
		"nats://discovery.pstefano.com.br:4222",
	}
	if len(urls) != len(want) {
		t.Fatalf("candidatos = %v, esperado %v", urls, want)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Fatalf("candidato[%d] = %q, esperado %q (todos: %v)", i, urls[i], want[i], urls)
		}
	}
	if cands[0].proxyPath != "/nats/" {
		t.Fatalf("ProxyPath do wss = %q, esperado /nats/", cands[0].proxyPath)
	}
}

// TestRemoteDebugEndpointCandidates_NoDuplicates garante que WSS explícito do
// comando/config não é duplicado com o derivado do ApiServer, e que o host
// aceita as três formas (host, host:porta, URL com path).
func TestRemoteDebugEndpointCandidates_NoDuplicates(t *testing.T) {
	cands := remoteDebugEndpointCandidates(
		Config{
			ApiServer:    "https://discovery.pstefano.com.br/api/",
			NatsWsServer: "wss://discovery.pstefano.com.br/nats/",
		},
		StreamConfig{NatsSubject: testDebugSubject, NatsWssURL: "wss://discovery.pstefano.com.br:443/nats/"},
	)

	seen := map[string]int{}
	for _, c := range cands {
		seen[c.url]++
	}
	for url, n := range seen {
		if n != 1 {
			t.Fatalf("candidato duplicado %q (%d vezes): %+v", url, n, cands)
		}
	}
	if len(cands) != 2 {
		t.Fatalf("esperava 2 candidatos (wss + nativo derivado), got %d: %+v", len(cands), cands)
	}
	if cands[0].url != "wss://discovery.pstefano.com.br:443/nats/" || cands[0].proxyPath != "/nats/" {
		t.Fatalf("primeiro candidato = %+v", cands[0])
	}
	if cands[1].url != "nats://discovery.pstefano.com.br:4222" {
		t.Fatalf("segundo candidato = %+v", cands[1])
	}
}

// TestEndpointHost cobre as formas aceitas no campo do admin.
func TestEndpointHost(t *testing.T) {
	cases := map[string]string{
		"discovery.pstefano.com.br":              "discovery.pstefano.com.br",
		"https://discovery.pstefano.com.br/api/": "discovery.pstefano.com.br",
		"host:4222":                              "host",
		"  ":                                     "",
		"":                                       "",
	}
	for in, want := range cases {
		if got := endpointHost(in); got != want {
			t.Fatalf("endpointHost(%q) = %q, esperado %q", in, got, want)
		}
	}
}
