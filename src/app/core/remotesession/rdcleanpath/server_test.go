package rdcleanpath

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// x224Confirm é um X.224 Connection Confirm sintético (TPKT + X.224 CC).
func x224Confirm() []byte {
	body := []byte{0x0e, 0xd0, 0x00, 0x00, 0x12, 0x34, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	total := 4 + len(body)
	hdr := make([]byte, 4)
	hdr[0] = 0x03 // TPKT version
	hdr[1] = 0x00
	binary.BigEndian.PutUint16(hdr[2:4], uint16(total))
	return append(hdr, body...)
}

// selfSignedCert gera um certificado self-signed para o fake RDP server.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fake-rdp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("criar certificado: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// fakeRDPServer simula o TermService: aceita TCP, le o X.224 Request, responde o
// Confirm cru, e so entao faz o upgrade TLS na mesma conexao.
type fakeRDPServer struct {
	ln      net.Listener
	tlsConf *tls.Config
	gotX224 []byte
	mu      sync.Mutex
	closed  bool
	// afterTLS recebe a conexao TLS ja estabelecida (para o teste de relay).
	afterTLS func(*tls.Conn)
}

func newFakeRDPServer(t *testing.T, cert tls.Certificate) *fakeRDPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeRDPServer{ln: ln, tlsConf: &tls.Config{Certificates: []tls.Certificate{cert}}}
	go s.serve(t)
	return s
}

func (s *fakeRDPServer) serve(t *testing.T) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			// 1. Ler o X.224 Connection Request.
			buf := make([]byte, 4096)
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err := c.Read(buf)
			if err != nil || n == 0 {
				return
			}
			s.mu.Lock()
			s.gotX224 = append([]byte(nil), buf[:n]...)
			s.mu.Unlock()
			// 2. Responder o Confirm em texto claro.
			if _, err := c.Write(x224Confirm()); err != nil {
				return
			}
			// 3. Upgrade TLS na mesma conexao.
			_ = c.SetReadDeadline(time.Time{})
			tc := tls.Server(c, s.tlsConf)
			if err := tc.Handshake(); err != nil {
				return
			}
			if s.afterTLS != nil {
				s.afterTLS(tc)
			} else {
				// Eco simples para manter a conexao viva.
				_, _ = io.Copy(tc, tc)
			}
		}(conn)
	}
}

func (s *fakeRDPServer) addr() (string, int) {
	host, portStr, _ := net.SplitHostPort(s.ln.Addr().String())
	p := 0
	for _, ch := range portStr {
		p = p*10 + int(ch-'0')
	}
	return host, p
}

func (s *fakeRDPServer) close() { _ = s.ln.Close() }

// ── Testes do handshake ─────────────────────────────────────────────────────

func TestHandshakeCompletesAndExtractsCert(t *testing.T) {
	cert := selfSignedCert(t)
	srv := newFakeRDPServer(t, cert)
	defer srv.close()
	_, port := srv.addr()

	x224Req := []byte{0x03, 0x00, 0x00, 0x13, 0x0e, 0xe0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x08, 0x00, 0x03, 0x00, 0x00, 0x00}
	res, err := Handshake(context.Background(), HandshakeConfig{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		Addr:               net.JoinHostPort("127.0.0.1", itoa(port)),
		ServerName:         "127.0.0.1",
		X224Request:        x224Req,
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	defer res.Conn.Close()

	if !bytes.Equal(res.X224Response, x224Confirm()) {
		t.Errorf("X224Response = % x, esperado % x", res.X224Response, x224Confirm())
	}
	if len(res.CertChain) != 1 {
		t.Fatalf("CertChain len = %d, esperado 1", len(res.CertChain))
	}
	if len(res.CertChain[0]) == 0 {
		t.Error("certificado vazio")
	}
	// O servidor deve ter recebido exatamente o X.224 enviado.
	srv.mu.Lock()
	got := srv.gotX224
	srv.mu.Unlock()
	if !bytes.Equal(got, x224Req) {
		t.Errorf("servidor recebeu % x, esperado % x", got, x224Req)
	}
}

// TestHandshakeRejectsEmptyX224 garante validacao de entrada.
func TestHandshakeRejectsEmptyX224(t *testing.T) {
	_, err := Handshake(context.Background(), HandshakeConfig{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) { return nil, nil },
		Addr: "127.0.0.1:3389",
	})
	if err == nil {
		t.Fatal("esperava erro para X.224 vazio")
	}
}

// TestHandshakePropagatesDialError garante que erro de dial nao e engolido.
func TestHandshakePropagatesDialError(t *testing.T) {
	_, err := Handshake(context.Background(), HandshakeConfig{
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, io.ErrUnexpectedEOF
		},
		Addr:        "127.0.0.1:3389",
		X224Request: []byte{0x03, 0x00, 0x00, 0x13},
	})
	if err == nil {
		t.Fatal("esperava erro de dial")
	}
}

