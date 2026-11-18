//go:build windows

package remotesession

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/core/netproxy"
)

func startEchoTCPServer(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

func tunnelSubject(sessionID, suffix string) string {
	return "tenant." + stripHyphens("client-1") + ".site." + stripHyphens("site-1") +
		".agent." + stripHyphens("agent-1") + ".remote.session." + stripHyphens(sessionID) + "." + suffix
}

func TestSessionTunnel_MultiplexedEchoAndBinaryData(t *testing.T) {
	srv := startLivenessNATS(t)
	agentNC, err := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("agent connect: %v", err)
	}
	t.Cleanup(agentNC.Close)
	viewerNC, err := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("viewer connect: %v", err)
	}
	t.Cleanup(viewerNC.Close)

	port1, stop1 := startEchoTCPServer(t)
	defer stop1()
	port2, stop2 := startEchoTCPServer(t)
	defer stop2()

	al := netproxy.NewAllowlist()
	al.SetAllowLoopback(true)
	_ = al.AddCIDR("127.0.0.1/32")
	al.AddPort(port1)
	al.AddPort(port2)

	sid := "1a2b3c4d0010"
	ns := NewNatsStreamHandler(agentNC, "client-1", "site-1", "agent-1")
	tunnel := NewSessionTunnel(ns, TunnelConfig{
		SessionID:      sid,
		Mode:           KindProxy,
		Allowlist:      al,
		ReadChunkBytes: 16 * 1024,
		PublishReady:   true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu       sync.Mutex
		openedCh = make(chan uint32, 4)
		readyCh  = make(chan struct{}, 1)
		recv1    bytes.Buffer
		recv2    bytes.Buffer
		done1    = make(chan struct{})
		done2    = make(chan struct{})
	)
	want1 := bytes.Repeat([]byte{0x01, 0x02, 0xFF, 0x80}, 18000) // 72 KB (> chunk size)
	want2 := []byte("ssh-stream-2-payload")

	readySub, _ := viewerNC.Subscribe(tunnelSubject(sid, "proxy.ready"), func(*nats.Msg) {
		select {
		case readyCh <- struct{}{}:
		default:
		}
	})
	defer readySub.Unsubscribe()

	outSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "tunnel.out"), func(msg *nats.Msg) {
		isBin, op, streamID, payload, ctrl, derr := DecodeTunnelFrame(msg.Data)
		if derr != nil {
			return
		}
		if isBin && op == TunnelOpData {
			mu.Lock()
			if streamID == 1 {
				recv1.Write(payload)
				if recv1.Len() == len(want1) {
					select {
					case <-done1:
					default:
						close(done1)
					}
				}
			} else if streamID == 2 {
				recv2.Write(payload)
				if recv2.Len() == len(want2) {
					select {
					case <-done2:
					default:
						close(done2)
					}
				}
			}
			mu.Unlock()
			return
		}
		if ctrl != nil && ctrl.Op == "opened" {
			openedCh <- ctrl.StreamID
		}
	})
	defer outSub.Unsubscribe()
	_ = viewerNC.Flush()

	go func() { _ = tunnel.Start(ctx) }()
	<-readyCh

	inSubj := tunnelSubject(sid, "tunnel.in")
	open1, _ := json.Marshal(TunnelControlMsg{StreamID: 1, Op: "open", TargetHost: "127.0.0.1", TargetPort: port1, Protocol: "tcp"})
	open2, _ := json.Marshal(TunnelControlMsg{StreamID: 2, Op: "open", TargetHost: "127.0.0.1", TargetPort: port2, Protocol: "ssh"})
	_ = viewerNC.Publish(inSubj, open1)
	_ = viewerNC.Publish(inSubj, open2)
	_ = viewerNC.Flush()

	for i := 0; i < 2; i++ {
		select {
		case <-openedCh:
		case <-time.After(3 * time.Second):
			t.Fatal("timeout aguardando streams abrirem")
		}
	}

	// Intercala envio binario nos dois streams
	for off := 0; off < len(want1); off += 8192 {
		end := off + 8192
		if end > len(want1) {
			end = len(want1)
		}
		_ = viewerNC.Publish(inSubj, EncodeTunnelBinaryData(nil, 1, want1[off:end]))
	}
	_ = viewerNC.Publish(inSubj, EncodeTunnelBinaryData(nil, 2, want2))
	_ = viewerNC.Flush()

	select {
	case <-done1:
	case <-time.After(5 * time.Second):
		mu.Lock()
		gotLen := recv1.Len()
		mu.Unlock()
		t.Fatalf("timeout aguardando dados stream 1: got %d want %d", gotLen, len(want1))
	}
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout aguardando dados stream 2")
	}

	mu.Lock()
	b1 := append([]byte(nil), recv1.Bytes()...)
	b2 := append([]byte(nil), recv2.Bytes()...)
	mu.Unlock()

	if !bytes.Equal(b1, want1) {
		t.Fatalf("stream 1 mismatch (got %d want %d)", len(b1), len(want1))
	}
	if !bytes.Equal(b2, want2) {
		t.Fatalf("stream 2 mismatch: got %q want %q", string(b2), string(want2))
	}
}

