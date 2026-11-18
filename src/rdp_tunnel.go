package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"discovery/app/core/remotesession"
	"discovery/app/core/tlsutil"
)

const (
	rdpTunnelMaxStreams       = 64
	rdpTunnelReadChunkBytes   = 32 * 1024
	rdpTunnelWriteQueueDepth  = 128
	rdpTunnelSendQueueDepth   = 256
	rdpTunnelOpenTimeout      = 10 * time.Second
	rdpTunnelPreflightTimeout = 10 * time.Second
	rdpTunnelPingInterval     = 15 * time.Second
)

type rdpTunnelConfig struct {
	ListenAddr       string
	LocalPort        int
	AllowNonLoopback bool
	WSURL            string
	APIURL           string
	AgentID          string
	SessionID        string
	TargetHost       string
	TargetPort       int
	Protocol         string
	Token            string
	TokenStdin       bool
	TokenFile        string
	Ticket           string
	PreflightOnly    bool
	Insecure         bool
	TLSPinHash       string
}

type wsOutMsg struct {
	msgType int
	data    []byte
}

type rdpTunnelOpenResult struct {
	opened bool
	errMsg string
}

type rdpTunnelStream struct {
	id         uint32
	conn       net.Conn
	openCh     chan rdpTunnelOpenResult
	writeCh    chan []byte
	writeEndCh chan struct{}
	abortCh    chan struct{}

	openOnce     sync.Once
	writeEndOnce sync.Once
	abortOnce    sync.Once
	closeMsgOnce sync.Once
}

type rdpTunnelForwarder struct {
	cfg    rdpTunnelConfig
	wsURL  string
	wsConn *websocket.Conn

	sendCh      chan wsOutMsg
	preflightCh chan remotesession.RDPStatus

	mu         sync.Mutex
	streams    map[uint32]*rdpTunnelStream
	listener   net.Listener
	closed     bool
	nextStream atomic.Uint32

	stopOnce sync.Once
	stopCh   chan struct{}
	wsDoneCh chan struct{}
	wg       sync.WaitGroup
}

func runRDPTunnelCLI(args []string) int {
	cfg, showHelp, err := parseRDPTunnelConfig(args, os.Getenv, os.Stdin, os.Stderr)
	if showHelp {
		printRDPTunnelHelp(os.Stdout)
		return 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "[rdp-tunnel] erro: %v\n", err)
		return 1
	}

	wsURL, err := resolveRDPTunnelWSURL(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[rdp-tunnel] erro de URL: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fwd, err := startRDPTunnelForwarder(ctx, cfg, wsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[rdp-tunnel] falha ao conectar: %v\n", err)
		return 1
	}
	defer fwd.Close()

	if cfg.PreflightOnly {
		status, pErr := fwd.RequestPreflight(ctx, rdpTunnelPreflightTimeout)
		if pErr != nil {
			fmt.Fprintf(os.Stderr, "[rdp-tunnel] erro no preflight: %v\n", pErr)
			return 1
		}
		out, _ := json.MarshalIndent(status, "", "  ")
		fmt.Println(string(out))
		if !status.Ready {
			return 2
		}
		return 0
	}

	if err := fwd.ListenAndServe(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "[rdp-tunnel] erro no listener: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "[rdp-tunnel] ouvindo em %s -> %s (target=%s:%d)\n",
		fwd.Addr(), wsURL, cfg.TargetHost, cfg.TargetPort)

	// Preflight informativo ao iniciar (nao fatal).
	go func() {
		status, pErr := fwd.RequestPreflight(ctx, 5*time.Second)
		if pErr == nil && !status.Ready {
			fmt.Fprintf(os.Stderr, "[rdp-tunnel] aviso preflight RDP nao pronto (issues=%v hints=%v)\n",
				status.Issues, status.Hints)
		}
	}()

	select {
	case <-ctx.Done():
	case <-fwd.Done():
	}

	_ = fwd.Close()
	return 0
}