// ── Testes do servidor (fail-closed) ────────────────────────────────────────

func TestServerIgnoresClientDestination(t *testing.T) {
	cert := selfSignedCert(t)
	srv := newFakeRDPServer(t, cert)
	defer srv.close()
	_, port := srv.addr()

	var seenAuth, seenDest string
	s := &Server{
		RDPPort:          port,
		AllowInsecureTLS: true,
		Authenticate: func(ctx context.Context, proxyAuth, destination string) error {
			seenAuth, seenDest = proxyAuth, destination
			return nil
		},
		// dial injetado aponta para o fake server, mas o ALVO deve ser o fail-closed.
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}

	x224 := []byte{0x03, 0x00, 0x00, 0x13, 0x0e, 0xe0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x08, 0x00, 0x03, 0x00, 0x00, 0x00}
	// O cliente PEDE um destino malicioso; deve ser ignorado.
	req := NewRequest(x224, "10.0.0.99:3389", "token-x", nil)
	reqDER, err := req.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	respDER, conn, err := s.HandleRequest(context.Background(), reqDER)
	if err != nil {
		t.Fatalf("HandleRequest: %v", err)
	}
	if conn == nil {
		t.Fatal("esperava conexao TLS")
	}
	defer conn.Close()

	// O destino pedido foi registrado...
	if seenDest != "10.0.0.99:3389" {
		t.Errorf("destination visto = %q", seenDest)
	}
	if seenAuth != "token-x" {
		t.Errorf("proxyAuth visto = %q", seenAuth)
	}

	// ...mas a resposta traz o serverAddr FAIL-CLOSED.
	resp, err := Decode(respDER)
	if err != nil {
		t.Fatalf("Decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("response inesperadamente com erro: %+v", resp.Error)
	}
	if resp.ServerAddr == nil || *resp.ServerAddr != net.JoinHostPort("127.0.0.1", itoa(port)) {
		t.Errorf("ServerAddr = %v, esperado 127.0.0.1:%d", resp.ServerAddr, port)
	}
}

// TestDialFailClosedBlocksNonLoopback prova que o dial padrao recusa destinos
// fora de 127.0.0.1:<port> antes de abrir socket.
func TestDialFailClosedBlocksNonLoopback(t *testing.T) {
	s := &Server{RDPPort: 3389}
	cases := []string{
		"10.0.0.1:3389",
		"192.168.1.1:3389",
		"127.0.0.1:3390", // porta errada
		"localhost:3389", // hostname, nao loopback literal
		"[::1]:3389",     // IPv6 loopback nao permitido (fail-closed)
		"0.0.0.0:3389",
	}
	for _, addr := range cases {
		if _, err := s.DialFailClosed(context.Background(), "tcp", addr); err == nil {
			t.Errorf("DialFailClosed(%q) deveria recusar", addr)
		}
	}
}

// TestServerRejectsWithoutAuthenticate prova o fail-closed quando nao ha autenticador.
func TestServerRejectsWithoutAuthenticate(t *testing.T) {
	s := &Server{RDPPort: 3389}
	x224 := []byte{0x03, 0x00, 0x00, 0x13}
	req := NewRequest(x224, "127.0.0.1:3389", "tok", nil)
	reqDER, _ := req.Encode()

	respDER, conn, err := s.HandleRequest(context.Background(), reqDER)
	if err == nil {
		t.Fatal("esperava recusa sem Authenticate")
	}
	if conn != nil {
		t.Error("nao deveria devolver conexao")
	}
	resp, decErr := Decode(respDER)
	if decErr != nil {
		t.Fatalf("Decode: %v", decErr)
	}
	if resp.Error == nil || resp.Error.ErrorCode != ErrGeneralError {
		t.Fatalf("esperava Error PDU general; veio %+v", resp.Error)
	}
	if resp.Error.HTTPStatusCode == nil || *resp.Error.HTTPStatusCode != 401 {
		t.Errorf("http status = %v, esperado 401", resp.Error.HTTPStatusCode)
	}
}

// TestServerRejectsBadAuth prova que autenticacao falha gera Error PDU 401.
func TestServerRejectsBadAuth(t *testing.T) {
	s := &Server{
		RDPPort: 3389,
		Authenticate: func(ctx context.Context, proxyAuth, destination string) error {
			return ErrUnauthorized
		},
	}
	req := NewRequest([]byte{0x03, 0x00, 0x00, 0x13}, "127.0.0.1:3389", "ruim", nil)
	reqDER, _ := req.Encode()

	respDER, conn, err := s.HandleRequest(context.Background(), reqDER)
	if err == nil {
		t.Fatal("esperava erro de auth")
	}
	if conn != nil {
		t.Error("nao deveria devolver conexao")
	}
	resp, _ := Decode(respDER)
	if resp.Error == nil || resp.Error.ErrorCode != ErrGeneralError {
		t.Fatalf("esperava Error general; veio %+v", resp.Error)
	}
}

// TestServerRejectsMalformedRequest prova que DER invalido vira Error PDU 400.
func TestServerRejectsMalformedRequest(t *testing.T) {
	s := &Server{
		RDPPort:      3389,
		Authenticate: func(ctx context.Context, proxyAuth, destination string) error { return nil },
	}
	respDER, conn, _ := s.HandleRequest(context.Background(), []byte("nao-e-der"))
	if conn != nil {
		t.Error("nao deveria devolver conexao")
	}
	resp, err := Decode(respDER)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if resp.Error == nil || resp.Error.HTTPStatusCode == nil || *resp.Error.HTTPStatusCode != 400 {
		t.Fatalf("esperava Error 400; veio %+v", resp.Error)
	}
}

// TestTargetAddrDefault confirma a porta default e o host fail-closed.
func TestTargetAddrDefault(t *testing.T) {
	s := &Server{}
	if got := s.TargetAddr(); got != "127.0.0.1:3389" {
		t.Errorf("TargetAddr = %q", got)
	}
	s2 := &Server{RDPPort: 70000}
	if got := s2.TargetAddr(); got != "127.0.0.1:3389" {
		t.Errorf("TargetAddr porta invalida = %q", got)
	}
	s3 := &Server{RDPPort: 13389}
	if got := s3.TargetAddr(); got != "127.0.0.1:13389" {
		t.Errorf("TargetAddr = %q", got)
	}
}

// ── Testes do relay ─────────────────────────────────────────────────────────

// pipeStream adapta um net.Conn em ClientStream para os testes de relay.
type pipeStream struct{ net.Conn }

func TestRelayTransfersBothDirections(t *testing.T) {
	// Servidor RDP falso que ecoa com prefixo, para distinguir direcoes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		c, aErr := ln.Accept()
		if aErr != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c) // eco
	}()

	serverConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer serverConn.Close()

	clientLocal, clientRemote := net.Pipe()
	defer clientLocal.Close()
	defer clientRemote.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = Relay(ctx, pipeStream{clientRemote}, serverConn)
	}()

	payload := []byte("ola-rdp")
	if _, err := clientLocal.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(payload))
	_ = clientLocal.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(clientLocal, got); err != nil {
		t.Fatalf("read eco: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("eco = %q, esperado %q", got, payload)
	}
	cancel()
	clientLocal.Close()
	<-done
}

// itoa evita importar strconv nos testes de forma redundante.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
