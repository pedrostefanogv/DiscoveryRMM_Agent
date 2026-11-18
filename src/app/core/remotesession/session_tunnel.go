//go:build windows

package remotesession

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/core/netproxy"
)

const (
	defaultTunnelMaxStreams          = 64
	defaultTunnelReadChunkBytes      = 32 * 1024
	maxTunnelReadChunkBytes          = 64 * 1024
	defaultTunnelWriteQueueDepth     = 128
	defaultTunnelDialTimeout         = 10 * time.Second
	tunnelNatsBufferedHighWaterBytes = 4 * 1024 * 1024
	tunnelInboundWriteEnqueueTimeout = 200 * time.Millisecond
)

// TunnelConfig configura um SessionTunnel multiplexado sobre NATS (.tunnel.in/.tunnel.out).
type TunnelConfig struct {
	SessionID             string
	Mode                  string // "proxy" | "rdp"
	Allowlist             *netproxy.Allowlist
	RDPPort               int
	MaxStreams            int
	ReadChunkBytes        int
	StreamWriteQueueDepth int
	DialTimeout           time.Duration
	DialFunc              func(ctx context.Context, host string, port int) (net.Conn, error)
	PreflightFunc         func(ctx context.Context) RDPStatus
	PublishReady          bool
}

// SessionTunnel multiplexa streams TCP/SSH/RDP concorrentes sobre .tunnel.in e .tunnel.out.
type SessionTunnel struct {
	cfg        TunnelConfig
	natsStream *NatsStreamHandler

	mu      sync.Mutex
	sub     *nats.Subscription
	streams map[uint32]*tunnelStream
	closed  bool

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

type tunnelStream struct {
	tunnel *SessionTunnel
	id     uint32

	writeCh    chan []byte
	writeEndCh chan struct{}
	abortCh    chan struct{}

	writeEndOnce sync.Once
	abortOnce    sync.Once
	closeOnce    sync.Once

	connMu sync.Mutex
	conn   net.Conn
}

// NewSessionTunnel cria um SessionTunnel validando e normalizando os defaults de configuracao.
func NewSessionTunnel(natsStream *NatsStreamHandler, cfg TunnelConfig) *SessionTunnel {
	if cfg.Mode == "" {
		cfg.Mode = KindProxy
	}
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	if cfg.RDPPort <= 0 || cfg.RDPPort > 65535 {
		cfg.RDPPort = 3389
	}
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = defaultTunnelMaxStreams
	}
	if cfg.StreamWriteQueueDepth <= 0 {
		cfg.StreamWriteQueueDepth = defaultTunnelWriteQueueDepth
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultTunnelDialTimeout
	}
	if cfg.ReadChunkBytes <= 0 {
		cfg.ReadChunkBytes = defaultTunnelReadChunkBytes
	}
	if cfg.ReadChunkBytes > maxTunnelReadChunkBytes {
		cfg.ReadChunkBytes = maxTunnelReadChunkBytes
	}
	if natsStream != nil {
		maxAllowed := natsStream.EffectiveMaxPayloadBytes() - 128
		if maxAllowed > TunnelBinaryHeaderLen && cfg.ReadChunkBytes > maxAllowed {
			cfg.ReadChunkBytes = maxAllowed
		}
	}
	if cfg.Allowlist == nil {
		cfg.Allowlist = netproxy.NewAllowlist()
		if cfg.Mode == KindRdp {
			_ = cfg.Allowlist.AllowRDPEndpoint(cfg.RDPPort)
		}
	}
	if cfg.PreflightFunc == nil {
		cfg.PreflightFunc = CheckRDPPreflight
	}

	return &SessionTunnel{
		cfg:        cfg,
		natsStream: natsStream,
		streams:    make(map[uint32]*tunnelStream),
		stopCh:     make(chan struct{}),
	}
}

// Subscribe assina .tunnel.in antes da publicacao de .proxy.ready (anti-race).
func (t *SessionTunnel) Subscribe(ctx context.Context) error {
	if t.natsStream == nil {
		return fmt.Errorf("natsStream nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return fmt.Errorf("tunel ja encerrado")
	}
	if t.sub != nil {
		return nil
	}

	sub, err := t.natsStream.SubscribeToTunnelIn(t.cfg.SessionID, func(data []byte) {
		cp := make([]byte, len(data))
		copy(cp, data)
		t.handleInbound(ctx, cp)
	})
	if err != nil {
		return fmt.Errorf("subscribe tunnel.in: %w", err)
	}
	t.sub = sub
	return nil
}

// Run aguarda o cancelamento de ctx ou fechamento do tunel e encerra todos os streams.
func (t *SessionTunnel) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		_ = t.Close()
		return ctx.Err()
	case <-t.stopCh:
		_ = t.Close()
		return nil
	}
}

