//go:build windows

package terminal

import (
	"net"
	"sync"
	"testing"
	"time"
)

// fakeConn captura as escritas para o teste da fila de saida do dispatcher (T6).
type fakeConn struct {
	// gate, quando não-nil, bloqueia cada Write até ser fechado — simula o
	// named pipe lento para forçar o acúmulo/descarte da fila.
	gate   chan struct{}
	mu     sync.Mutex
	writes [][]byte
}

func (c *fakeConn) Write(p []byte) (int, error) {
	if c.gate != nil {
		<-c.gate
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}
func (c *fakeConn) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (c *fakeConn) Close() error                     { return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return fakeConnAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr             { return fakeConnAddr{} }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeConnAddr struct{}

func (fakeConnAddr) Network() string { return "fake" }
func (fakeConnAddr) String() string  { return "fake" }

func (c *fakeConn) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, w := range c.writes {
		n += len(w)
	}
	return n
}

// TestBufferedWriter_NaoBloqueiaEFlushNoFim garante que a fila bounded do
// dispatcher nunca bloqueia o callback e que o flush final entrega os bytes.
func TestBufferedWriter_NaoBloqueiaEFlushNoFim(t *testing.T) {
	conn := &fakeConn{}
	w := newBufferedWriter(conn, 4, 1<<20)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			w.Write([]byte("abcd")) // 800 bytes no total
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Write bloqueou (a fila deveria ser non-blocking)")
	}

	w.Close() // flush final
	if got := conn.total(); got != 800 {
		t.Fatalf("bytes drenados = %d, want 800 (sem perda abaixo do teto)", got)
	}
}

// TestBufferedWriter_DescartaNoTeto garante que, estourando o teto duro, o
// descarte acontece (com contador) sem bloquear.
func TestBufferedWriter_DescartaNoTeto(t *testing.T) {
	gate := make(chan struct{})
	conn := &fakeConn{gate: gate}
	w := newBufferedWriter(conn, 2, 4)

	// Escrita bloqueada no pipe → 200 chunks de 4 bytes acumulam bem acima de
	// maxBytes=4: coalesce + descarte, sem nunca bloquear o Write.
	for i := 0; i < 200; i++ {
		w.Write([]byte("abcd"))
	}
	close(gate)
	w.Close()
	if w.Dropped() == 0 {
		t.Fatal("esperava descarte contabilizado acima do teto duro")
	}
}
