package rdcleanpath

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// ── Dublês de transporte ────────────────────────────────────────────────────

// fakeTransport injeta blocos do cliente e captura o que vai para ele.
type fakeTransport struct {
	mu       sync.Mutex
	incoming [][]byte
	sent     [][]byte
	closed   bool
	notify   chan struct{}
}

func newFakeTransport(blocks ...[]byte) *fakeTransport {
	return &fakeTransport{incoming: blocks, notify: make(chan struct{}, 1)}
}

func (f *fakeTransport) Send(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ErrTransportClosed
	}
	cp := append([]byte(nil), data...)
	f.sent = append(f.sent, cp)
	return nil
}

func (f *fakeTransport) Recv() ([]byte, error) {
	for {
		f.mu.Lock()
		if len(f.incoming) > 0 {
			blk := f.incoming[0]
			f.incoming = f.incoming[1:]
			f.mu.Unlock()
			return blk, nil
		}
		closed := f.closed
		f.mu.Unlock()
		if closed {
			return nil, io.EOF
		}
		select {
		case <-f.notify:
		case <-time.After(50 * time.Millisecond):
			// Evita travar indefinidamente nos testes.
			f.mu.Lock()
			empty := len(f.incoming) == 0
			f.mu.Unlock()
			if empty {
				return nil, io.EOF
			}
		}
	}
}

func (f *fakeTransport) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	select {
	case f.notify <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeTransport) sentBlocks() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.sent))
	copy(out, f.sent)
	return out
}

// feed adiciona um bloco depois da criacao (simula mensagem tardia).
func (f *fakeTransport) feed(block []byte) {
	f.mu.Lock()
	f.incoming = append(f.incoming, block)
	f.mu.Unlock()
	select {
	case f.notify <- struct{}{}:
	default:
	}
}

// ── Sessao: fluxo feliz ─────────────────────────────────────────────────────

func TestSessionAwaitingRequestInitially(t *testing.T) {
	s := NewSession(SessionConfig{Server: &Server{}})
	if s.State() != StateAwaitingRequest {
		t.Fatalf("estado inicial = %v", s.State())
	}
}

// TestSessionRejectsOversizedRequest prova o teto anti-DoS.
func TestSessionRejectsOversizedRequest(t *testing.T) {
	srv := &Server{
		RDPPort:      3389,
		Authenticate: func(ctx context.Context, a, d string) error { return nil },
	}
	s := NewSession(SessionConfig{Server: srv, MaxRequestBytes: 64, RequestTimeout: 2 * time.Second})

	big := bytes.Repeat([]byte{0xAA}, 200)
	tr := newFakeTransport(big)

	err := s.Run(context.Background(), tr)
	if err == nil {
		t.Fatal("esperava erro para request grande demais")
	}
	// Deve responder 400 in-band.
	sent := tr.sentBlocks()
	if len(sent) == 0 {
		t.Fatal("esperava Error PDU in-band")
	}
	pdu, decErr := Decode(sent[0])
	if decErr != nil {
		t.Fatalf("Decode: %v", decErr)
	}
	if pdu.Error == nil || pdu.Error.HTTPStatusCode == nil || *pdu.Error.HTTPStatusCode != 400 {
		t.Fatalf("esperava Error 400, veio %+v", pdu.Error)
	}
}

// TestSessionTimeoutWithoutRequest prova que conexao silenciosa nao fica pendurada.
func TestSessionTimeoutWithoutRequest(t *testing.T) {
	srv := &Server{
		RDPPort:      3389,
		Authenticate: func(ctx context.Context, a, d string) error { return nil },
	}
	s := NewSession(SessionConfig{Server: srv, RequestTimeout: 200 * time.Millisecond})

	// Transporte sem nenhum bloco: Recv deve dar timeout.
	tr := newFakeTransport()
	start := time.Now()
	err := s.Run(context.Background(), tr)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("esperava timeout")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("demorou demais para expirar: %v", elapsed)
	}
	if s.State() != StateClosed {
		t.Errorf("estado final = %v", s.State())
	}
}