func printRDPTunnelHelp(w io.Writer) {
	fmt.Fprintln(w, "Discovery RDP/TCP Tunnel Forwarder (--rdp-tunnel)")
	fmt.Fprintln(w, "Uso:")
	fmt.Fprintln(w, "  discovery-agent.exe --rdp-tunnel --api-url=https://api.exemplo --agent-id=<id> --session-id=<id> [opcoes]")
	fmt.Fprintln(w, "  discovery-agent.exe --rdp-tunnel --ws-url=wss://api.exemplo/api/v1/remote-sessions/.../tunnel/ws [opcoes]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Opcoes:")
	fmt.Fprintln(w, "  --listen=127.0.0.1:33890   Endereco loopback local para clientes RDP (mstsc.exe)")
	fmt.Fprintln(w, "  --local-port=33890         Atalho para --listen=127.0.0.1:<porta>")
	fmt.Fprintln(w, "  --allow-non-loopback       Permite bind fora de loopback (nao recomendado)")
	fmt.Fprintln(w, "  --ws-url=<url>             URL WebSocket direta do tunel")
	fmt.Fprintln(w, "  --api-url=<url>            URL base da API (requer --agent-id e --session-id)")
	fmt.Fprintln(w, "  --agent-id=<uuid>          ID do agente")
	fmt.Fprintln(w, "  --session-id=<uuid>        ID da sessao remota")
	fmt.Fprintln(w, "  --target-host=127.0.0.1    Host alvo no agente")
	fmt.Fprintln(w, "  --target-port=0            Porta alvo no agente (0 = detectada pelo agente)")
	fmt.Fprintln(w, "  --protocol=rdp             Protocolo (rdp|tcp|ssh)")
	fmt.Fprintln(w, "  --token-stdin              Le Bearer token da primeira linha do stdin")
	fmt.Fprintln(w, "  --token-file=<caminho>     Le Bearer token de arquivo")
	fmt.Fprintln(w, "  --token=<jwt>              Bearer token na linha de comando (evite; prefira DISCOVERY_TUNNEL_TOKEN)")
	fmt.Fprintln(w, "  --ticket=<ticket>          Ticket de tunel enviado via header X-Tunnel-Ticket")
	fmt.Fprintln(w, "  --preflight-only           Consulta status RDP do host remoto, imprime JSON e sai")
	fmt.Fprintln(w, "  --insecure                 Permite TLS nao verificado (apenas laboratorio)")
	fmt.Fprintln(w, "  --tls-pin-hash=<sha256>    Exige pin SHA-256 do certificado TLS da API")
}

