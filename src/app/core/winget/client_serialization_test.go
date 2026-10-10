package winget

import (
	"context"
	"testing"
	"time"
)

// Invocações concorrentes do winget fazem o binário falhar com 0x8a150001
// (disputa do cache de fontes). O semáforo garante que só uma rode por vez.
func TestWingetRunSerialized(t *testing.T) {
	ctx := context.Background()

	if err := acquireWingetRun(ctx); err != nil {
		t.Fatalf("primeira reserva: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		if err := acquireWingetRun(ctx); err != nil {
			close(acquired)
			return
		}
		releaseWingetRun()
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("segunda reserva nao podia entrar enquanto a primeira esta ativa")
	case <-time.After(150 * time.Millisecond):
	}

	releaseWingetRun()

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("segunda reserva nao foi liberada apos o release")
	}
}

// Ctx cancelado nao pode deixar a reserva travada esperando para sempre.
func TestAcquireWingetRunRespectsContext(t *testing.T) {
	if err := acquireWingetRun(context.Background()); err != nil {
		t.Fatalf("reserva inicial: %v", err)
	}
	defer releaseWingetRun()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := acquireWingetRun(ctx); err == nil {
		t.Fatal("esperava erro de ctx ao nao conseguir a vez")
	}
}