// Start assina .tunnel.in, opcionalmente publica .proxy.ready e bloqueia ate encerrar.
func (t *SessionTunnel) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := t.Subscribe(ctx); err != nil {
		return err
	}
	if t.cfg.PublishReady {
		payload := map[string]any{
			"status":          "ready",
			"mode":            t.cfg.Mode,
			"tunnel":          true,
			"protocolVersion": 1,
		}
		if t.cfg.Mode == KindRdp {
			status := t.cfg.PreflightFunc(ctx)
			payload["rdpHost"] = "127.0.0.1"
			payload["rdpPort"] = t.cfg.RDPPort
			payload["rdpStatus"] = status
		}
		_ = t.natsStream.PublishProxyReady(t.cfg.SessionID, payload)
	}
	return t.Run(ctx)
}

// Close encerra a subscricao NATS, fecha todos os streams ativos e aguarda goroutines.
func (t *SessionTunnel) Close() error {
	if t == nil {
		return nil
	}
	t.stopOnce.Do(func() {
		close(t.stopCh)
	})

	t.mu.Lock()
	t.closed = true
	sub := t.sub
	t.sub = nil
	active := make([]*tunnelStream, 0, len(t.streams))
	for id, s := range t.streams {
		active = append(active, s)
		delete(t.streams, id)
	}
	t.mu.Unlock()

	if sub != nil {
		_ = sub.Unsubscribe()
	}
	for _, s := range active {
		s.abort()
	}

	t.wg.Wait()
	if t.natsStream != nil {
		_ = t.natsStream.FlushTimeout(1 * time.Second)
	}
	return nil
}

// ActiveStreams retorna a quantidade de streams atualmente abertos no tunel.
func (t *SessionTunnel) ActiveStreams() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.streams)
}

func (t *SessionTunnel) handleInbound(ctx context.Context, raw []byte) {
	isBin, opByte, streamID, payload, ctrl, err := DecodeTunnelFrame(raw)
	if err != nil {
		log.Printf("[session-tunnel] frame invalido descartado (session=%s): %v", t.cfg.SessionID, err)
		return
	}

	if isBin {
		if streamID == 0 {
			t.sendError(0, "invalid_stream", "streamId binario nao pode ser 0")
			return
		}
		s := t.getStream(streamID)
		if s == nil {
			return
		}
		switch opByte {
		case TunnelOpData:
			t.enqueueToStream(s, payload)
		case TunnelOpClose:
			s.endWrite()
		}
		return
	}

	switch ctrl.Op {
	case "preflight":
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return
		}
		t.wg.Add(1)
		t.mu.Unlock()
		go func(reqStreamID uint32) {
			defer t.wg.Done()
			status := t.cfg.PreflightFunc(ctx)
			t.sendControl(TunnelControlMsg{
				StreamID:  reqStreamID,
				Op:        "preflight",
				RdpStatus: &status,
			})
		}(ctrl.StreamID)

	case "open":
		t.handleOpen(ctx, ctrl)

	case "data":
		if ctrl.StreamID == 0 {
			return
		}
		s := t.getStream(ctrl.StreamID)
		if s == nil {
			return
		}
		decoded, derr := base64.StdEncoding.DecodeString(ctrl.DataB64)
		if derr != nil {
			s.failAndClose("invalid_payload", "dataB64 invalido: "+derr.Error())
			return
		}
		t.enqueueToStream(s, decoded)

	case "close":
		if ctrl.StreamID == 0 {
			return
		}
		if s := t.getStream(ctrl.StreamID); s != nil {
			s.endWrite()
		}
	}
}

func (t *SessionTunnel) enqueueToStream(s *tunnelStream, data []byte) {
	if len(data) == 0 {
		return
	}
	cp := make([]byte, len(data))
	copy(cp, data)

	select {
	case <-s.abortCh:
		return
	case <-s.writeEndCh:
		return
	default:
	}

	timer := time.NewTimer(tunnelInboundWriteEnqueueTimeout)
	defer timer.Stop()

	select {
	case s.writeCh <- cp:
	case <-s.abortCh:
	case <-s.writeEndCh:
	case <-timer.C:
		s.failAndClose("backpressure_overflow", "fila de escrita do stream saturada")
	}
}

