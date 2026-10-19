//go:build windows

package app

// Testes de integração do canal IPC (servidor/autenticação/cliente) usando
// net.Pipe — sem named pipe real e sem depender do serviço instalado.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"discovery/app/core/buildinfo"
)

type ipcTestPeer struct {
	server *IPCServer
	conn   net.Conn
	reader *bufio.Reader
}

// newIPCTestServer cria um servidor sem named pipe (net.Pipe nos testes).
func newIPCTestServer(onMessage func(net.Conn, IPCMessage)) *IPCServer {
	return &IPCServer{
		conns:     make(map[net.Conn]*ipcClientConn),
		tokens:    newIPCTokenStore(),
		OnMessage: onMessage,
	}
}

// attachIPCTestPeer sobe um handleConn sobre net.Pipe no servidor informado,
// com a identidade fixa. A validação real de pipe não roda sobre net.Pipe.
// Cada peer é iniciado após o hello do anterior nos testes multi-conexão, então
// o stub global não é disputado.
func attachIPCTestPeer(t *testing.T, server *IPCServer, admin bool, identity ipcAuthIdentity) *ipcTestPeer {
	t.Helper()
	id := identity
	old := ipcCallerValidator
	ipcCallerValidator = func(net.Conn) (ipcAuthIdentity, error) { return id, nil }
	t.Cleanup(func() { ipcCallerValidator = old })

	serverEnd, clientEnd := net.Pipe()
	go server.handleConn(serverEnd, admin)
	t.Cleanup(func() { clientEnd.Close() })
	return &ipcTestPeer{server: server, conn: clientEnd, reader: bufio.NewReader(clientEnd)}
}

// startIPCTestPeer sobe um handleConn sobre net.Pipe com identidade fixa.
func startIPCTestPeer(t *testing.T, admin bool, identity ipcAuthIdentity, onMessage func(net.Conn, IPCMessage)) *ipcTestPeer {
	t.Helper()
	server := newIPCTestServer(onMessage)
	return attachIPCTestPeer(t, server, admin, identity)
}

// readMaybe tenta ler um frame com deadline e devolve o erro em vez de falhar
// (usado para provar que NADA foi entregue/recebido).
func (p *ipcTestPeer) readMaybe(timeout time.Duration) (IPCMessage, error) {
	p.conn.SetReadDeadline(time.Now().Add(timeout))
	return DecodeIPCMessage(p.reader)
}

// ipcTestAddr/ipcDiscardConn servem para gravar respostas sem bloquear no pipe.
type ipcTestAddr string

func (a ipcTestAddr) Network() string { return "test" }
func (a ipcTestAddr) String() string  { return string(a) }

type ipcDiscardConn struct{}

