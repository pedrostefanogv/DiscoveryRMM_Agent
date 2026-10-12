package app

import (
	"context"
	"testing"
	"time"

	"discovery/app/core/agentconn"
	"discovery/app/core/database"
)

func newTestCommandDedupeApp(t *testing.T) *App {
	t.Helper()

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("falha ao abrir database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	app := &App{ctx: context.Background(), commandDedupe: newCommandDedupeStore()}
	app.CoreAgent.DB = db
	return app
}

// O dedupe de comando precisa sobreviver ao reboot do processo (é o cenário do
// loop de reinício: restart antes do result/ack).
func TestCommandDedupePersisteEntreInstancias(t *testing.T) {
	app := newTestCommandDedupeApp(t)

	expiresAt := time.Now().Add(6 * time.Hour).Truncate(time.Second)
	err := app.persistCommandDedupe(agentconn.CommandDedupeEntry{
		Key:       "agent-cmd:abc-123",
		ExpiresAt: expiresAt,
		Result: &agentconn.CommandResultSnapshot{
			CommandID: "abc-123",
			ExitCode:  0,
			Output:    "reinicio agendado",
		},
	})
	if err != nil {
		t.Fatalf("persistCommandDedupe falhou: %v", err)
	}

	// Simula o reboot: nova instância do App lendo o mesmo banco local.
	restarted := &App{ctx: context.Background(), commandDedupe: newCommandDedupeStore()}
	restarted.CoreAgent.DB = app.CoreAgent.DB

	entries := restarted.loadCommandDedupe()
	if len(entries) != 1 {
		t.Fatalf("esperava 1 entrada persistida, veio %d", len(entries))
	}
	if entries[0].Key != "agent-cmd:abc-123" {
		t.Fatalf("chave inesperada: %q", entries[0].Key)
	}
	if entries[0].Result == nil || entries[0].Result.Output != "reinicio agendado" {
		t.Fatalf("resultado deve sobreviver ao reboot, veio %+v", entries[0].Result)
	}
	if !entries[0].ExpiresAt.After(time.Now().Add(5 * time.Hour)) {
		t.Fatalf("TTL curto demais: %s", entries[0].ExpiresAt)
	}
}

func TestCommandDedupeDescartaEntradasExpiradas(t *testing.T) {
	app := newTestCommandDedupeApp(t)

	err := app.persistCommandDedupe(agentconn.CommandDedupeEntry{
		Key:       "agent-cmd:velho",
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("persistCommandDedupe falhou: %v", err)
	}

	if entries := app.loadCommandDedupe(); len(entries) != 0 {
		t.Fatalf("entrada expirada não deve ser carregada, veio %+v", entries)
	}
}
