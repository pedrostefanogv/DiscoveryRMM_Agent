package rdcleanpath

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"
)

// blockingTransport bloqueia DE VERDADE em Recv (sem RecvContext nativo),
// para exercitar o caminho de FALLBACK (leitor persistente).
//
// Protegido por mutex: Recv roda em goroutine propria (o pump) e feed/Close
// rodam no teste, entao todo acesso ao estado precisa de sincronizacao.
type blockingTransport struct {
	mu       sync.Mutex
	notify   chan struct{}
	incoming [][]byte
	closed   bool
}

func newBlockingTransport() *blockingTransport {
	return &blockingTransport{notify: make(chan struct{}, 1)}
}

func (b *blockingTransport) Send(data []byte) error { return nil }

func (b *blockingTransport) Recv() ([]byte, error) {
	for {
		b.mu.Lock()
		if len(b.incoming) > 0 {
			blk := b.incoming[0]
			b.incoming = b.incoming[1:]
			b.mu.Unlock()
			return blk, nil
		}
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return nil, io.EOF
		}
		<-b.notify
	}
}

func (b *blockingTransport) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	select {
	case b.notify <- struct{}{}:
	default:
	}
	return nil
}

func (b *blockingTransport) feed(d []byte) {
	b.mu.Lock()
	b.incoming = append(b.incoming, d)
	b.mu.Unlock()
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

// REGRESSAO: o leitor persistente deve ser UNICO por sessao, mesmo com N timeouts.
func TestProbePumpIsSingleGoroutine(t *testing.T) {
	srv := &Server{RDPPort: 3389, Authenticate: func(ctx context.Context, a, d string) error { return nil }}
	s := NewSession(SessionConfig{Server: srv, RequestTimeout: 20 * time.Millisecond})
	tr := newBlockingTransport()

	base := runtime.NumGoroutine()
	// 5 chamadas de recvBlock com timeout: NAO devem criar 5 goroutines.
	for i := 0; i < 5; i++ {
		_, _ = s.recvBlock(context.Background(), tr, 15*time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	after := runtime.NumGoroutine()
	fmt.Printf("base=%d apos=%d delta=%d\n", base, after, after-base)
	if after-base > 1 {
		t.Fatalf("VAZAMENTO: delta=%d, esperado no maximo 1 (leitor persistente)", after-base)
	}
	fmt.Println("OK: no maximo 1 goroutine de leitura por sessao")
	s.finish()
	time.Sleep(100 * time.Millisecond)
	fmt.Printf("apos finish=%d\n", runtime.NumGoroutine())
}

// REGRESSAO: nenhum bloco pode ser perdido quando ha timeouts intercalados.
func TestProbeNoBlockLossAfterTimeout(t *testing.T) {
	srv := &Server{RDPPort: 3389, Authenticate: func(ctx context.Context, a, d string) error { return nil }}
	s := NewSession(SessionConfig{Server: srv, RequestTimeout: 20 * time.Millisecond})
	tr := newBlockingTransport()

	// 3 timeouts primeiro.
	for i := 0; i < 3; i++ {
		_, _ = s.recvBlock(context.Background(), tr, 15*time.Millisecond)
	}
	// Agora alimenta 3 blocos e confirma que TODOS chegam.
	for i := 0; i < 3; i++ {
		tr.feed([]byte(fmt.Sprintf("BLOCO-%d", i)))
	}
	received := 0
	for i := 0; i < 3; i++ {
		got, err := s.recvBlock(context.Background(), tr, 300*time.Millisecond)
		if err != nil {
			break
		}
		received++
		fmt.Printf("recebido: %q\n", got)
	}
	if received != 3 {
		t.Fatalf("PERDA DE DADOS: recebeu %d de 3 blocos", received)
	}
	fmt.Println("OK: nenhum bloco perdido")
	s.finish()
}

// Confirma que o caminho NATIVO (RecvContext) nao cria goroutine alguma.
func TestProbeNativePathNoGoroutine(t *testing.T) {
	pub := &fakePublisher{}
	sub := func(sessionID string, h func([]byte)) (func(), error) { return func() {}, nil }
	tr, err := NewNatsTransport("s", pub, sub)
	if err != nil {
		t.Fatalf("NewNatsTransport: %v", err)
	}
	if _, ok := interface{}(tr).(RecvWithContext); !ok {
		t.Fatal("NatsTransport deve implementar RecvWithContext")
	}
	srv := &Server{RDPPort: 3389, Authenticate: func(ctx context.Context, a, d string) error { return nil }}
	s := NewSession(SessionConfig{Server: srv, RequestTimeout: 20 * time.Millisecond})

	base := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		_, _ = s.recvBlock(context.Background(), tr, 15*time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	after := runtime.NumGoroutine()
	fmt.Printf("nativo: base=%d apos=%d delta=%d\n", base, after, after-base)
	if after-base != 0 {
		t.Fatalf("caminho nativo nao deveria criar goroutines, delta=%d", after-base)
	}
	fmt.Println("OK: caminho nativo sem goroutines")
}