// TestSessionRequestFragmentedAcrossBlocks prova que o DER pode chegar em pedacos.
func TestSessionRequestFragmentedAcrossBlocks(t *testing.T) {
	x224 := []byte{0x03, 0x00, 0x00, 0x13, 0x0e, 0xe0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x08, 0x00, 0x03, 0x00, 0x00, 0x00}
	req := NewRequest(x224, "127.0.0.1:3389", "tok", nil)
	der, err := req.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(der) < 4 {
		t.Fatal("der curto demais para o teste")
	}

	// Servidor que falha de proposito apos o request (nao temos TermService aqui),
	// mas prova que o request fragmentado foi remontado e decodificado.
	var gotDestination string
	srv := &Server{
		RDPPort: 3389,
		Authenticate: func(ctx context.Context, a, d string) error {
			gotDestination = d
			return nil
		},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, io.ErrUnexpectedEOF // forca falha apos o parse
		},
	}
	s := NewSession(SessionConfig{Server: srv, RequestTimeout: 3 * time.Second, HandshakeTimeout: 2 * time.Second})

	// Quebra o DER em 3 blocos.
	third := len(der) / 3
	tr := newFakeTransport(der[:third], der[third:2*third], der[2*third:])

	_ = s.Run(context.Background(), tr)

	if gotDestination != "127.0.0.1:3389" {
		t.Fatalf("request fragmentado nao foi remontado; destination=%q", gotDestination)
	}
}

// TestSessionSendsResponseBeforeRelay garante a ordem do protocolo:
// o cliente DEVE receber o Response DER antes de qualquer byte de relay.
func TestSessionSendsResponseBeforeRelay(t *testing.T) {
	cert := selfSignedCert(t)
	rdp := newFakeRDPServer(t, cert)
	defer rdp.close()
	_, port := rdp.addr()

	srv := &Server{
		RDPPort:          port,
		AllowInsecureTLS: true,
		Authenticate:     func(ctx context.Context, a, d string) error { return nil },
	}
	s := NewSession(SessionConfig{Server: srv, RequestTimeout: 3 * time.Second, HandshakeTimeout: 5 * time.Second})

	x224 := []byte{0x03, 0x00, 0x00, 0x13, 0x0e, 0xe0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x08, 0x00, 0x03, 0x00, 0x00, 0x00}
	req := NewRequest(x224, "127.0.0.1:"+itoa(port), "tok", nil)
	der, _ := req.Encode()

	tr := newFakeTransport(der)
	// Encerra o transporte logo apos para o relay terminar rapido.
	go func() {
		time.Sleep(300 * time.Millisecond)
		tr.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Run(ctx, tr)

	sent := tr.sentBlocks()
	if len(sent) == 0 {
		t.Fatal("nada foi enviado ao cliente")
	}
	// O primeiro bloco deve ser um Response DER VALIDO (nao bytes de relay).
	pdu, decErr := Decode(sent[0])
	if decErr != nil {
		t.Fatalf("primeiro bloco nao e um PDU valido: %v", decErr)
	}
	if pdu.Error != nil {
		t.Fatalf("response com erro inesperado: %+v", pdu.Error)
	}
	if pdu.ServerAddr == nil || *pdu.ServerAddr != net.JoinHostPort("127.0.0.1", itoa(port)) {
		t.Errorf("ServerAddr = %v", pdu.ServerAddr)
	}
	if len(pdu.ServerCertChain) == 0 {
		t.Error("response sem cadeia de certificados")
	}
}

// TestSessionStateTransitions prova a progressao dos estados.
func TestSessionStateTransitions(t *testing.T) {
	s := NewSession(SessionConfig{Server: &Server{}, RequestTimeout: 100 * time.Millisecond})
	if s.State() != StateAwaitingRequest {
		t.Fatalf("inicial = %v", s.State())
	}
	tr := newFakeTransport()
	_ = s.Run(context.Background(), tr)
	if s.State() != StateClosed {
		t.Fatalf("final = %v, esperado closed", s.State())
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() nao foi fechado")
	}
}

// ── Transporte NATS (subject dedicado + isolamento) ────────────────────────

// fakePublisher captura publicacoes por sessionID.
type fakePublisher struct {
	mu    sync.Mutex
	items map[string][][]byte
}

func (p *fakePublisher) PublishRdpCleanpathOut(sessionID string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.items == nil {
		p.items = map[string][][]byte{}
	}
	cp := append([]byte(nil), data...)
	p.items[sessionID] = append(p.items[sessionID], cp)
	return nil
}

func (p *fakePublisher) get(sessionID string) [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.items[sessionID]))
	copy(out, p.items[sessionID])
	return out
}