func TestSessionTunnel_PipelinedDataBeforeOpened(t *testing.T) {
	srv := startLivenessNATS(t)
	agentNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(agentNC.Close)
	viewerNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(viewerNC.Close)

	port, stop := startEchoTCPServer(t)
	defer stop()

	al := netproxy.NewAllowlist()
	al.SetAllowLoopback(true)
	_ = al.AddCIDR("127.0.0.1/32")
	al.AddPort(port)

	sid := "1a2b3c4d0011"
	ns := NewNatsStreamHandler(agentNC, "client-1", "site-1", "agent-1")
	tunnel := NewSessionTunnel(ns, TunnelConfig{
		SessionID:    sid,
		Mode:         KindProxy,
		Allowlist:    al,
		PublishReady: true,
		DialFunc: func(ctx context.Context, host string, p int) (net.Conn, error) {
			time.Sleep(60 * time.Millisecond)
			return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, "0")[:len(host)+1]+string(rune(0))) // unreachable? wait: use real DialAllowed below
		},
	})
	// Use real DialAllowed with slight delay so TCP echo receives pipelined bytes
	tunnel.cfg.DialFunc = func(ctx context.Context, host string, p int) (net.Conn, error) {
		time.Sleep(50 * time.Millisecond)
		return netproxy.DialTCPAllowed(ctx, al, host, p)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readyCh := make(chan struct{}, 1)
	echoCh := make(chan []byte, 4)
	readySub, _ := viewerNC.Subscribe(tunnelSubject(sid, "proxy.ready"), func(*nats.Msg) {
		select {
		case readyCh <- struct{}{}:
		default:
		}
	})
	defer readySub.Unsubscribe()
	outSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "tunnel.out"), func(msg *nats.Msg) {
		isBin, op, _, payload, _, err := DecodeTunnelFrame(msg.Data)
		if err == nil && isBin && op == TunnelOpData {
			cp := append([]byte(nil), payload...)
			echoCh <- cp
		}
	})
	defer outSub.Unsubscribe()
	_ = viewerNC.Flush()

	go func() { _ = tunnel.Start(ctx) }()
	<-readyCh

	inSubj := tunnelSubject(sid, "tunnel.in")
	openMsg, _ := json.Marshal(TunnelControlMsg{StreamID: 10, Op: "open", TargetHost: "127.0.0.1", TargetPort: port})
	_ = viewerNC.Publish(inSubj, openMsg)
	_ = viewerNC.Publish(inSubj, EncodeTunnelBinaryData(nil, 10, []byte("pipelined-hello")))
	_ = viewerNC.Flush()

	select {
	case got := <-echoCh:
		if string(got) != "pipelined-hello" {
			t.Fatalf("pipelined echo = %q, want pipelined-hello", string(got))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando echo pipelined")
	}
}

