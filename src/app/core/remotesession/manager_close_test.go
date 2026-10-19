package remotesession

import (
	"sync"
	"testing"
	"time"
)

// slowBanner/slowTray simulam janelas cuja thread não responde: Close() demora
// (como o Close real, que espera até 2s), RequestClose() é imediato.
type slowBanner struct {
	mu       sync.Mutex
	requests int
	closes   int
}

func (b *slowBanner) RequestClose() {
	b.mu.Lock()
	b.requests++
	b.mu.Unlock()
}

func (b *slowBanner) Close() {
	b.mu.Lock()
	b.closes++
	b.mu.Unlock()
	time.Sleep(2 * time.Second)
}

type slowTray struct {
	mu       sync.Mutex
	requests int
	closes   int
}

func (t *slowTray) Show(string) error { return nil }
func (t *slowTray) Hide() error       { return nil }

func (t *slowTray) RequestClose() {
	t.mu.Lock()
	t.requests++
	t.mu.Unlock()
}

func (t *slowTray) Close() {
	t.mu.Lock()
	t.closes++
	t.mu.Unlock()
	time.Sleep(2 * time.Second)
}

// TestCloseSessionLockedDoesNotWaitForWindowThread é a regressão do mutex do
// manager: closeSessionLocked roda com m.mu preso e NÃO pode chamar Close()
// (que espera a thread da janela). Deve apenas pedir o fechamento
// (RequestClose), sem segurar o manager por até 2s.
func TestCloseSessionLockedDoesNotWaitForWindowThread(t *testing.T) {
	mgr := NewManager(nil)
	banner := &slowBanner{}
	tray := &slowTray{}
	session := &Session{
		ID:     "sess-close",
		Kind:   "proxy",
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
		banner: banner,
		tray:   tray,
	}
	mgr.sessions[session.ID] = session

	start := time.Now()
	mgr.StopByUser(session.ID, "teste")
	elapsed := time.Since(start)

	if elapsed > 200*time.Millisecond {
		t.Fatalf("StopByUser levou %v — closeSessionLocked está esperando a thread da janela com o mutex preso", elapsed)
	}
	banner.mu.Lock()
	bannerRequests, bannerCloses := banner.requests, banner.closes
	banner.mu.Unlock()
	tray.mu.Lock()
	trayRequests, trayCloses := tray.requests, tray.closes
	tray.mu.Unlock()
	if bannerRequests != 1 || trayRequests != 1 {
		t.Fatalf("RequestClose não chamado (banner=%d tray=%d)", bannerRequests, trayRequests)
	}
	if bannerCloses != 0 || trayCloses != 0 {
		t.Fatalf("Close bloqueante foi chamado com o mutex preso (banner=%d tray=%d)", bannerCloses, trayCloses)
	}
	if mgr.CountActive() != 0 {
		t.Fatalf("sessão deveria ter sido removida")
	}
}