func (*ipcDiscardConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (*ipcDiscardConn) Write(p []byte) (int, error)      { return len(p), nil }
func (*ipcDiscardConn) Close() error                     { return nil }
func (*ipcDiscardConn) LocalAddr() net.Addr              { return ipcTestAddr("local") }
func (*ipcDiscardConn) RemoteAddr() net.Addr             { return ipcTestAddr("remote") }
func (*ipcDiscardConn) SetDeadline(time.Time) error      { return nil }
func (*ipcDiscardConn) SetReadDeadline(time.Time) error  { return nil }
func (*ipcDiscardConn) SetWriteDeadline(time.Time) error { return nil }

// ipcUniqueConn é um net.Conn de descarte com identidade PRÓPRIA: net.Conn é a
// chave do mapa de conexões do servidor e structs vazias (ipcDiscardConn)
// compartilham o mesmo endereço em Go — dois &ipcDiscardConn{} seriam a MESMA
// chave de mapa e uma conexão sobrescreveria a outra.
type ipcUniqueConn struct {
	ipcDiscardConn
	id uint32
}

// testUIImageIdentity monta a identidade como o validador real faz: SID/PID/
// sessão + caminho da imagem e o veredito UIImage calculado pelo caminho.
func testUIImageIdentity(session uint32, imagePath string) ipcAuthIdentity {
	id := testIdentity(session)
	id.ImagePath = imagePath
	id.UIImage = ipcIsDiscoveryUIImagePath(imagePath, ipcAllowedUIDirectories())
	return id
}

func (p *ipcTestPeer) send(t *testing.T, msg IPCMessage) {
	t.Helper()
	p.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := p.conn.Write(EncodeIPCMessage(msg)); err != nil {
		t.Fatalf("write IPC: %v", err)
	}
}

func (p *ipcTestPeer) read(t *testing.T) IPCMessage {
	t.Helper()
	p.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg, err := DecodeIPCMessage(p.reader)
	if err != nil {
		t.Fatalf("read IPC: %v", err)
	}
	return msg
}

func (p *ipcTestPeer) hello(t *testing.T) IPCMessage {
	t.Helper()
	p.send(t, NewIPCMessage(IPCMsgHello, map[string]any{"pid": 1, "protocol": IPCProtocolVersion}))
	return p.read(t)
}

func testIdentity(session uint32) ipcAuthIdentity {
	// UIImage=true: a identidade de teste representa a UI instalada; os testes
	// de restrição de chamador montam a identidade com testUIImageIdentity.
	return ipcAuthIdentity{
		SID:       fmt.Sprintf("S-1-5-21-%d", session),
		PID:       1000 + session,
		SessionID: session,
		Valid:     true,
		UIImage:   true,
	}
}

func TestIPCHelloAckHasVersionAndAdminToken(t *testing.T) {
	readPeer := startIPCTestPeer(t, false, testIdentity(2), func(net.Conn, IPCMessage) {})
	ack := readPeer.hello(t)
	if ack.Type != IPCMsgHelloAck {
		t.Fatalf("tipo esperado hello_ack, got %s", ack.Type)
	}
	if ack.Payload["version"] != buildinfo.Version {
		t.Fatalf("hello_ack (leitura) deveria carregar version=%q, got %v", buildinfo.Version, ack.Payload["version"])
	}
	if _, hasToken := ack.Payload["token"]; hasToken {
		t.Fatal("pipe de leitura NÃO deve emitir token")
	}

	adminPeer := startIPCTestPeer(t, true, testIdentity(2), func(net.Conn, IPCMessage) {})
	ack = adminPeer.hello(t)
	token, _ := ack.Payload["token"].(string)
	if len(token) != 64 {
		t.Fatalf("hello_ack admin deveria trazer token de 64 hex chars, got %q", token)
	}
	expiresAt, _ := ack.Payload["expiresAt"].(string)
	if _, err := time.Parse(time.RFC3339, expiresAt); err != nil {
		t.Fatalf("expiresAt inválido: %q (%v)", expiresAt, err)
	}
	if ack.Payload["version"] != buildinfo.Version {
		t.Fatalf("hello_ack admin deveria carregar version=%q, got %v", buildinfo.Version, ack.Payload["version"])
	}
}

func TestIPCMutatingWithoutTokenIsUnauthorized(t *testing.T) {
	var dispatched atomic.Int64
	peer := startIPCTestPeer(t, true, testIdentity(2), func(_ net.Conn, msg IPCMessage) {
		if msg.Type == IPCMsgRequest {
			dispatched.Add(1)
		}
	})
	peer.hello(t)
	unauthorizedBefore := ipcAuthStats.Unauthorized.Load()
	deniedBefore := ipcAuthStats.MutatingDenied.Load()
	peer.send(t, IPCMessage{
		Type:          IPCMsgRequest,
		CorrelationID: "c-sem-token",
		Payload:       map[string]any{"method": "debug:set", "config": map[string]any{}},
	})
	resp := peer.read(t)
	if resp.Type != IPCMsgResponse || resp.CorrelationID != "c-sem-token" {
		t.Fatalf("resposta inesperada: %+v", resp)
	}
	if resp.Payload["ok"] != false || resp.Payload["code"] != "ipc_unauthorized" || resp.Payload["error"] != "unauthorized" {
		t.Fatalf("sem token deveria retornar unauthorized, got %v", resp.Payload)
	}
	if dispatched.Load() != 0 {
		t.Fatal("request sem token NÃO pode ser despachado")
	}
	if ipcAuthStats.Unauthorized.Load() <= unauthorizedBefore {
		t.Fatal("contador Unauthorized deveria ter sido incrementado")
	}
	if ipcAuthStats.MutatingDenied.Load() <= deniedBefore {
		t.Fatal("contador MutatingDenied deveria ter sido incrementado")
	}
}

func TestIPCAdminConnectionWithoutValidatableCallerIsRefused(t *testing.T) {
	old := ipcCallerValidator
	ipcCallerValidator = func(net.Conn) (ipcAuthIdentity, error) {
		return ipcAuthIdentity{}, fmt.Errorf("sem handle de pipe")
	}
	t.Cleanup(func() { ipcCallerValidator = old })

	server := &IPCServer{
		conns:     make(map[net.Conn]*ipcClientConn),
		tokens:    newIPCTokenStore(),
		OnMessage: func(net.Conn, IPCMessage) {},
	}
	serverEnd, clientEnd := net.Pipe()
	defer clientEnd.Close()
	go server.handleConn(serverEnd, true)

	clientEnd.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = clientEnd.Write(EncodeIPCMessage(NewIPCMessage(IPCMsgHello, map[string]any{"pid": 1})))
	clientEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	if _, err := clientEnd.Read(buf); err == nil {
		t.Fatal("conexão admin sem chamador validável deveria ser FECHADA (sem hello_ack)")
	}
}

func TestIPCTokenFromAnotherConnectionIsUnauthorized(t *testing.T) {
	var dispatched atomic.Int64
	onMessage := func(_ net.Conn, msg IPCMessage) {
		if msg.Type == IPCMsgRequest {
			dispatched.Add(1)
		}
	}
	identity := testIdentity(2)
	first := startIPCTestPeer(t, true, identity, onMessage)
	ack := first.hello(t)
	token, _ := ack.Payload["token"].(string)

	second := startIPCTestPeer(t, true, identity, onMessage)
	second.hello(t)
	second.send(t, IPCMessage{
		Type:          IPCMsgRequest,
		CorrelationID: "c-outra-conn",
		Payload:       map[string]any{"method": "config:set", "apiServer": "x", "token": token},
	})
	resp := second.read(t)
	if resp.Payload["code"] != "ipc_unauthorized" {
		t.Fatalf("token de outra conexão deveria ser recusado, got %v", resp.Payload)
	}
	if dispatched.Load() != 0 {
		t.Fatal("request com token de outra conexão NÃO pode ser despachado")
	}
}

func TestIPCExpiredTokenIsUnauthorized(t *testing.T) {
	var dispatched atomic.Int64
	old := ipcCallerValidator
	ipcCallerValidator = func(net.Conn) (ipcAuthIdentity, error) { return testIdentity(2), nil }
	t.Cleanup(func() { ipcCallerValidator = old })

	server := &IPCServer{
		conns:  make(map[net.Conn]*ipcClientConn),
		tokens: newIPCTokenStore(),
		OnMessage: func(_ net.Conn, msg IPCMessage) {
			if msg.Type == IPCMsgRequest {
				dispatched.Add(1)
			}
		},
	}
	server.tokens.ttl = -time.Second // emite já expirado
	serverEnd, clientEnd := net.Pipe()
	defer clientEnd.Close()
	go server.handleConn(serverEnd, true)
	peer := &ipcTestPeer{server: server, conn: clientEnd, reader: bufio.NewReader(clientEnd)}

	ack := peer.hello(t)
	token, _ := ack.Payload["token"].(string)
	peer.send(t, IPCMessage{
		Type:          IPCMsgRequest,
		CorrelationID: "c-expirado",
		Payload:       map[string]any{"method": "memory:add", "content": "x", "token": token},
	})
	resp := peer.read(t)
	if resp.Payload["code"] != "ipc_unauthorized" {
		t.Fatalf("token expirado deveria ser recusado, got %v", resp.Payload)
	}
	if dispatched.Load() != 0 {
		t.Fatal("request com token expirado NÃO pode ser despachado")
	}
}

func TestIPCChannelGating(t *testing.T) {
	var dispatched atomic.Int64
	onMessage := func(_ net.Conn, msg IPCMessage) {
		if msg.Type == IPCMsgRequest {
			dispatched.Add(1)
		}
	}

	admin := startIPCTestPeer(t, true, testIdentity(2), onMessage)
	ack := admin.hello(t)
	token, _ := ack.Payload["token"].(string)
	admin.send(t, IPCMessage{
		Type:          IPCMsgRequest,
		CorrelationID: "c-leitura-admin",
		Payload:       map[string]any{"method": "status:pending_counts", "token": token},
	})
	resp := admin.read(t)
	if resp.Payload["code"] != "ipc_wrong_channel" {
		t.Fatalf("método de leitura no pipe admin deveria ser recusado, got %v", resp.Payload)
	}

	read := startIPCTestPeer(t, false, testIdentity(2), onMessage)
	read.hello(t)
	read.send(t, IPCMessage{
		Type:          IPCMsgRequest,
		CorrelationID: "c-mutante-leitura",
		Payload:       map[string]any{"method": "debug:set", "config": map[string]any{}, "token": token},
	})
	resp = read.read(t)
	if resp.Payload["code"] != "ipc_unauthorized" {
		t.Fatalf("método mutante no pipe de leitura deveria ser unauthorized, got %v", resp.Payload)
	}
	if dispatched.Load() != 0 {
		t.Fatal("nenhum dos requests acima pode chegar ao dispatch")
	}

	// Leitura no pipe de leitura continua funcionando (UI antiga).
	read.send(t, IPCMessage{
		Type:          IPCMsgRequest,
		CorrelationID: "c-leitura-ok",
		Payload:       map[string]any{"method": "status:pending_counts"},
	})
	deadline := time.Now().Add(2 * time.Second)
	for dispatched.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if dispatched.Load() == 0 {
		t.Fatal("método de leitura no pipe de leitura deveria ser despachado")
	}
}

func TestIPCRemoteSessionRequiresAuthenticatedAdmin(t *testing.T) {
	var dispatched atomic.Int64
	onMessage := func(_ net.Conn, msg IPCMessage) {
		if msg.Type == IPCMsgRemoteSession {
			dispatched.Add(1)
		}
	}
	identity := testIdentity(3)

	read := startIPCTestPeer(t, false, identity, onMessage)
	read.hello(t)
	read.send(t, NewIPCMessage(IPCMsgRemoteSession, map[string]any{"action": "start", "sessionId": "uuid"}))
	time.Sleep(30 * time.Millisecond)
	if dispatched.Load() != 0 {
		t.Fatal("remote_session no pipe de leitura NÃO pode ser despachado")
	}

	admin := startIPCTestPeer(t, true, identity, onMessage)
	ack := admin.hello(t)
	token, _ := ack.Payload["token"].(string)
	// Sem token no canal admin: recusado (nunca spawna worker).
	admin.send(t, NewIPCMessage(IPCMsgRemoteSession, map[string]any{
		"action": "start", "sessionId": "uuid",
	}))
	time.Sleep(20 * time.Millisecond)
	if dispatched.Load() != 0 {
		t.Fatal("remote_session sem token NÃO pode ser despachado")
	}
	admin.send(t, NewIPCMessage(IPCMsgRemoteSession, map[string]any{
		"action": "start", "sessionId": "uuid", "token": token,
	}))
	deadline := time.Now().Add(2 * time.Second)
	for dispatched.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if dispatched.Load() != 1 {
		t.Fatal("remote_session autenticado deveria ser despachado")
	}
	// pid divergente do token/identidade: recusado.
	admin.send(t, NewIPCMessage(IPCMsgRemoteSession, map[string]any{
		"action": "start", "sessionId": "uuid", "pid": float64(999999), "token": token,
	}))
	time.Sleep(30 * time.Millisecond)
	if dispatched.Load() != 1 {
		t.Fatal("remote_session com pid divergente NÃO pode ser despachado")
	}
}

func TestIPCNotificationRespondRequiresTargetSession(t *testing.T) {
	ipcNotificationTargets.mu.Lock()
	ipcNotificationTargets.entries = map[string]ipcNotificationTarget{}
	ipcNotificationTargets.mu.Unlock()
	ipcRememberNotificationTarget("n-42", testIdentity(7), false)

	var dispatched atomic.Int64
	onMessage := func(_ net.Conn, msg IPCMessage) {
		if msg.Type == IPCMsgNotificationRespond {
			dispatched.Add(1)
		}
	}

	owner := startIPCTestPeer(t, false, testIdentity(7), onMessage)
	owner.hello(t)
	owner.send(t, NewIPCMessage(IPCMsgNotificationRespond, map[string]any{"notificationId": "n-42", "result": "approved"}))
	deadline := time.Now().Add(2 * time.Second)
	for dispatched.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if dispatched.Load() != 1 {
		t.Fatal("resposta da sessão destinatária deveria ser aceita")
	}

	other := startIPCTestPeer(t, false, testIdentity(8), onMessage)
	other.hello(t)
	other.send(t, NewIPCMessage(IPCMsgNotificationRespond, map[string]any{"notificationId": "n-42", "result": "approved"}))
	time.Sleep(30 * time.Millisecond)
	if dispatched.Load() != 1 {
		t.Fatal("resposta de OUTRA sessão NÃO pode ser aceita")
	}
}

func TestIPCServerRateLimitResponse(t *testing.T) {
	var server *IPCServer
	server = &IPCServer{
		conns:  make(map[net.Conn]*ipcClientConn),
		tokens: newIPCTokenStore(),
		OnMessage: func(conn net.Conn, msg IPCMessage) {
			if msg.Type != IPCMsgRequest {
				return
			}
			// Responde imediatamente para o teste poder medir 51 requests em 1s.
			server.RespondTo(conn, IPCMessage{
				Type:          IPCMsgResponse,
				CorrelationID: msg.CorrelationID,
				Payload:       map[string]any{"ok": true},
			})
		},
	}
	old := ipcCallerValidator
	ipcCallerValidator = func(net.Conn) (ipcAuthIdentity, error) { return testIdentity(2), nil }
	t.Cleanup(func() { ipcCallerValidator = old })
	serverEnd, clientEnd := net.Pipe()
	defer clientEnd.Close()
	go server.handleConn(serverEnd, false)
	peer := &ipcTestPeer{server: server, conn: clientEnd, reader: bufio.NewReader(clientEnd)}
	peer.hello(t)

	var rateLimited int
	for i := 0; i < ipcMaxRequestsPerSecond+1; i++ {
		cid := fmt.Sprintf("c-%d", i)
		peer.send(t, IPCMessage{
			Type:          IPCMsgRequest,
			CorrelationID: cid,
			Payload:       map[string]any{"method": "status:pending_counts"},
		})
		resp := peer.read(t)
		if resp.Payload["code"] == "ipc_rate_limited" {
			rateLimited++
		}
	}
	if rateLimited != 1 {
		t.Fatalf("esperado exatamente 1 resposta de rate limit no 51º request, got %d", rateLimited)
	}
}

func TestIPCServerInFlightLimit(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Int64
	peer := startIPCTestPeer(t, false, testIdentity(2), func(_ net.Conn, msg IPCMessage) {
		if msg.Type == IPCMsgRequest {
			started.Add(1)
		}
		<-release
	})

	// Envia só o limite e espera os handlers entrarem antes do excedente: o
	// read-loop do servidor é assíncrono, então enviar o 9º "junto" podia ser
	// classificado antes de todos os 8 estarem em voo (teste instável sob carga).
	for i := 0; i < ipcMaxRequestsInFlight; i++ {
		peer.send(t, IPCMessage{
			Type:          IPCMsgRequest,
			CorrelationID: fmt.Sprintf("inflight-%d", i),
			Payload:       map[string]any{"method": "status:pending_counts"},
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for started.Load() < int64(ipcMaxRequestsInFlight) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if started.Load() != int64(ipcMaxRequestsInFlight) {
		t.Fatalf("esperado %d dispatches em voo, got %d", ipcMaxRequestsInFlight, started.Load())
	}

	peer.send(t, IPCMessage{
		Type:          IPCMsgRequest,
		CorrelationID: "inflight-overflow",
		Payload:       map[string]any{"method": "status:pending_counts"},
	})
	resp := peer.read(t)
	if resp.Payload["code"] != "ipc_inflight_limit" {
		t.Fatalf("request além do limite em voo deveria ser recusado, got %v", resp.Payload)
	}
	close(release)
}

func TestIPCServerDedupCID(t *testing.T) {
	var dispatched atomic.Int64
	var server *IPCServer
	server = &IPCServer{
		conns:  make(map[net.Conn]*ipcClientConn),
		tokens: newIPCTokenStore(),
		OnMessage: func(conn net.Conn, msg IPCMessage) {
			if msg.Type != IPCMsgRequest {
				return
			}
			dispatched.Add(1)
			resp := map[string]any{"ok": true, "data": "uma-vez"}
			server.recordRequestResult(conn, msg.CorrelationID, resp)
			server.RespondTo(conn, IPCMessage{Type: IPCMsgResponse, CorrelationID: msg.CorrelationID, Payload: resp})
		},
	}
	old := ipcCallerValidator
	ipcCallerValidator = func(net.Conn) (ipcAuthIdentity, error) { return testIdentity(2), nil }
	t.Cleanup(func() { ipcCallerValidator = old })

	serverEnd, clientEnd := net.Pipe()
	defer clientEnd.Close()
	go server.handleConn(serverEnd, false)
	peer := &ipcTestPeer{server: server, conn: clientEnd, reader: bufio.NewReader(clientEnd)}
	peer.hello(t)

	for i := 0; i < 2; i++ {
		peer.send(t, IPCMessage{
			Type:          IPCMsgRequest,
			CorrelationID: "cid-repetido",
			Payload:       map[string]any{"method": "status:pending_counts"},
		})
		resp := peer.read(t)
		if resp.Payload["data"] != "uma-vez" {
			t.Fatalf("resposta %d inesperada: %v", i+1, resp.Payload)
		}
	}
	if dispatched.Load() != 1 {
		t.Fatalf("cid duplicado deveria ser executado 1 vez, got %d", dispatched.Load())
	}
}

// ── C1: framing sob concorrência ───────────────────────────────────────────

type chunkedConn struct {
	net.Conn
}

func (c *chunkedConn) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		written, err := c.Conn.Write(p[:1])
		total += written
		if err != nil {
			return total, err
		}
		p = p[1:]
		runtime.Gosched()
	}
	return total, nil
}

func TestIPCClientSendConcurrentFraming(t *testing.T) {
	clientEnd, peerEnd := net.Pipe()
	defer peerEnd.Close()
	client := &IPCClient{
		conn:            &chunkedConn{Conn: clientEnd},
		pendingRequests: make(map[string]chan IPCMessage),
		closedCh:        make(chan struct{}),
		adminHelloCh:    make(chan IPCServiceHelloAck, 1),
	}
	defer client.Close()

	const total = 20
	readDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(peerEnd)
		seen := make(map[string]bool, total)
		for i := 0; i < total; i++ {
			msg, err := DecodeIPCMessage(reader)
			if err != nil {
				readDone <- fmt.Errorf("frame %d corrompido: %w", i, err)
				return
			}
			if msg.Type != IPCMsgRequest {
				readDone <- fmt.Errorf("frame %d com tipo inesperado: %s", i, msg.Type)
				return
			}
			seen[msg.CorrelationID] = true
		}
		if len(seen) != total {
			readDone <- fmt.Errorf("esperado %d cids únicos, got %d", total, len(seen))
			return
		}
		readDone <- nil
	}()

	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cid := fmt.Sprintf("concorrente-%d", n)
			if err := client.Send(IPCMessage{
				Type:          IPCMsgRequest,
				CorrelationID: cid,
				Payload:       map[string]any{"method": "status:pending_counts", "n": n},
			}); err != nil {
				t.Errorf("Send %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout lendo os 20 frames")
	}
}

func TestIPCClientAdminRequestInjectsToken(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	client := &IPCClient{
		adminConn:       clientEnd,
		adminRx:         bufio.NewReader(clientEnd),
		adminToken:      "token-de-teste",
		adminExpires:    time.Now().Add(time.Hour),
		pendingRequests: make(map[string]chan IPCMessage),
		closedCh:        make(chan struct{}),
		adminHelloCh:    make(chan IPCServiceHelloAck, 1),
	}
	go client.adminReadLoop(clientEnd, client.adminRx, make(chan struct{}))
	defer client.Close()

	seen := make(chan map[string]any, 1)
	go func() {
		reader := bufio.NewReader(serverEnd)
		msg, err := DecodeIPCMessage(reader)
		if err != nil {
			seen <- nil
			return
		}
		seen <- msg.Payload
		serverEnd.Write(EncodeIPCMessage(IPCMessage{
			Type:          IPCMsgResponse,
			CorrelationID: msg.CorrelationID,
			Payload:       map[string]any{"ok": true, "data": map[string]any{"saved": true}},
		}))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := client.Request(ctx, "config:set", map[string]any{"apiServer": "https://x"})
	if err != nil {
		t.Fatalf("Request admin: %v", err)
	}
	if resp["ok"] != true {
		t.Fatalf("resposta inesperada: %v", resp)
	}
	payload := <-seen
	if payload == nil {
		t.Fatal("nenhum request chegou ao canal admin")
	}
	if payload["token"] != "token-de-teste" {
		t.Fatalf("request mutante deveria carregar o token, got %v", payload["token"])
	}
	if payload["method"] != "config:set" {
		t.Fatalf("método inesperado: %v", payload["method"])
	}
}

func TestIPCClientRemoteSessionUsesAdminChannel(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	client := &IPCClient{
		adminConn:       clientEnd,
		adminRx:         bufio.NewReader(clientEnd),
		adminToken:      "token-remoto",
		adminExpires:    time.Now().Add(time.Hour),
		pendingRequests: make(map[string]chan IPCMessage),
		closedCh:        make(chan struct{}),
		adminHelloCh:    make(chan IPCServiceHelloAck, 1),
	}
	defer client.Close()

	payloads := make(chan map[string]any, 1)
	go func() {
		msg, err := DecodeIPCMessage(bufio.NewReader(serverEnd))
		if err != nil {
			payloads <- nil
			return
		}
		payloads <- msg.Payload
	}()

	if err := client.Send(NewIPCMessage(IPCMsgRemoteSession, map[string]any{"action": "start", "sessionId": "uuid"})); err != nil {
		t.Fatalf("Send remote_session: %v", err)
	}
	payload := <-payloads
	if payload == nil || payload["token"] != "token-remoto" {
		t.Fatalf("remote_session deveria ir pelo admin com token, got %v", payload)
	}
}

func TestIPCClientHelloAckFromPayload(t *testing.T) {
	ack := helloAckFromPayload(map[string]any{
		"service":   ServiceName,
		"protocol":  float64(2),
		"version":   "1.2.3",
		"token":     "abc",
		"expiresAt": "2030-01-02T03:04:05Z",
	})
	if ack.Protocol != 2 || ack.Version != "1.2.3" || ack.Token != "abc" {
		t.Fatalf("hello_ack decodificado incorretamente: %+v", ack)
	}
	if ack.ExpiresAt.IsZero() {
		t.Fatal("expiresAt deveria ser parseado")
	}
	// Compat: serviço antigo sem protocol/version/token.
	old := helloAckFromPayload(map[string]any{"service": ServiceName})
	if old.Protocol != 1 || old.Version != "" || old.Token != "" {
		t.Fatalf("compat com serviço antigo quebrada: %+v", old)
	}
}

// ── A.0.1: token admin só para a UI instalada ──────────────────────────────

// captureIPCStructuredLogs troca o logger estruturado para inspecionar as
// linhas emitidas e devolve um acessor concorrente-seguro.
func captureIPCStructuredLogs(t *testing.T) func() []string {
	t.Helper()
	old := ipcStructuredLogger
	var mu sync.Mutex
	var msgs []string
	ipcStructuredLogger = func(scope, message string) {
		mu.Lock()
		msgs = append(msgs, scope+" "+message)
		mu.Unlock()
	}
	t.Cleanup(func() { ipcStructuredLogger = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(msgs))
		copy(out, msgs)
		return out
	}
}

// isolateUIDirs fixa os diretórios de instalação aceitos num temp dir, para o
// teste não depender do Program Files da máquina.
func isolateUIDirs(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("ProgramFiles", base)
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LOCALAPPDATA", "")
	return base
}

func TestIPCAdminTokenOnlyForDiscoveryUIImage(t *testing.T) {
	base := isolateUIDirs(t)
	validImage := filepath.Join(base, "Discovery", ipcUIExecutableName)

	// Caminho válido (discovery-agent.exe sob <ProgramFiles>/Discovery): emite
	// token e carrega identidade/sessão no hello_ack.
	admin := startIPCTestPeer(t, true, testUIImageIdentity(2, validImage), func(net.Conn, IPCMessage) {})
	ack := admin.hello(t)
	if token, _ := ack.Payload["token"].(string); len(token) != 64 {
		t.Fatalf("UI instalada deveria receber token, got %q", token)
	}
	if ack.Payload["sessionId"] != float64(2) || ack.Payload["sid"] != "S-1-5-21-2" {
		t.Fatalf("hello_ack deveria trazer sessionId/sid, got %v", ack.Payload)
	}

	// Outro executável (fora dos diretórios/ nome errado): NÃO emite token,
	// responde hello_ack com erro e FECHA a conexão.
	before := ipcAuthStats.CallerNotAllowed.Load()
	foreign := startIPCTestPeer(t, true, testUIImageIdentity(3, filepath.Join(base, "Outros", "outro.exe")), func(net.Conn, IPCMessage) {})
	ack = foreign.hello(t)
	if _, hasToken := ack.Payload["token"]; hasToken {
		t.Fatal("executável que não é a UI NÃO pode receber token admin")
	}
	if ack.Payload["code"] != "ipc_caller_not_allowed" {
		t.Fatalf("hello_ack deveria sinalizar ipc_caller_not_allowed, got %v", ack.Payload)
	}
	if ack.Payload["sessionId"] != float64(3) {
		t.Fatalf("identidade/sessão continuam no hello_ack, got %v", ack.Payload)
	}
	if _, err := foreign.readMaybe(500 * time.Millisecond); err == nil {
		t.Fatal("conexão admin de chamador não autorizado deveria ser FECHADA após o hello_ack")
	}
	if ipcAuthStats.CallerNotAllowed.Load() != before+1 {
		t.Fatal("contador CallerNotAllowed deveria registrar a recusa")
	}

	// Mesmo nome, diretório parecido ("Discovery2") também não casa.
	if ipcIsDiscoveryUIImagePath(filepath.Join(base, "Discovery2", ipcUIExecutableName), ipcAllowedUIDirectories()) {
		t.Fatal("diretório Discovery2 não pode ser aceito como Discovery")
	}
}

func TestIPCAdminCallerBypassEmitsTokenAndLogs(t *testing.T) {
	base := isolateUIDirs(t)
	t.Setenv(ipcCallerBypassEnv, "1")
	logs := captureIPCStructuredLogs(t)

	admin := startIPCTestPeer(t, true, testUIImageIdentity(4, filepath.Join(base, "Outros", "outro.exe")), func(net.Conn, IPCMessage) {})
	ack := admin.hello(t)
	if token, _ := ack.Payload["token"].(string); len(token) != 64 {
		t.Fatalf("bypass de dev deveria emitir token, got %q", token)
	}
	found := false
	for _, line := range logs() {
		if strings.Contains(line, "caller validation bypassed (dev)") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("bypass deveria ser LOGADO; logs=%v", logs())
	}
}

// ── A.3.2/A.3.5: identidade nos dois canais e no handshake ─────────────────

func TestIPCHelloAckCarriesSessionIdentityOnBothChannels(t *testing.T) {
	read := startIPCTestPeer(t, false, testIdentity(6), func(net.Conn, IPCMessage) {})
	ack := read.hello(t)
	if ack.Payload["sessionId"] != float64(6) || ack.Payload["sid"] != "S-1-5-21-6" {
		t.Fatalf("canal de leitura deveria trazer sessionId/sid, got %v", ack.Payload)
	}
	if _, hasToken := ack.Payload["token"]; hasToken {
		t.Fatal("canal de leitura não emite token")
	}

	admin := startIPCTestPeer(t, true, testIdentity(9), func(net.Conn, IPCMessage) {})
	ack = admin.hello(t)
	if ack.Payload["sessionId"] != float64(9) || ack.Payload["sid"] != "S-1-5-21-9" {
		t.Fatalf("canal admin deveria trazer sessionId/sid, got %v", ack.Payload)
	}
}

func TestIPCReadChannelNonUICallerStaysUnauthenticated(t *testing.T) {
	base := isolateUIDirs(t)
	peer := startIPCTestPeer(t, false, testUIImageIdentity(5, filepath.Join(base, "Outros", "outro.exe")), func(net.Conn, IPCMessage) {})
	ack := peer.hello(t)
	if ack.Type != IPCMsgHelloAck {
		t.Fatal("probe/UI antiga de processo não-UI precisa continuar recebendo hello_ack")
	}
	// A conexão existe, mas NÃO conta como autenticada (identityOK=false).
	peer.server.mu.Lock()
	var client *ipcClientConn
	for _, c := range peer.server.conns {
		client = c
	}
	peer.server.mu.Unlock()
	if client == nil {
		t.Fatal("conexão de leitura deveria estar registrada")
	}
	if client.identityOK {
		t.Fatal("chamador não-UI no canal de leitura não pode ser identityOK")
	}
	if client.identity.SessionID != 5 {
		t.Fatalf("sessão deveria continuar registrada para auditoria, got %d", client.identity.SessionID)
	}
}

// ── A.3.5: entrega de eventos por sessão ───────────────────────────────────

func TestBroadcastIPCEventTargetsNotificationSession(t *testing.T) {
	ipcNotificationTargets.mu.Lock()
	ipcNotificationTargets.entries = map[string]ipcNotificationTarget{}
	ipcNotificationTargets.mu.Unlock()

	oldProvider := ipcActiveSessionProvider
	ipcActiveSessionProvider = func() ipcAuthIdentity { return testIdentity(7) }
	t.Cleanup(func() { ipcActiveSessionProvider = oldProvider })

	server := newIPCTestServer(func(net.Conn, IPCMessage) {})
	peer7 := attachIPCTestPeer(t, server, false, testIdentity(7))
	peer7.hello(t)
	peer8 := attachIPCTestPeer(t, server, false, testIdentity(8))
	peer8.hello(t)
	// Sessão 7 sem identidade autenticada: nunca pode receber evento targeted.
	unauth7 := attachIPCTestPeer(t, server, false, testUIImageIdentity(7, filepath.Join(t.TempDir(), "x.exe")))
	unauth7.hello(t)

	a := &App{ipcServer: server}
	// net.Pipe não tem buffer: o envio só conclui com o leitor ativo, então o
	// broadcast roda em goroutine e o teste mede entrega/não-entrega em paralelo.
	done := make(chan struct{})
	go func() {
		a.broadcastIPCEvent("notification:new", map[string]any{"id": "n-target", "title": "Instalação"})
		close(done)
	}()

	msg := peer7.read(t)
	if msg.Type != IPCMsgEvent || msg.Payload["name"] != "notification:new" {
		t.Fatalf("sessão alvo deveria receber o evento, got %+v", msg)
	}
	if msg.Payload["sessionId"] != float64(7) || msg.Payload["sid"] != "S-1-5-21-7" {
		t.Fatalf("evento targeted deveria carregar a sessão alvo, got %v", msg.Payload)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("entrega targeted travou (escrita para conexão fora da sessão alvo?)")
	}
	if _, err := peer8.readMaybe(300 * time.Millisecond); err == nil {
		t.Fatal("conexão de OUTRA sessão não pode receber o evento targeted")
	}
	if _, err := unauth7.readMaybe(100 * time.Millisecond); err == nil {
		t.Fatal("conexão não autenticada nunca recebe evento targeted")
	}

	// supportchat:invite segue a mesma regra.
	done = make(chan struct{})
	go func() {
		a.broadcastIPCEvent("supportchat:invite", "sessionId", "chat-1", "ticketId", "t-1")
		close(done)
	}()
	invite := peer7.read(t)
	if invite.Payload["name"] != "supportchat:invite" {
		t.Fatalf("sessão alvo deveria receber o convite, got %+v", invite)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("entrega do convite travou")
	}
	if _, err := peer8.readMaybe(300 * time.Millisecond); err == nil {
		t.Fatal("convite não pode vazar para outra sessão")
	}
}

func TestBroadcastIPCEventFallsBackWhenNoSessionTarget(t *testing.T) {
	ipcNotificationTargets.mu.Lock()
	ipcNotificationTargets.entries = map[string]ipcNotificationTarget{}
	ipcNotificationTargets.mu.Unlock()

	oldProvider := ipcActiveSessionProvider
	ipcActiveSessionProvider = func() ipcAuthIdentity { return ipcAuthIdentity{} }
	t.Cleanup(func() { ipcActiveSessionProvider = oldProvider })

	server := newIPCTestServer(func(net.Conn, IPCMessage) {})
	// Chamadores não-UI: identityOK=false → currentNotificationTarget não tem
	// alvo conhecido; a compatibilidade exige Broadcast.
	peerA := attachIPCTestPeer(t, server, false, testUIImageIdentity(1, filepath.Join(t.TempDir(), "a.exe")))
	peerA.hello(t)
	peerB := attachIPCTestPeer(t, server, false, testUIImageIdentity(2, filepath.Join(t.TempDir(), "b.exe")))
	peerB.hello(t)

	a := &App{ipcServer: server}
	// Broadcast escreve conn a conn e net.Pipe bloqueia até o leitor: os dois
	// leitores precisam estar ativos ANTES do envio.
	type readResult struct {
		msg IPCMessage
		err error
	}
	readOne := func(peer *ipcTestPeer) <-chan readResult {
		ch := make(chan readResult, 1)
		go func() {
			peer.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			msg, err := DecodeIPCMessage(peer.reader)
			ch <- readResult{msg: msg, err: err}
		}()
		return ch
	}
	chA := readOne(peerA)
	chB := readOne(peerB)
	done := make(chan struct{})
	go func() {
		a.broadcastIPCEvent("notification:new", map[string]any{"id": "n-fallback"})
		close(done)
	}()

	for _, ch := range []<-chan readResult{chA, chB} {
		res := <-ch
		if res.err != nil {
			t.Fatalf("broadcast deveria chegar às duas UIs sem sessão alvo: %v", res.err)
		}
		if res.msg.Payload["name"] != "notification:new" || res.msg.Payload["id"] != "n-fallback" {
			t.Fatalf("sem sessão alvo o evento deve ir em broadcast, got %+v", res.msg.Payload)
		}
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast não concluiu")
	}
}

// ── A.3.2/DoS: teto e ociosidade do canal admin ────────────────────────────

func TestIPCServerAdminConnectionCap(t *testing.T) {
	server := newIPCTestServer(func(net.Conn, IPCMessage) {})
	for i := 0; i < ipcMaxAdminConnections; i++ {
		conn, peer := net.Pipe()
		t.Cleanup(func() { conn.Close(); peer.Close() })
		server.conns[conn] = &ipcClientConn{
			reader:     bufio.NewReader(conn),
			admin:      true,
			identity:   testIdentity(uint32(i + 1)),
			identityOK: true,
		}
	}
	before := ipcAuthStats.AdminConnRefused.Load()
	extra := attachIPCTestPeer(t, server, true, testIdentity(20))
	if _, err := extra.readMaybe(500 * time.Millisecond); err == nil {
		t.Fatal("9ª conexão admin deveria ser RECUSADA")
	}
	if ipcAuthStats.AdminConnRefused.Load() != before+1 {
		t.Fatal("recusa do teto admin deveria ser contada")
	}
	if got := server.adminConnCount(); got != ipcMaxAdminConnections {
		t.Fatalf("teto admin deveria permanecer em %d, got %d", ipcMaxAdminConnections, got)
	}
}

func TestIPCServerSweepIdleConnsAdminTimeout(t *testing.T) {
	if ipcAdminIdleTimeout != 5*time.Minute {
		t.Fatalf("ociosidade do admin deveria ser 5min, got %s", ipcAdminIdleTimeout)
	}
	server := &IPCServer{
		conns:            make(map[net.Conn]*ipcClientConn),
		adminIdleTimeout: 50 * time.Millisecond, // injetável/curto no teste
	}
	adminConn, adminPeer := net.Pipe()
	defer adminConn.Close()
	defer adminPeer.Close()
	readConn, readPeer := net.Pipe()
	defer readConn.Close()
	defer readPeer.Close()

	admin := &ipcClientConn{reader: bufio.NewReader(adminConn), admin: true}
	admin.touch()
	admin.lastActivity.Store(time.Now().Add(-100 * time.Millisecond).UnixMilli())
	read := &ipcClientConn{reader: bufio.NewReader(readConn)}
	read.touch()

	server.conns[adminConn] = admin
	server.conns[readConn] = read

	if dropped := server.sweepIdleConns(time.Now()); dropped != 1 {
		t.Fatalf("admin ocioso deveria ser derrubado; derrubou %d", dropped)
	}
	if _, ok := server.conns[adminConn]; ok {
		t.Fatal("conexão admin ociosa precisa sair do mapa")
	}
	if _, ok := server.conns[readConn]; !ok {
		t.Fatal("conexão de leitura saudável não pode ser derrubada pelo teto do admin")
	}
}

// ── Hello: rate limit próprio ──────────────────────────────────────────────

func TestIPCHelloRateLimitClosesConnection(t *testing.T) {
	peer := startIPCTestPeer(t, false, testIdentity(2), func(net.Conn, IPCMessage) {})
	for i := 0; i < ipcMaxHellosPerSecond; i++ {
		if ack := peer.hello(t); ack.Type != IPCMsgHelloAck {
			t.Fatalf("hello %d deveria ser respondido, got %s", i+1, ack.Type)
		}
	}
	before := ipcAuthStats.HelloRateLimited.Load()
	peer.conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = peer.conn.Write(EncodeIPCMessage(NewIPCMessage(IPCMsgHello, map[string]any{"pid": 1, "protocol": IPCProtocolVersion})))
	if _, err := peer.readMaybe(500 * time.Millisecond); err == nil {
		t.Fatalf("rajada de hello além de %d/s deveria fechar a conexão", ipcMaxHellosPerSecond)
	}
	if ipcAuthStats.HelloRateLimited.Load() != before+1 {
		t.Fatal("rate limit de hello deveria ser contado")
	}
}

// ── A.0.8: desfecho real do request mutante ────────────────────────────────

func TestIPCMutatingOutcomeAuditOnFailure(t *testing.T) {
	logs := captureIPCStructuredLogs(t)
	identity := testIdentity(2)
	conn := &ipcDiscardConn{}
	client := &ipcClientConn{
		admin:      true,
		connKey:    "conn-outcome",
		identity:   identity,
		identityOK: true,
		limiter:    newIPCRequestLimiter(),
		dedup:      newIPCDedupCache(),
	}
	server := newIPCTestServer(func(net.Conn, IPCMessage) {})
	server.conns[conn] = client
	token, _, err := server.tokens.issue(client.connKey, identity)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	a := &App{ipcServer: server}
	before := ipcAuthStats.MutatingFailed.Load()
	// updates:scan é mutante e FALHA deterministicamente sem UpdatesSvc.
	a.handleIPCMessage(conn, IPCMessage{
		Type:          IPCMsgRequest,
		CorrelationID: "c-outcome",
		Payload:       map[string]any{"method": "updates:scan", "token": token},
	})
	if got := ipcAuthStats.MutatingFailed.Load(); got != before+1 {
		t.Fatalf("falha mutante deveria incrementar MutatingFailed (%d -> %d)", before, got)
	}
	found := false
	for _, line := range logs() {
		if strings.Contains(line, "mutating_outcome") &&
			strings.Contains(line, "method=\"updates:scan\"") &&
			strings.Contains(line, "result=error") &&
			strings.Contains(line, "sessionId=2") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("desfecho do mutante deveria ser logado com sid/pid/sessionId/method/result; logs=%v", logs())
	}
}

// ── A.0.1: âncoras do diretório de instalação da UI ────────────────────────

// O instalador permite escolher o diretório (MUI_PAGE_DIRECTORY) e o watchdog
// lança a UI como IRMÃ do binário do serviço. A validação precisa aceitar esse
// diretório (antes um install fora de %ProgramFiles%\Discovery deixava a UI sem
// token admin e TODAS as mutações falhavam) e NÃO pode aceitar caminho gravável
// pelo usuário (%LOCALAPPDATA%).
func TestIPCAllowedUIDirectoriesAnchorOnServiceDir(t *testing.T) {
	base := isolateUIDirs(t) // ProgramFiles=<base>; (x86)/LOCALAPPDATA vazios

	custom := filepath.Join(t.TempDir(), "Apps", "Discovery")
	oldExec := ipcServiceExecutablePath
	ipcServiceExecutablePath = func() (string, error) {
		return filepath.Join(custom, "discovery-service.exe"), nil
	}
	t.Cleanup(func() { ipcServiceExecutablePath = oldExec })

	roots := ipcAllowedUIDirectories()
	if !ipcIsDiscoveryUIImagePath(filepath.Join(custom, ipcUIExecutableName), roots) {
		t.Fatal("UI ao lado do binário do serviço (install customizado) deveria ser aceita")
	}
	if !ipcIsDiscoveryUIImagePath(filepath.Join(base, "Discovery", ipcUIExecutableName), roots) {
		t.Fatal("diretório padrão do instalador deveria continuar aceito")
	}

	local := filepath.Join(t.TempDir(), "AppData", "Local")
	t.Setenv("LOCALAPPDATA", local)
	if ipcIsDiscoveryUIImagePath(filepath.Join(local, "Programs", "Discovery", ipcUIExecutableName), ipcAllowedUIDirectories()) {
		t.Fatal("caminho gravável pelo usuário (%LOCALAPPDATA%/Programs/Discovery) não pode ser aceito")
	}

	if ipcIsDiscoveryUIImagePath(filepath.Join(base, "Discovery", "bin", ipcUIExecutableName), ipcAllowedUIDirectories()) {
		t.Fatal("subdiretório não é o diretório de instalação da UI (ela fica ao lado do serviço)")
	}
	if ipcIsDiscoveryUIImagePath(filepath.Join(base, "Discovery", "..", "Outros", ipcUIExecutableName), ipcAllowedUIDirectories()) {
		t.Fatal("caminho com '..' resolvido para fora do diretório de instalação não pode ser aceito")
	}
	if ipcIsDiscoveryUIImagePath(filepath.Join(custom, "outro.exe"), ipcAllowedUIDirectories()) {
		t.Fatal("executável com nome diferente NÃO é a UI, mesmo dentro do diretório de instalação")
	}
}

// ── Bypass de dev nos DOIS canais ─────────────────────────────────────────

// O bypass de desenvolvimento (DISCOVERY_IPC_ALLOW_ANY_CALLER=1) existia só no
// canal admin: a build de dev (executável FORA do diretório de instalação)
// conseguia mutar, mas ficava sem entrega targeted, sem poder responder prompt e
// fora da contagem de UIs online. O bypass precisa valer para a identidade da UI
// nos dois canais (sem ele, tudo continua fail-closed).
func TestIPCReadChannelBypassAuthenticatesDevUI(t *testing.T) {
	base := isolateUIDirs(t)
	t.Setenv(ipcCallerBypassEnv, "1")

	ipcNotificationTargets.mu.Lock()
	ipcNotificationTargets.entries = map[string]ipcNotificationTarget{}
	ipcNotificationTargets.mu.Unlock()
	ipcRememberNotificationTarget("n-dev", testIdentity(5), false)

	var dispatched atomic.Int64
	onMessage := func(_ net.Conn, msg IPCMessage) {
		if msg.Type == IPCMsgNotificationRespond {
			dispatched.Add(1)
		}
	}
	peer := startIPCTestPeer(t, false, testUIImageIdentity(5, filepath.Join(base, "Outros", "outro.exe")), onMessage)
	peer.hello(t)

	peer.server.mu.Lock()
	var client *ipcClientConn
	for _, c := range peer.server.conns {
		client = c
	}
	peer.server.mu.Unlock()
	if client == nil {
		t.Fatal("conexão de leitura deveria estar registrada")
	}
	if !client.identityOK {
		t.Fatal("bypass de dev deveria autenticar a UI de dev TAMBÉM no canal de leitura")
	}
	if got := peer.server.UIAuthenticatedCount(); got != 1 {
		t.Fatalf("UI de dev autenticada deveria contar como 1 UI online, got %d", got)
	}

	peer.send(t, NewIPCMessage(IPCMsgNotificationRespond, map[string]any{
		"notificationId": "n-dev", "result": "approved",
	}))
	deadline := time.Now().Add(2 * time.Second)
	for dispatched.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if dispatched.Load() != 1 {
		t.Fatal("com bypass, a resposta de prompt da UI de dev deveria ser aceita")
	}
}

// ── A3: contagem de UIs autenticadas (uiOnlineCount) ──────────────────────

// ClientCount conta CONEXÕES do canal de leitura, incluindo chamador não
// autenticado — o heartbeat usava esse número como "UIs online". A contagem
// correta é por PROCESSO autenticado: leitura+admin da mesma UI = 1 UI.
func TestIPCUIAuthenticatedCountCountsDistinctUIs(t *testing.T) {
	server := newIPCTestServer(func(net.Conn, IPCMessage) {})
	nextID := uint32(0)
	add := func(admin, authenticated bool, pid, session uint32) *ipcUniqueConn {
		nextID++
		conn := &ipcUniqueConn{id: nextID}
		server.conns[conn] = &ipcClientConn{
			reader:     bufio.NewReader(&ipcDiscardConn{}),
			admin:      admin,
			identity:   ipcAuthIdentity{SID: "S-1-5-21-x", PID: pid, SessionID: session, Valid: authenticated},
			identityOK: authenticated,
		}
		return conn
	}
	// Duas conexões de leitura da MESMA UI + a conexão admin dela.
	add(false, true, 100, 2)
	add(false, true, 100, 2)
	add(true, true, 100, 2)
	// Probe/ferramenta não autenticada na mesma sessão.
	add(false, false, 200, 2)

	if got := server.UIAuthenticatedCount(); got != 1 {
		t.Fatalf("duas conexões da mesma UI + admin = 1 UI; got %d", got)
	}
	if got := server.ClientCount(); got != 3 {
		t.Fatalf("ClientCount continua contando conexões de leitura (3), got %d", got)
	}
	// Entrega por sessão: as 2 conexões de leitura autenticadas recebem; a admin
	// (mesma UI) e a não autenticada NÃO — sem toast duplicado.
	msg := NewIPCMessage(IPCMsgEvent, map[string]any{"name": "notification:new"})
	if sent := server.SendToSession(2, msg); sent != 2 {
		t.Fatalf("entrega por sessão deveria ir só às conexões de leitura autenticadas (2), got %d", sent)
	}

	// Outra UI (outra sessão) conta separado.
	add(false, true, 300, 7)
	if got := server.UIAuthenticatedCount(); got != 2 {
		t.Fatalf("2 UIs autenticadas em sessões diferentes; got %d", got)
	}

	var nilServer *IPCServer
	if got := nilServer.UIAuthenticatedCount(); got != 0 {
		t.Fatalf("servidor nil deve devolver 0, got %d", got)
	}
}

// uiOnline bool precisa seguir a MESMA contagem: conexão não autenticada não é
// interface online (senão o serviço suprime o toast nativo sem ter quem renderize).
func TestHeartbeatUIOnlineCountsAuthenticatedUIs(t *testing.T) {
	server := newIPCTestServer(func(net.Conn, IPCMessage) {})
	a := NewApp(AppStartupOptions{ServiceMode: true})
	a.ipcServer = server

	m := a.getHeartbeatMetrics()
	if m.UIOnline == nil || m.UIOnlineCount == nil {
		t.Fatal("no modo serviço uiOnline/uiOnlineCount deveriam estar preenchidos")
	}
	if *m.UIOnline || *m.UIOnlineCount != 0 {
		t.Fatalf("sem UI conectada: uiOnline=%t count=%d", *m.UIOnline, *m.UIOnlineCount)
	}

	probeConn := &ipcUniqueConn{id: 1}
	server.conns[probeConn] = &ipcClientConn{
		reader:   bufio.NewReader(&ipcDiscardConn{}),
		identity: ipcAuthIdentity{SessionID: 2, Valid: true},
	}
	m = a.getHeartbeatMetrics()
	if *m.UIOnline || *m.UIOnlineCount != 0 {
		t.Fatalf("conexão não autenticada não pode marcar UI online: uiOnline=%t count=%d", *m.UIOnline, *m.UIOnlineCount)
	}

	uiConn := &ipcUniqueConn{id: 2}
	server.conns[uiConn] = &ipcClientConn{
		reader:     bufio.NewReader(&ipcDiscardConn{}),
		identity:   testIdentity(2),
		identityOK: true,
	}
	m = a.getHeartbeatMetrics()
	if !*m.UIOnline || *m.UIOnlineCount != 1 {
		t.Fatalf("UI autenticada: uiOnline=%t count=%d (esperado true/1)", *m.UIOnline, *m.UIOnlineCount)
	}
}

// Close concorrente com SendToSession/Broadcast: a entrega monta um snapshot das
// conexões (fora do lock), então fechar o servidor no meio não pode panicar,
// corromper o mapa nem entregar depois do fechamento. Roda sob -race.
func TestIPCSendToSessionConcurrentWithClose(t *testing.T) {
	server := newIPCTestServer(func(net.Conn, IPCMessage) {})
	for i := 0; i < 4; i++ {
		conn := &ipcUniqueConn{id: uint32(i + 1)}
		server.conns[conn] = &ipcClientConn{
			reader:     bufio.NewReader(&ipcDiscardConn{}),
			identity:   testIdentity(uint32(i + 1)),
			identityOK: true,
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			msg := NewIPCMessage(IPCMsgEvent, map[string]any{"name": "notification:new"})
			if n%2 == 0 {
				server.SendToSession(uint32(n%4+1), msg)
				return
			}
			server.Broadcast(msg)
		}(i)
	}
	server.Close()
	wg.Wait()

	if got := server.SendToSession(1, NewIPCMessage(IPCMsgEvent, map[string]any{"name": "late"})); got != 0 {
		t.Fatalf("servidor fechado não pode entregar (got %d)", got)
	}
	if got := server.UIAuthenticatedCount(); got != 0 {
		t.Fatalf("servidor fechado não tem UIs (got %d)", got)
	}
}