func TestSessionTunnel_AllowlistAndSSRFEnforcement(t *testing.T) {
	srv := startLivenessNATS(t)
	agentNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(agentNC.Close)
	viewerNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(viewerNC.Close)

	// 1. Proxy mode com allowlist vazia -> allowlist_denied
	sid := "1a2b3c4d0012"
	ns := NewNatsStreamHandler(agentNC, "client-1", "site-1", "agent-1")
	proxyTunnel := NewSessionTunnel(ns, TunnelConfig{
		SessionID:    sid,
		Mode:         KindProxy,
		Allowlist:    netproxy.NewAllowlist(),
		PublishReady: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readyCh := make(chan struct{}, 4)
	errCh := make(chan TunnelControlMsg, 8)
	rSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "proxy.ready"), func(*nats.Msg) { readyCh <- struct{}{} })
	defer rSub.Unsubscribe()
	oSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "tunnel.out"), func(msg *nats.Msg) {
		_, _, _, _, ctrl, err := DecodeTunnelFrame(msg.Data)
		if err == nil && ctrl != nil && ctrl.Op == "error" {
			errCh <- *ctrl
		}
	})
	defer oSub.Unsubscribe()
	_ = viewerNC.Flush()

	go func() { _ = proxyTunnel.Start(ctx) }()
	<-readyCh

	inSubj := tunnelSubject(sid, "tunnel.in")
	for i, target := range []struct {
		host string
		port int
	}{
		{"192.168.1.50", 22},
		{"127.0.0.1", 3389},
		{"169.254.169.254", 80},
	} {
		b, _ := json.Marshal(TunnelControlMsg{StreamID: uint32(i + 1), Op: "open", TargetHost: target.host, TargetPort: target.port})
		_ = viewerNC.Publish(inSubj, b)
	}
	_ = viewerNC.Flush()

	for i := 0; i < 3; i++ {
		select {
		case msg := <-errCh:
			if msg.Code != "allowlist_denied" {
				t.Fatalf("esperava code=allowlist_denied, got %+v", msg)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timeout aguardando allowlist_denied")
		}
	}
	_ = proxyTunnel.Close()

	// 2. RDP mode bloqueia porta errada e IP externo, permite apenas 127.0.0.1:<RDPPort>
	rdpPort, stopEcho := startEchoTCPServer(t)
	defer stopEcho()

	sidRdp := "1a2b3c4d0013"
	rdpAllow := netproxy.NewAllowlist()
	_ = rdpAllow.AllowRDPEndpoint(rdpPort)
	rdpTunnel := NewSessionTunnel(ns, TunnelConfig{
		SessionID:    sidRdp,
		Mode:         KindRdp,
		Allowlist:    rdpAllow,
		RDPPort:      rdpPort,
		PublishReady: true,
	})
	rdpReady := make(chan struct{}, 1)
	rdpEvents := make(chan TunnelControlMsg, 8)
	rrSub, _ := viewerNC.Subscribe(tunnelSubject(sidRdp, "proxy.ready"), func(*nats.Msg) { rdpReady <- struct{}{} })
	defer rrSub.Unsubscribe()
	roSub, _ := viewerNC.Subscribe(tunnelSubject(sidRdp, "tunnel.out"), func(msg *nats.Msg) {
		_, _, _, _, ctrl, err := DecodeTunnelFrame(msg.Data)
		if err == nil && ctrl != nil && (ctrl.Op == "error" || ctrl.Op == "opened") {
			rdpEvents <- *ctrl
		}
	})
	defer roSub.Unsubscribe()
	_ = viewerNC.Flush()

	go func() { _ = rdpTunnel.Start(ctx) }()
	<-rdpReady

	rdpIn := tunnelSubject(sidRdp, "tunnel.in")
	badPortMsg, _ := json.Marshal(TunnelControlMsg{StreamID: 1, Op: "open", TargetHost: "127.0.0.1", TargetPort: rdpPort + 1})
	badHostMsg, _ := json.Marshal(TunnelControlMsg{StreamID: 2, Op: "open", TargetHost: "192.168.1.10", TargetPort: rdpPort})
	goodMsg, _ := json.Marshal(TunnelControlMsg{StreamID: 3, Op: "open", TargetHost: "127.0.0.1", TargetPort: rdpPort})
	_ = viewerNC.Publish(rdpIn, badPortMsg)
	_ = viewerNC.Publish(rdpIn, badHostMsg)
	_ = viewerNC.Publish(rdpIn, goodMsg)
	_ = viewerNC.Flush()

	gotOpened := false
	deniedCount := 0
	for i := 0; i < 3; i++ {
		select {
		case ev := <-rdpEvents:
			if ev.StreamID == 3 && ev.Op == "opened" {
				gotOpened = true
			} else if ev.Op == "error" && ev.Code == "allowlist_denied" {
				deniedCount++
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timeout aguardando eventos RDP")
		}
	}
	if !gotOpened || deniedCount != 2 {
		t.Fatalf("RDP policy falhou: gotOpened=%v deniedCount=%d", gotOpened, deniedCount)
	}
}

func TestSessionTunnel_CleanCloseAndEOFPropagation(t *testing.T) {
	srv := startLivenessNATS(t)
	agentNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(agentNC.Close)
	viewerNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(viewerNC.Close)

	port, stop := startEchoTCPServer(t)
	defer stop()

	al := netproxy.NewAllowlist()
	al.SetAllowLoopback(true)
	_ = al.AddCIDR("127.0.0.1/32")
	al.AddPort(port)

	sid := "1a2b3c4d0014"
	ns := NewNatsStreamHandler(agentNC, "client-1", "site-1", "agent-1")
	tunnel := NewSessionTunnel(ns, TunnelConfig{
		SessionID:    sid,
		Mode:         KindProxy,
		Allowlist:    al,
		PublishReady: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readyCh := make(chan struct{}, 1)
	openedCh := make(chan struct{}, 1)
	closedCh := make(chan struct{}, 1)
	dataCh := make(chan []byte, 4)

	rSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "proxy.ready"), func(*nats.Msg) { readyCh <- struct{}{} })
	defer rSub.Unsubscribe()
	oSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "tunnel.out"), func(msg *nats.Msg) {
		isBin, op, _, payload, ctrl, err := DecodeTunnelFrame(msg.Data)
		if err != nil {
			return
		}
		if isBin && op == TunnelOpData {
			dataCh <- append([]byte(nil), payload...)
			return
		}
		if ctrl != nil {
			switch ctrl.Op {
			case "opened":
				openedCh <- struct{}{}
			case "close":
				select {
				case closedCh <- struct{}{}:
				default:
				}
			}
		}
	})
	defer oSub.Unsubscribe()
	_ = viewerNC.Flush()

	go func() { _ = tunnel.Start(ctx) }()
	<-readyCh

	inSubj := tunnelSubject(sid, "tunnel.in")
	openMsg, _ := json.Marshal(TunnelControlMsg{StreamID: 1, Op: "open", TargetHost: "127.0.0.1", TargetPort: port})
	_ = viewerNC.Publish(inSubj, openMsg)
	_ = viewerNC.Flush()
	<-openedCh

	// Envia dado seguido imediatamente de close: dados devem drenar antes do CloseWrite/EOF.
	_ = viewerNC.Publish(inSubj, EncodeTunnelBinaryData(nil, 1, []byte("flush-before-close")))
	closeMsg, _ := json.Marshal(TunnelControlMsg{StreamID: 1, Op: "close"})
	_ = viewerNC.Publish(inSubj, closeMsg)
	_ = viewerNC.Flush()

	select {
	case got := <-dataCh:
		if string(got) != "flush-before-close" {
			t.Fatalf("got %q want flush-before-close", string(got))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando echo antes do close")
	}

	select {
	case <-closedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando propagacao de close/EOF")
	}
}

