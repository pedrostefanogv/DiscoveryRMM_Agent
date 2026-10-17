//go:build windows

package app

// Testes de integração do canal IPC (servidor/autenticação/cliente) usando
// net.Pipe — sem named pipe real e sem depender do serviço instalado.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"runtime"
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

// startIPCTestPeer sobe um handleConn sobre net.Pipe com identidade fixa.
func startIPCTestPeer(t *testing.T, admin bool, identity ipcAuthIdentity, onMessage func(net.Conn, IPCMessage)) *ipcTestPeer {
	t.Helper()
	old := ipcCallerValidator
	ipcCallerValidator = func(net.Conn) (ipcAuthIdentity, error) { return identity, nil }
	t.Cleanup(func() { ipcCallerValidator = old })

	server := &IPCServer{
		conns:     make(map[net.Conn]*ipcClientConn),
		tokens:    newIPCTokenStore(),
		OnMessage: onMessage,
	}
	serverEnd, clientEnd := net.Pipe()
	go server.handleConn(serverEnd, admin)
	t.Cleanup(func() { clientEnd.Close() })
	return &ipcTestPeer{server: server, conn: clientEnd, reader: bufio.NewReader(clientEnd)}
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
	return ipcAuthIdentity{SID: fmt.Sprintf("S-1-5-21-%d", session), PID: 1000 + session, SessionID: session, Valid: true}
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
	go client.adminReadLoop(clientEnd, client.adminRx)
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