func parseRDPTunnelConfig(args []string, getenv func(string) string, stdin io.Reader, stderr io.Writer) (rdpTunnelConfig, bool, error) {
	cfg := rdpTunnelConfig{
		ListenAddr: "127.0.0.1:33890",
		TargetHost: "127.0.0.1",
		TargetPort: 0,
		Protocol:   "rdp",
	}

	cleaned := make([]string, 0, len(args))
	for _, raw := range args {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if lower == "--rdp-tunnel" || lower == "/rdp-tunnel" || lower == "-rdp-tunnel" {
			continue
		}
		if lower == "--help" || lower == "-help" || lower == "-h" || lower == "/help" || lower == "/h" || lower == "/?" {
			return cfg, true, nil
		}
		if strings.HasPrefix(trimmed, "/") {
			trimmed = "--" + trimmed[1:]
		}
		cleaned = append(cleaned, trimmed)
	}

	fs := flag.NewFlagSet("rdp-tunnel", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "")
	fs.IntVar(&cfg.LocalPort, "local-port", 0, "")
	fs.BoolVar(&cfg.AllowNonLoopback, "allow-non-loopback", false, "")
	fs.StringVar(&cfg.WSURL, "ws-url", "", "")
	fs.StringVar(&cfg.APIURL, "api-url", "", "")
	fs.StringVar(&cfg.AgentID, "agent-id", "", "")
	fs.StringVar(&cfg.SessionID, "session-id", "", "")
	fs.StringVar(&cfg.TargetHost, "target-host", cfg.TargetHost, "")
	fs.IntVar(&cfg.TargetPort, "target-port", cfg.TargetPort, "")
	fs.StringVar(&cfg.Protocol, "protocol", cfg.Protocol, "")
	fs.BoolVar(&cfg.TokenStdin, "token-stdin", false, "")
	fs.StringVar(&cfg.TokenFile, "token-file", "", "")
	fs.StringVar(&cfg.Token, "token", "", "")
	fs.StringVar(&cfg.Ticket, "ticket", "", "")
	fs.BoolVar(&cfg.PreflightOnly, "preflight-only", false, "")
	fs.BoolVar(&cfg.Insecure, "insecure", false, "")
	fs.StringVar(&cfg.TLSPinHash, "tls-pin-hash", "", "")

	if err := fs.Parse(cleaned); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, true, nil
		}
		return cfg, false, err
	}

	if cfg.LocalPort > 0 {
		if cfg.LocalPort > 65535 {
			return cfg, false, fmt.Errorf("--local-port invalida: %d", cfg.LocalPort)
		}
		cfg.ListenAddr = fmt.Sprintf("127.0.0.1:%d", cfg.LocalPort)
	}

	if !cfg.PreflightOnly {
		if err := validateTunnelListenAddr(cfg.ListenAddr, cfg.AllowNonLoopback); err != nil {
			return cfg, false, err
		}
	}

	if cfg.TargetPort < 0 || cfg.TargetPort > 65535 {
		return cfg, false, fmt.Errorf("--target-port invalida: %d", cfg.TargetPort)
	}

	// Resolucao de token em ordem de seguranca: env -> stdin -> file -> --token fallback
	var envToken string
	if getenv != nil {
		envToken = strings.TrimSpace(getenv("DISCOVERY_TUNNEL_TOKEN"))
	}
	switch {
	case envToken != "":
		cfg.Token = envToken
	case cfg.TokenStdin:
		if stdin == nil {
			return cfg, false, fmt.Errorf("--token-stdin especificado mas stdin nil")
		}
		line, rerr := bufio.NewReader(stdin).ReadString('\n')
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return cfg, false, fmt.Errorf("ler token do stdin: %w", rerr)
		}
		cfg.Token = strings.TrimSpace(line)
	case strings.TrimSpace(cfg.TokenFile) != "":
		rawToken, rerr := os.ReadFile(strings.TrimSpace(cfg.TokenFile))
		if rerr != nil {
			return cfg, false, fmt.Errorf("ler --token-file: %w", rerr)
		}
		cfg.Token = strings.TrimSpace(string(rawToken))
	case strings.TrimSpace(cfg.Token) != "":
		cfg.Token = strings.TrimSpace(cfg.Token)
		if stderr != nil {
			fmt.Fprintln(stderr, "[rdp-tunnel] AVISO: --token na linha de comando fica visivel para outros processos; prefira DISCOVERY_TUNNEL_TOKEN, --token-stdin ou --token-file")
		}
	}

	return cfg, false, nil
}

func validateTunnelListenAddr(listenAddr string, allowNonLoopback bool) error {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return fmt.Errorf("--listen invalido %q: %w", listenAddr, err)
	}
	if portStr == "" {
		return fmt.Errorf("--listen sem porta: %q", listenAddr)
	}
	if allowNonLoopback {
		return nil
	}
	cleanHost := strings.TrimSpace(host)
	if strings.EqualFold(cleanHost, "localhost") {
		return nil
	}
	ip := net.ParseIP(cleanHost)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen %q rejeitado: somente loopback permitido sem --allow-non-loopback", listenAddr)
	}
	return nil
}

