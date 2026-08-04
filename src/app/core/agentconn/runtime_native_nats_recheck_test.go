package agentconn

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestIsWSSLabel(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		{"nats", false},
		{"nats-ws", true},
		{"nats-wss", true},
		{"nats-ws/wss", true},
		{"NATS-WSS", true},
		{"", false},
		{"nats (api-fallback)", false},
		{"nats-wss (api-fallback)", false},
	}
	for _, c := range cases {
		if got := isWSSLabel(c.label); got != c.want {
			t.Errorf("isWSSLabel(%q) = %v, esperado %v", c.label, got, c.want)
		}
	}
}

// TestRunNATSEventLoop_NativeTransport_NoPanic reproduz o bug onde o agente,
// conectado via NATS nativo (nats://), acessava o campo .C de um ticker nil
// dentro do select do event loop, causando nil pointer dereference (panic).
// Com a correção, o canal de recheck fica nil (case desabilitado) e o loop
// deve rodar normalmente até o contexto ser cancelado.
func TestRunNATSEventLoop_NativeTransport_NoPanic(t *testing.T) {
	server := startEmbeddedNATSServer(t)
	nc, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("falha ao conectar no NATS de teste: %v", err)
	}
	t.Cleanup(nc.Close)

	r := &Runtime{}
	cfg := Config{
		AgentID:  testHomologAgentID,
		ClientID: testHomologClientID,
		SiteID:   testHomologSiteID,
		// NatsServer presente: mesmo assim, como o transporte é nativo,
		// o ticker de recheck NÃO deve ser criado (e não deve causar panic).
		NatsServer: server.ClientURL(),
	}
	subjects, err := resolveNATSSubjects(cfg)
	if err != nil {
		t.Fatalf("resolveNATSSubjects falhou: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Transporte nativo ("nats") — cenário que causava o panic.
	done := make(chan error, 1)
	go func() {
		done <- r.runNATSEventLoop(ctx, nc, cfg, subjects, "127.0.0.1", nil, "nats")
	}()

	select {
	case err := <-done:
		// Com ctx cancelado, o loop deve retornar nil (sem panic).
		if err != nil {
			t.Fatalf("runNATSEventLoop retornou erro inesperado: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runNATSEventLoop nao retornou apos cancelamento do contexto (possivel deadlock)")
	}
}