func TestSessionTunnel_BackpressureAndMaxStreams(t *testing.T) {
	srv := startLivenessNATS(t)
	agentNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(agentNC.Close)
	viewerNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(viewerNC.Close)

	// Servidor que aceita conexao mas nunca chama Read (simula consumidor lento)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	stallPort := ln.Addr().(*net.TCPAddr).Port

	var connsMu sync.Mutex
	var conns []net.Conn
	defer func() {
		connsMu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		connsMu.Unlock()
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			connsMu.Lock()
			conns = append(conns, c)
			connsMu.Unlock()
		}
	}()

	al := netproxy.NewAllowlist()
	al.SetAllowLoopback(true)
	_ = al.AddCIDR("127.0.0.1/32")
	al.AddPort(stallPort)

	sid := "1a2b3c4d0015"
	ns := NewNatsStreamHandler(agentNC, "client-1", "site-1", "agent-1")
	tunnel := NewSessionTunnel(ns, TunnelConfig{
		SessionID:             sid,
		Mode:                  KindProxy,
		Allowlist:             al,
		MaxStreams:            1,
		StreamWriteQueueDepth: 1,
		PublishReady:          true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readyCh := make(chan struct{}, 1)
	openedCh := make(chan struct{}, 1)
	errCh := make(chan TunnelControlMsg, 4)

	rSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "proxy.ready"), func(*nats.Msg) { readyCh <- struct{}{} })
	defer rSub.Unsubscribe()
	oSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "tunnel.out"), func(msg *nats.Msg) {
		_, _, _, _, ctrl, derr := DecodeTunnelFrame(msg.Data)
		if derr == nil && ctrl != nil {
			if ctrl.Op == "opened" {
				openedCh <- struct{}{}
			} else if ctrl.Op == "error" {
				errCh <- *ctrl
			}
		}
	})
	defer oSub.Unsubscribe()
	_ = viewerNC.Flush()

	go func() { _ = tunnel.Start(ctx) }()
	<-readyCh

	inSubj := tunnelSubject(sid, "tunnel.in")
	open1, _ := json.Marshal(TunnelControlMsg{StreamID: 1, Op: "open", TargetHost: "127.0.0.1", TargetPort: stallPort})
	open2, _ := json.Marshal(TunnelControlMsg{StreamID: 2, Op: "open", TargetHost: "127.0.0.1", TargetPort: stallPort})
	_ = viewerNC.Publish(inSubj, open1)
	_ = viewerNC.Flush()
	<-openedCh

	// Excede MaxStreams=1
	_ = viewerNC.Publish(inSubj, open2)
	_ = viewerNC.Flush()

	select {
	case ev := <-errCh:
		if ev.StreamID != 2 || ev.Code != "stream_limit" {
			t.Fatalf("esperava stream_limit para stream 2, got %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando stream_limit")
	}
}