// TestSubjectForUsesDedicatedSuffix prova que o RDCleanPath NAO usa .tunnel.out.
func TestSubjectForUsesDedicatedSuffix(t *testing.T) {
	base := "tenant.t.site.s.agent.a.remote.session"
	sid := "11111111-2222-3333-4444-555555555555"

	in := SubjectFor(base, sid, SuffixSubjectIn)
	out := SubjectFor(base, sid, SuffixSubjectOut)

	wantIn := "tenant.t.site.s.agent.a.remote.session.11111111222233334444555555555555.rdp.cleanpath.in"
	wantOut := "tenant.t.site.s.agent.a.remote.session.11111111222233334444555555555555.rdp.cleanpath.out"

	if in != wantIn {
		t.Errorf("in = %q, esperado %q", in, wantIn)
	}
	if out != wantOut {
		t.Errorf("out = %q, esperado %q", out, wantOut)
	}
	// O sufixo dedicado NAO pode colidir com o canal do tunel do proxy LAN.
	if SuffixSubjectIn == "tunnel.in" || SuffixSubjectOut == "tunnel.out" {
		t.Fatal("RDCleanPath nao deve compartilhar subject com o tunel do proxy LAN")
	}
}

// TestNormalizeSessionIDStripsHyphens espelha a regra do backend.
func TestNormalizeSessionIDStripsHyphens(t *testing.T) {
	got := NormalizeSessionID("AAAAAAAA-BBBB-CCCC")
	if got != "AAAAAAAABBBBCCCC" {
		t.Errorf("NormalizeSessionID = %q", got)
	}
}

// TestNatsTransportSendReceive prova o caminho de dados do transporte.
func TestNatsTransportSendReceive(t *testing.T) {
	pub := &fakePublisher{}
	var capturedHandler func([]byte)
	subscribe := func(sessionID string, handler func(data []byte)) (func(), error) {
		capturedHandler = handler
		return func() {}, nil
	}

	tr, err := NewNatsTransport("sess-1", pub, subscribe)
	if err != nil {
		t.Fatalf("NewNatsTransport: %v", err)
	}
	if capturedHandler == nil {
		t.Fatal("handler nao foi registrado")
	}

	// Dado injetado pelo handler deve sair no Recv.
	payload := []byte("bloco-rdp")
	capturedHandler(payload)
	got, err := tr.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("Recv = %q, esperado %q", got, payload)
	}

	// Send deve publicar no subject dedicado.
	if err := tr.Send([]byte("resposta")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := pub.get("sess-1"); len(got) != 1 || !bytes.Equal(got[0], []byte("resposta")) {
		t.Errorf("publicado = %v", got)
	}

	// Apos Close, Recv devolve EOF e Send falha.
	_ = tr.Close()
	if _, err := tr.Recv(); err != io.EOF {
		t.Errorf("Recv apos Close = %v, esperado EOF", err)
	}
	if err := tr.Send([]byte("x")); err != ErrTransportClosed {
		t.Errorf("Send apos Close = %v", err)
	}
}