func (t *SessionTunnel) handleOpen(ctx context.Context, ctrl *TunnelControlMsg) {
	sid := ctrl.StreamID
	if sid == 0 {
		t.sendError(0, "invalid_stream", "streamId obrigatorio para open")
		return
	}

	targetHost := strings.TrimSpace(ctrl.TargetHost)
	targetPort := ctrl.TargetPort
	protocol := strings.ToLower(strings.TrimSpace(ctrl.Protocol))
	if protocol == "" {
		if t.cfg.Mode == KindRdp {
			protocol = "rdp"
		} else {
			protocol = "tcp"
		}
	}

	if t.cfg.Mode == KindRdp {
		if targetHost == "" {
			targetHost = "127.0.0.1"
		}
		if targetPort == 0 {
			targetPort = t.cfg.RDPPort
		}
		if targetHost != "127.0.0.1" || targetPort != t.cfg.RDPPort {
			t.sendError(sid, "allowlist_denied", fmt.Sprintf("sessao RDP bloqueia destino fora de 127.0.0.1:%d", t.cfg.RDPPort))
			t.sendControl(TunnelControlMsg{StreamID: sid, Op: "close"})
			return
		}
	} else {
		if targetHost == "" || targetPort < 1 || targetPort > 65535 {
			t.sendError(sid, "invalid_target", "targetHost e targetPort validos sao obrigatorios")
			t.sendControl(TunnelControlMsg{StreamID: sid, Op: "close"})
			return
		}
		// Pre-valida literais / allowlist antes de registrar o stream; a validacao
		// completa (incluindo DNS unico anti-TOCTOU) tambem ocorre em DialAllowed.
		if t.cfg.Allowlist == nil || t.cfg.Allowlist.IsEmpty() {
			t.sendError(sid, "allowlist_denied", fmt.Sprintf("acesso bloqueado pela allowlist: %s:%d", targetHost, targetPort))
			t.sendControl(TunnelControlMsg{StreamID: sid, Op: "close"})
			return
		}
	}

	s := &tunnelStream{
		tunnel:     t,
		id:         sid,
		writeCh:    make(chan []byte, t.cfg.StreamWriteQueueDepth),
		writeEndCh: make(chan struct{}),
		abortCh:    make(chan struct{}),
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	if _, dup := t.streams[sid]; dup {
		t.mu.Unlock()
		t.sendError(sid, "duplicate_stream", fmt.Sprintf("streamId %d ja ativo", sid))
		return
	}
	if len(t.streams) >= t.cfg.MaxStreams {
		t.mu.Unlock()
		t.sendError(sid, "stream_limit", fmt.Sprintf("limite de %d streams atingido", t.cfg.MaxStreams))
		t.sendControl(TunnelControlMsg{StreamID: sid, Op: "close"})
		return
	}
	t.streams[sid] = s
	t.wg.Add(1)
	t.mu.Unlock()

	go t.dialAndPump(ctx, s, targetHost, targetPort, protocol)
}

func (t *SessionTunnel) dialAndPump(ctx context.Context, s *tunnelStream, host string, port int, protocol string) {
	defer t.wg.Done()

	dialCtx, cancel := context.WithTimeout(ctx, t.cfg.DialTimeout)
	defer cancel()

	var (
		conn net.Conn
		err  error
	)
	if t.cfg.DialFunc != nil {
		// Mesmo com DialFunc customizado em testes, exige validacao de allowlist antes!
		if _, vErr := t.cfg.Allowlist.ResolveAndValidate(dialCtx, host, port); vErr != nil {
			err = fmt.Errorf("allowlist_denied: %w", vErr)
		} else {
			conn, err = t.cfg.DialFunc(dialCtx, host, port)
		}
	} else {
		conn, err = t.cfg.Allowlist.DialAllowed(dialCtx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}

	if err != nil {
		code := "dial_failed"
		msg := err.Error()
		if strings.Contains(msg, "allowlist") || strings.Contains(msg, "SSRF") {
			code = "allowlist_denied"
		}
		s.failAndClose(code, msg)
		return
	}

	select {
	case <-s.abortCh:
		_ = conn.Close()
		return
	case <-t.stopCh:
		_ = conn.Close()
		return
	default:
	}

	if !s.setConn(conn) {
		s.abort()
		t.removeStream(s.id, s)
		return
	}
	t.sendControl(TunnelControlMsg{
		StreamID:   s.id,
		Op:         "opened",
		TargetHost: host,
		TargetPort: port,
		Protocol:   protocol,
	})

	var pumpWG sync.WaitGroup
	pumpWG.Add(2)

	// Writer goroutine (NATS -> TCP socket)
	go func() {
		defer pumpWG.Done()
		s.writeLoop(conn)
	}()

	// Reader goroutine (TCP socket -> NATS)
	go func() {
		defer pumpWG.Done()
		s.readLoop(conn)
	}()

	pumpWG.Wait()
	t.removeStream(s.id, s)
	s.abort()
	s.notifyClosed()
}

func (s *tunnelStream) writeLoop(conn net.Conn) {
	for {
		select {
		case <-s.abortCh:
			return
		case data := <-s.writeCh:
			if err := writeAllTCP(conn, data); err != nil {
				s.failAndClose("write_error", err.Error())
				return
			}
		case <-s.writeEndCh:
			for {
				select {
				case data := <-s.writeCh:
					if err := writeAllTCP(conn, data); err != nil {
						s.failAndClose("write_error", err.Error())
						return
					}
				default:
					if cw, ok := conn.(interface{ CloseWrite() error }); ok {
						_ = cw.CloseWrite()
					} else {
						_ = conn.Close()
					}
					return
				}
			}
		}
	}
}

func (s *tunnelStream) readLoop(conn net.Conn) {
	chunkSize := s.tunnel.cfg.ReadChunkBytes
	buf := make([]byte, TunnelBinaryHeaderLen+chunkSize)
	EncodeTunnelBinaryHeader(buf[:TunnelBinaryHeaderLen], TunnelOpData, s.id)

	for {
		n, err := conn.Read(buf[TunnelBinaryHeaderLen:])
		if n > 0 {
			if s.tunnel.natsStream != nil {
				if buffered, berr := s.tunnel.natsStream.BufferedBytes(); berr == nil && buffered > tunnelNatsBufferedHighWaterBytes {
					_ = s.tunnel.natsStream.FlushTimeout(2 * time.Second)
				}
				if pubErr := s.tunnel.natsStream.PublishTunnelOut(s.tunnel.cfg.SessionID, buf[:TunnelBinaryHeaderLen+n]); pubErr != nil {
					s.failAndClose("publish_error", pubErr.Error())
					return
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !s.isAborting() {
				s.tunnel.sendError(s.id, "read_error", err.Error())
			}
			s.tunnel.removeStream(s.id, s)
			s.notifyClosed()
			s.abort()
			return
		}
	}
}

func (s *tunnelStream) setConn(conn net.Conn) bool {
	s.connMu.Lock()
	if s.isAborting() {
		s.connMu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		return false
	}
	s.conn = conn
	s.connMu.Unlock()
	return true
}

func (s *tunnelStream) endWrite() {
	s.writeEndOnce.Do(func() {
		close(s.writeEndCh)
	})
}

func (s *tunnelStream) abort() {
	s.abortOnce.Do(func() {
		close(s.abortCh)
		s.connMu.Lock()
		c := s.conn
		s.conn = nil
		s.connMu.Unlock()
		if c != nil {
			_ = c.Close()
		}
	})
}

func (s *tunnelStream) isAborting() bool {
	select {
	case <-s.abortCh:
		return true
	case <-s.tunnel.stopCh:
		return true
	default:
		return false
	}
}

func (s *tunnelStream) notifyClosed() {
	s.closeOnce.Do(func() {
		s.tunnel.sendControl(TunnelControlMsg{
			StreamID: s.id,
			Op:       "close",
		})
	})
}

func (s *tunnelStream) failAndClose(code, msg string) {
	s.tunnel.removeStream(s.id, s)
	s.tunnel.sendError(s.id, code, msg)
	s.notifyClosed()
	s.abort()
}

func (t *SessionTunnel) getStream(id uint32) *tunnelStream {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.streams[id]
}

func (t *SessionTunnel) removeStream(id uint32, expected *tunnelStream) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cur, ok := t.streams[id]; ok && (expected == nil || cur == expected) {
		delete(t.streams, id)
	}
}

func (t *SessionTunnel) sendError(streamID uint32, code, msg string) {
	t.sendControl(TunnelControlMsg{
		StreamID: streamID,
		Op:       "error",
		Code:     code,
		Error:    msg,
	})
}

func (t *SessionTunnel) sendControl(msg TunnelControlMsg) {
	if t.natsStream == nil {
		return
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_ = t.natsStream.PublishTunnelOut(t.cfg.SessionID, b)
}

func writeAllTCP(conn net.Conn, data []byte) error {
	written := 0
	for written < len(data) {
		n, err := conn.Write(data[written:])
		if n > 0 {
			written += n
		}
		if err != nil {
			return err
		}
	}
	return nil
}