func TestSessionTunnel_BackpressureOverflowDoesNotBlockSibling(t *testing.T) {
	srv := startLivenessNATS(t)
	agentNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(agentNC.Close)
	viewerNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(viewerNC.Close)

	echoPort, stopEcho := startEchoTCPServer(t)
	defer stopEcho()

	al := netproxy.NewAllowlist()
	al.SetAllowLoopback(true)
	_ = al.AddCIDR("127.0.0.1/32")
	al.AddPort(echoPort)
	al.AddPort(9999)

	// Stream 1 conecta num net.Pipe sem leitor (trava na 1a escrita), stream 2 usa TCP echo real.
	clientPipe, serverPipe := net.Pipe()
	defer clientPipe.Close()
	defer serverPipe.Close()

	sid := "1a2b3c4d0017"
	ns := NewNatsStreamHandler(agentNC, "client-1", "site-1", "agent-1")
	tunnel := NewSessionTunnel(ns, TunnelConfig{
		SessionID:             sid,
		Mode:                  KindProxy,
		Allowlist:             al,
		MaxStreams:            4,
		StreamWriteQueueDepth: 1,
		PublishReady:          true,
		DialFunc: func(ctx context.Context, host string, port int) (net.Conn, error) {
			if port == 9999 {
				return clientPipe, nil
			}
			return netproxy.DialTCPAllowed(ctx, al, host, port)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readyCh := make(chan struct{}, 1)
	openedCh := make(chan uint32, 4)
	errCh := make(chan TunnelControlMsg, 4)
	echoCh := make(chan []byte, 4)

	rSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "proxy.ready"), func(*nats.Msg) { readyCh <- struct{}{} })
	defer rSub.Unsubscribe()
	oSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "tunnel.out"), func(msg *nats.Msg) {
		isBin, op, _, payload, ctrl, err := DecodeTunnelFrame(msg.Data)
		if err != nil {
			return
		}
		if isBin && op == TunnelOpData {
			echoCh <- append([]byte(nil), payload...)
			return
		}
		if ctrl != nil {
			if ctrl.Op == "opened" {
				openedCh <- ctrl.StreamID
			} else if ctrl.Op == "error" {
				errCh <- *ctrl
			}
		}
	})
	defer oSub.Unsubscribe()
	_ = viewerNC.Flush()

	go func() { _ = tunnel.Start(ctx) }()
	<-readyCh

	inSubj := tunnelSubject(sid, "tunnel.in")
	open1, _ := json.Marshal(TunnelControlMsg{StreamID: 1, Op: "open", TargetHost: "127.0.0.1", TargetPort: 9999})
	open2, _ := json.Marshal(TunnelControlMsg{StreamID: 2, Op: "open", TargetHost: "127.0.0.1", TargetPort: echoPort})
	_ = viewerNC.Publish(inSubj, open1)
	_ = viewerNC.Publish(inSubj, open2)
	_ = viewerNC.Flush()

	for i := 0; i < 2; i++ {
		select {
		case <-openedCh:
		case <-time.After(3 * time.Second):
			t.Fatal("timeout aguardando abertura de streams")
		}
	}

	// Satura stream 1 (1a escrita trava em writeAllTCP, 2a ocupa writeCh cap=1, 3a estoura backpressure)
	for i := 0; i < 4; i++ {
		_ = viewerNC.Publish(inSubj, EncodeTunnelBinaryData(nil, 1, []byte("stall-chunk")))
	}
	_ = viewerNC.Publish(inSubj, EncodeTunnelBinaryData(nil, 2, []byte("sibling-alive")))
	_ = viewerNC.Flush()

	select {
	case ev := <-errCh:
		if ev.StreamID != 1 || ev.Code != "backpressure_overflow" {
			t.Fatalf("esperava backpressure_overflow para stream 1, got %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando backpressure_overflow")
	}

	select {
	case got := <-echoCh:
		if string(got) != "sibling-alive" {
			t.Fatalf("stream 2 echo = %q, want sibling-alive", string(got))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream 2 bloqueado pelo backpressure do stream 1")
	}
}

func TestSessionTunnel_PreflightOverNATS(t *testing.T) {
	srv := startLivenessNATS(t)
	agentNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(agentNC.Close)
	viewerNC, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(viewerNC.Close)

	sid := "1a2b3c4d0016"
	ns := NewNatsStreamHandler(agentNC, "client-1", "site-1", "agent-1")
	al := netproxy.NewAllowlist()
	_ = al.AllowRDPEndpoint(3395)

	tunnel := NewSessionTunnel(ns, TunnelConfig{
		SessionID:    sid,
		Mode:         KindRdp,
		Allowlist:    al,
		RDPPort:      3395,
		PublishReady: true,
		PreflightFunc: func(context.Context) RDPStatus {
			return RDPStatus{
				Ready:            true,
				TermServiceState: "running",
				RDPEnabled:       true,
				Port:             3395,
				Listening:        true,
				CheckedAtUtc:     "2026-11-18T00:00:00Z",
			}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readyCh := make(chan struct{}, 1)
	preflightCh := make(chan TunnelControlMsg, 1)

	rSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "proxy.ready"), func(*nats.Msg) { readyCh <- struct{}{} })
	defer rSub.Unsubscribe()
	oSub, _ := viewerNC.Subscribe(tunnelSubject(sid, "tunnel.out"), func(msg *nats.Msg) {
		_, _, _, _, ctrl, err := DecodeTunnelFrame(msg.Data)
		if err == nil && ctrl != nil && ctrl.Op == "preflight" {
			preflightCh <- *ctrl
		}
	})
	defer oSub.Unsubscribe()
	_ = viewerNC.Flush()

	go func() { _ = tunnel.Start(ctx) }()
	<-readyCh

	req, _ := json.Marshal(TunnelControlMsg{StreamID: 0, Op: "preflight"})
	_ = viewerNC.Publish(tunnelSubject(sid, "tunnel.in"), req)
	_ = viewerNC.Flush()

	select {
	case resp := <-preflightCh:
		if resp.RdpStatus == nil || !resp.RdpStatus.Ready || resp.RdpStatus.Port != 3395 {
			t.Fatalf("preflight inesperado: %+v", resp)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout aguardando preflight over NATS")
	}
}

func TestNatsStreamHandler_ProxyReadyAndTunnelSubjects(t *testing.T) {
	srv := startLivenessNATS(t)
	nc, _ := nats.Connect(srv.ClientURL(), nats.Timeout(2*time.Second))
	t.Cleanup(nc.Close)

	ns := NewNatsStreamHandler(nc, "c1", "s1", "a1")
	if ns.EffectiveMaxPayloadBytes() <= 0 {
		t.Fatal("EffectiveMaxPayloadBytes deve ser > 0")
	}
	if _, err := ns.BufferedBytes(); err != nil {
		t.Fatalf("BufferedBytes erro: %v", err)
	}
	if err := ns.FlushTimeout(time.Second); err != nil {
		t.Fatalf("FlushTimeout erro: %v", err)
	}

	// Rejeita sessionId com wildcard/ponto
	if err := ns.PublishProxyReady("bad.id", map[string]string{"status": "ready"}); err == nil {
		t.Fatal("PublishProxyReady deveria rejeitar sessionId com ponto")
	}
	if err := ns.PublishTunnelOut("bad*id", []byte{1}); err == nil {
		t.Fatal("PublishTunnelOut deveria rejeitar sessionId com *")
	}
	if _, err := ns.SubscribeToTunnelIn("bad>id", func([]byte) {}); err == nil {
		t.Fatal("SubscribeToTunnelIn deveria rejeitar sessionId com >")
	}
	if _, err := ns.SubscribeToProxyReqStream("bad.id", func([]byte, func([]byte) error) {}); err == nil {
		t.Fatal("SubscribeToProxyReqStream deveria rejeitar sessionId com ponto")
	}
}