func resolveRDPTunnelWSURL(cfg rdpTunnelConfig) (string, error) {
	if raw := strings.TrimSpace(cfg.WSURL); raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("ws-url invalida: %w", err)
		}
		switch strings.ToLower(u.Scheme) {
		case "ws", "wss":
		case "http":
			u.Scheme = "ws"
		case "https":
			u.Scheme = "wss"
		default:
			return "", fmt.Errorf("esquema ws-url invalido: %q", u.Scheme)
		}
		if u.Host == "" {
			return "", fmt.Errorf("ws-url sem host")
		}
		return u.String(), nil
	}

	apiRaw := strings.TrimSpace(cfg.APIURL)
	agentID := strings.TrimSpace(cfg.AgentID)
	sessionID := strings.TrimSpace(cfg.SessionID)
	if apiRaw == "" || agentID == "" || sessionID == "" {
		return "", fmt.Errorf("informe --ws-url OU (--api-url, --agent-id e --session-id)")
	}
	if !strings.Contains(apiRaw, "://") {
		apiRaw = "https://" + apiRaw
	}
	u, err := url.Parse(apiRaw)
	if err != nil {
		return "", fmt.Errorf("api-url invalida: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("esquema api-url invalido: %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("api-url sem host")
	}
	basePath := strings.TrimRight(u.Path, "/")
	u.Path = fmt.Sprintf("%s/api/v1/remote-sessions/%s/%s/tunnel/ws",
		basePath, url.PathEscape(agentID), url.PathEscape(sessionID))
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func startRDPTunnelForwarder(ctx context.Context, cfg rdpTunnelConfig, wsURL string) (*rdpTunnelForwarder, error) {
	if cfg.Insecure {
		tlsutil.SetConfigAllowInsecureTLS(true)
	}
	dialer := tlsutil.NewWebSocketDialer(10 * time.Second)
	if strings.TrimSpace(cfg.TLSPinHash) != "" {
		dialer.TLSClientConfig = tlsutil.PinnedTLSConfig(cfg.TLSPinHash, true)
	}

	headers := http.Header{}
	if cfg.Token != "" {
		headers.Set("Authorization", "Bearer "+cfg.Token)
	}
	if cfg.Ticket != "" {
		headers.Set("X-Tunnel-Ticket", cfg.Ticket)
	}

	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}

	conn.SetPongHandler(func(string) error { return nil })

	f := &rdpTunnelForwarder{
		cfg:         cfg,
		wsURL:       wsURL,
		wsConn:      conn,
		sendCh:      make(chan wsOutMsg, rdpTunnelSendQueueDepth),
		preflightCh: make(chan remotesession.RDPStatus, 4),
		streams:     make(map[uint32]*rdpTunnelStream),
		stopCh:      make(chan struct{}),
		wsDoneCh:    make(chan struct{}),
	}

	f.wg.Add(2)
	go f.wsWriteLoop()
	go f.wsReadLoop()

	return f, nil
}

func (f *rdpTunnelForwarder) Done() <-chan struct{} {
	return f.wsDoneCh
}

func (f *rdpTunnelForwarder) Addr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listener == nil {
		return ""
	}
	return f.listener.Addr().String()
}

func (f *rdpTunnelForwarder) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", f.cfg.ListenAddr)
	if err != nil {
		return err
	}

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		_ = ln.Close()
		return fmt.Errorf("forwarder encerrado")
	}
	f.listener = ln
	f.mu.Unlock()

	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			f.wg.Add(1)
			go func(c net.Conn) {
				defer f.wg.Done()
				f.handleAcceptedTCP(ctx, c)
			}(conn)
		}
	}()

	return nil
}

func (f *rdpTunnelForwarder) RequestPreflight(ctx context.Context, timeout time.Duration) (remotesession.RDPStatus, error) {
	if err := f.enqueueJSON(remotesession.TunnelControlMsg{
		StreamID: 0,
		Op:       "preflight",
	}); err != nil {
		return remotesession.RDPStatus{}, err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case st := <-f.preflightCh:
		return st, nil
	case <-timer.C:
		return remotesession.RDPStatus{}, fmt.Errorf("timeout aguardando resposta de preflight")
	case <-ctx.Done():
		return remotesession.RDPStatus{}, ctx.Err()
	case <-f.wsDoneCh:
		return remotesession.RDPStatus{}, fmt.Errorf("conexao WebSocket encerrada antes do preflight")
	}
}