// TestNatsTransportCloseIsIdempotent prova que Close pode ser chamado varias vezes.
func TestNatsTransportCloseIsIdempotent(t *testing.T) {
	pub := &fakePublisher{}
	cancels := 0
	subscribe := func(sessionID string, handler func(data []byte)) (func(), error) {
		return func() { cancels++ }, nil
	}
	tr, err := NewNatsTransport("s", pub, subscribe)
	if err != nil {
		t.Fatalf("NewNatsTransport: %v", err)
	}
	_ = tr.Close()
	_ = tr.Close()
	_ = tr.Close()
	if cancels != 1 {
		t.Errorf("cancel chamado %d vezes, esperado 1", cancels)
	}
}

// TestNatsTransportRejectsBadArgs prova validacao de entrada.
func TestNatsTransportRejectsBadArgs(t *testing.T) {
	pub := &fakePublisher{}
	sub := func(sessionID string, handler func(data []byte)) (func(), error) { return func() {}, nil }
	if _, err := NewNatsTransport("", pub, sub); err == nil {
		t.Error("esperava erro para sessionID vazio")
	}
	if _, err := NewNatsTransport("s", nil, sub); err == nil {
		t.Error("esperava erro para publisher nil")
	}
	if _, err := NewNatsTransport("s", pub, nil); err == nil {
		t.Error("esperava erro para subscribe nil")
	}
}

// TestSessionOverNatsTransport prova o fluxo completo ponta-a-ponta.
func TestSessionOverNatsTransport(t *testing.T) {
	cert := selfSignedCert(t)
	rdp := newFakeRDPServer(t, cert)
	defer rdp.close()
	_, port := rdp.addr()

	srv := &Server{
		RDPPort:          port,
		AllowInsecureTLS: true,
		Authenticate:     func(ctx context.Context, a, d string) error { return nil },
	}
	s := NewSession(SessionConfig{Server: srv, RequestTimeout: 3 * time.Second, HandshakeTimeout: 5 * time.Second})

	pub := &fakePublisher{}
	var handler func([]byte)
	subscribe := func(sessionID string, h func(data []byte)) (func(), error) {
		handler = h
		return func() {}, nil
	}
	tr, err := NewNatsTransport("sess-x", pub, subscribe)
	if err != nil {
		t.Fatalf("NewNatsTransport: %v", err)
	}

	x224 := []byte{0x03, 0x00, 0x00, 0x13, 0x0e, 0xe0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x08, 0x00, 0x03, 0x00, 0x00, 0x00}
	req := NewRequest(x224, "127.0.0.1:"+itoa(port), "tok", nil)
	der, _ := req.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, tr) }()

	// Injeta o request pelo canal dedicado.
	time.Sleep(100 * time.Millisecond)
	handler(der)

	// O response deve aparecer no subject de saida dedicado.
	deadline := time.Now().Add(4 * time.Second)
	var respBytes []byte
	for time.Now().Before(deadline) {
		if items := pub.get("sess-x"); len(items) > 0 {
			respBytes = items[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if respBytes == nil {
		t.Fatal("nenhum response publicado no subject dedicado")
	}
	pdu, decErr := Decode(respBytes)
	if decErr != nil {
		t.Fatalf("response invalido: %v", decErr)
	}
	if pdu.Error != nil {
		t.Fatalf("response com erro: %+v", pdu.Error)
	}

	s.Close()
	_ = tr.Close()
}

// TestRelayBlockSizeBound garante que o relay respeita o teto de bloco do NATS.
func TestRelayBlockSizeBound(t *testing.T) {
	if MaxTransportBlockBytes > 2*1024*1024 {
		t.Fatalf("MaxTransportBlockBytes (%d) excede o payload maximo do NATS (2 MiB)", MaxTransportBlockBytes)
	}
	if MaxTransportBlockBytes <= 0 {
		t.Fatal("MaxTransportBlockBytes deve ser positivo")
	}
}

// Referencia para o compilador: binary e usado no helper de TPKT dos outros testes.
var _ = binary.BigEndian