func (f *rdpTunnelForwarder) handleAcceptedTCP(ctx context.Context, conn net.Conn) {
	sid := f.nextStream.Add(1)
	if sid == 0 {
		sid = f.nextStream.Add(1)
	}

	s := &rdpTunnelStream{
		id:         sid,
		conn:       conn,
		openCh:     make(chan rdpTunnelOpenResult, 1),
		writeCh:    make(chan []byte, rdpTunnelWriteQueueDepth),
		writeEndCh: make(chan struct{}),
		abortCh:    make(chan struct{}),
	}

	f.mu.Lock()
	if f.closed || len(f.streams) >= rdpTunnelMaxStreams {
		f.mu.Unlock()
		_ = conn.Close()
		return
	}
	f.streams[sid] = s
	f.mu.Unlock()

	cleanup := func() {
		f.removeStream(sid, s)
		s.abort()
	}

	if err := f.enqueueJSON(remotesession.TunnelControlMsg{
		StreamID:   sid,
		Op:         "open",
		TargetHost: f.cfg.TargetHost,
		TargetPort: f.cfg.TargetPort,
		Protocol:   f.cfg.Protocol,
	}); err != nil {
		cleanup()
		return
	}

	openTimer := time.NewTimer(rdpTunnelOpenTimeout)
	defer openTimer.Stop()

	select {
	case res := <-s.openCh:
		if !res.opened {
			cleanup()
			return
		}
	case <-openTimer.C:
		f.sendCloseControl(s)
		cleanup()
		return
	case <-ctx.Done():
		f.sendCloseControl(s)
		cleanup()
		return
	case <-f.wsDoneCh:
		cleanup()
		return
	case <-s.abortCh:
		cleanup()
		return
	}

	var pumpWG sync.WaitGroup
	pumpWG.Add(2)

	go func() {
		defer pumpWG.Done()
		f.tcpToWSLoop(s)
	}()

	go func() {
		defer pumpWG.Done()
		f.wsToTCPLoop(s)
	}()

	pumpWG.Wait()
	cleanup()
}

func (f *rdpTunnelForwarder) tcpToWSLoop(s *rdpTunnelStream) {
	buf := make([]byte, rdpTunnelReadChunkBytes)
	for {
		n, err := s.conn.Read(buf)
		if n > 0 {
			frame := remotesession.EncodeTunnelBinaryData(nil, s.id, buf[:n])
			if !f.enqueueWS(websocket.BinaryMessage, frame) {
				s.abort()
				return
			}
		}
		if err != nil {
			f.sendCloseControl(s)
			if !errors.Is(err, io.EOF) {
				s.abort()
			}
			return
		}
	}
}

func (f *rdpTunnelForwarder) wsToTCPLoop(s *rdpTunnelStream) {
	for {
		select {
		case <-s.abortCh:
			return
		case data := <-s.writeCh:
			if err := writeAllForwarderTCP(s.conn, data); err != nil {
				f.sendCloseControl(s)
				s.abort()
				return
			}
		case <-s.writeEndCh:
			for {
				select {
				case data := <-s.writeCh:
					if err := writeAllForwarderTCP(s.conn, data); err != nil {
						s.abort()
						return
					}
				default:
					if cw, ok := s.conn.(interface{ CloseWrite() error }); ok {
						_ = cw.CloseWrite()
					} else {
						_ = s.conn.Close()
					}
					return
				}
			}
		}
	}
}

func (f *rdpTunnelForwarder) wsReadLoop() {
	defer f.wg.Done()
	defer close(f.wsDoneCh)

	for {
		_, msg, err := f.wsConn.ReadMessage()
		if err != nil {
			f.abortAllStreams()
			return
		}

		isBin, opByte, streamID, payload, ctrl, derr := remotesession.DecodeTunnelFrame(msg)
		if derr != nil {
			continue
		}

		if isBin {
			s := f.getStream(streamID)
			if s == nil {
				continue
			}
			switch opByte {
			case remotesession.TunnelOpData:
				f.enqueueStreamWrite(s, payload)
			case remotesession.TunnelOpClose:
				s.endWrite()
			}
			continue
		}

		switch ctrl.Op {
		case "preflight":
			if ctrl.RdpStatus != nil {
				select {
				case f.preflightCh <- *ctrl.RdpStatus:
				default:
				}
			}
		case "opened":
			if s := f.getStream(ctrl.StreamID); s != nil {
				s.resolveOpen(true, "")
			}
		case "error":
			if s := f.getStream(ctrl.StreamID); s != nil {
				s.resolveOpen(false, ctrl.Error)
				s.abort()
			}
		case "close":
			if s := f.getStream(ctrl.StreamID); s != nil {
				s.resolveOpen(false, "closed")
				s.endWrite()
			}
		case "data":
			if s := f.getStream(ctrl.StreamID); s != nil && ctrl.DataB64 != "" {
				if decoded, berr := base64.StdEncoding.DecodeString(ctrl.DataB64); berr == nil {
					f.enqueueStreamWrite(s, decoded)
				}
			}
		}
	}
}

func (f *rdpTunnelForwarder) wsWriteLoop() {
	defer f.wg.Done()

	ticker := time.NewTicker(rdpTunnelPingInterval)
	defer ticker.Stop()

	for {
		select {
		case msg := <-f.sendCh:
			_ = f.wsConn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := f.wsConn.WriteMessage(msg.msgType, msg.data); err != nil {
				return
			}
		case <-ticker.C:
			_ = f.wsConn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
		case <-f.stopCh:
			// Drain queued outbound close/control messages briefly.
			deadline := time.Now().Add(200 * time.Millisecond)
			for time.Now().Before(deadline) {
				select {
				case msg := <-f.sendCh:
					_ = f.wsConn.SetWriteDeadline(time.Now().Add(2 * time.Second))
					_ = f.wsConn.WriteMessage(msg.msgType, msg.data)
				default:
					_ = f.wsConn.WriteControl(
						websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"),
						time.Now().Add(1*time.Second),
					)
					return
				}
			}
			return
		}
	}
}

func (f *rdpTunnelForwarder) enqueueJSON(msg remotesession.TunnelControlMsg) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if !f.enqueueWS(websocket.TextMessage, b) {
		return fmt.Errorf("fila de envio WebSocket indisponivel")
	}
	return nil
}

func (f *rdpTunnelForwarder) enqueueWS(msgType int, data []byte) bool {
	cp := make([]byte, len(data))
	copy(cp, data)

	select {
	case <-f.stopCh:
		return false
	case <-f.wsDoneCh:
		return false
	default:
	}

	select {
	case f.sendCh <- wsOutMsg{msgType: msgType, data: cp}:
		return true
	case <-f.stopCh:
		return false
	case <-f.wsDoneCh:
		return false
	}
}

func (f *rdpTunnelForwarder) sendCloseControl(s *rdpTunnelStream) {
	s.closeMsgOnce.Do(func() {
		_ = f.enqueueJSON(remotesession.TunnelControlMsg{
			StreamID: s.id,
			Op:       "close",
		})
	})
}

func (f *rdpTunnelForwarder) enqueueStreamWrite(s *rdpTunnelStream, data []byte) {
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

	select {
	case s.writeCh <- cp:
	case <-s.abortCh:
	case <-s.writeEndCh:
	}
}

func (f *rdpTunnelForwarder) getStream(id uint32) *rdpTunnelStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streams[id]
}

func (f *rdpTunnelForwarder) removeStream(id uint32, expected *rdpTunnelStream) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cur, ok := f.streams[id]; ok && (expected == nil || cur == expected) {
		delete(f.streams, id)
	}
}

func (f *rdpTunnelForwarder) abortAllStreams() {
	f.mu.Lock()
	active := make([]*rdpTunnelStream, 0, len(f.streams))
	for id, s := range f.streams {
		active = append(active, s)
		delete(f.streams, id)
	}
	f.mu.Unlock()
	for _, s := range active {
		s.resolveOpen(false, "websocket closed")
		s.abort()
	}
}

// Close encerra listener, streams ativos e conexao WebSocket.
func (f *rdpTunnelForwarder) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	f.closed = true
	ln := f.listener
	f.listener = nil
	active := make([]*rdpTunnelStream, 0, len(f.streams))
	for id, s := range f.streams {
		active = append(active, s)
		delete(f.streams, id)
	}
	f.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	for _, s := range active {
		f.sendCloseControl(s)
		s.resolveOpen(false, "shutdown")
		s.abort()
	}

	f.stopOnce.Do(func() {
		close(f.stopCh)
	})
	if f.wsConn != nil {
		_ = f.wsConn.Close()
	}
	f.wg.Wait()
	return nil
}

func (s *rdpTunnelStream) resolveOpen(opened bool, errMsg string) {
	s.openOnce.Do(func() {
		s.openCh <- rdpTunnelOpenResult{opened: opened, errMsg: errMsg}
	})
}

func (s *rdpTunnelStream) endWrite() {
	s.writeEndOnce.Do(func() {
		close(s.writeEndCh)
	})
}

func (s *rdpTunnelStream) abort() {
	s.abortOnce.Do(func() {
		close(s.abortCh)
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
}

func writeAllForwarderTCP(conn net.Conn, data []byte) error {
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
