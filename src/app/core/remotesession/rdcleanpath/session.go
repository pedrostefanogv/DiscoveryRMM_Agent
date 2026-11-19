package rdcleanpath

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Transport e o canal de bytes entre o cliente (IronRDP no browser) e o agent.
// Abstrai NATS/WebSocket para permitir testes deterministicos.
//
// IMPORTANTE: o transporte carrega DUAS fases distintas no MESMO canal:
//
//	fase 1 (handshake): cliente envia o RDCleanPath Request (DER), agent responde
//	                    o Response (DER) e a conexao troca de natureza;
//	fase 2 (relay):     bytes RDP crus em ambas as direcoes, sem framing.
//
// Nao ha discriminacao por prefixo: e a maquina de estados (Session) que sabe em
// qual fase esta. Ver SessionState.
type Transport interface {
	// Send envia bytes ao cliente. Deve ser seguro para chamada concorrente.
	Send(data []byte) error
	// Recv devolve o proximo bloco do cliente (bloqueante). io.EOF encerra.
	Recv() ([]byte, error)
	// Close encerra o transporte (idempotente).
	Close() error
}

// SessionState e a fase da sessao RDCleanPath.
type SessionState int32

const (
	// StateAwaitingRequest aguarda o RDCleanPath Request (DER) do cliente.
	StateAwaitingRequest SessionState = iota
	// StateRelaying faz relay de bytes RDP crus apos o handshake.
	StateRelaying
	// StateClosed encerrou (por EOF, erro, timeout ou cancelamento).
	StateClosed
)

func (s SessionState) String() string {
	switch s {
	case StateAwaitingRequest:
		return "awaiting_request"
	case StateRelaying:
		return "relaying"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// SessionConfig configura uma sessao RDCleanPath completa.
type SessionConfig struct {
	// Server e o servidor RDCleanPath (obrigatorio). Ele impoe o destino
	// fail-closed 127.0.0.1:<port> e valida o proxy_auth.
	Server *Server
	// RequestTimeout limita a espera pelo RDCleanPath Request inicial. Uma
	// conexao que abre e nao envia nada NAO pode ficar pendurada indefinidamente.
	// Default: 15s.
	RequestTimeout time.Duration
	// HandshakeTimeout limita o handshake X.224/TLS contra o TermService.
	// Default: 30s.
	HandshakeTimeout time.Duration
	// MaxRequestBytes limita o tamanho do request DER aceito (anti-DoS).
	// Default: 64 KiB.
	MaxRequestBytes int
	// Logf e o logger opcional.
	Logf func(format string, args ...any)
}

// SessionStats contabiliza o trafego de uma sessao.
type SessionStats struct {
	BytesToServer atomic.Int64
	BytesToClient atomic.Int64
	FramesFromIn  atomic.Int64
}

// Session executa a maquina de estados RDCleanPath sobre um Transport:
//
//  1. StateAwaitingRequest: acumula bytes ate ter um DER completo; decodifica.
//  2. Delega ao Server.HandleRequest (politica fail-closed + X.224 + TLS).
//  3. Envia o Response (ou Error PDU) ao cliente.
//  4. StateRelaying: relay TCP puro ate qualquer lado encerrar.
//
// Cada mensagem do transporte e um bloco, NAO necessariamente uma mensagem
// inteira: o request DER pode chegar fragmentado em varios publishs NATS.
type Session struct {
	cfg      SessionConfig
	stats    SessionStats
	state    atomic.Int32
	closed   atomic.Bool
	stopOnce sync.Once

	mu     sync.Mutex
	conn   net.Conn
	doneCh chan struct{}

	// pump e o leitor persistente (fallback quando o transporte nao implementa
	// RecvWithContext). pumpOnce garante UMA goroutine por sessao.
	pumpOnce sync.Once
	pump     *recvPump
	pumpStop chan struct{}
}

// NewSession cria uma sessao com os defaults aplicados.
func NewSession(cfg SessionConfig) *Session {
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 15 * time.Second
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 30 * time.Second
	}
	if cfg.MaxRequestBytes <= 0 {
		cfg.MaxRequestBytes = 64 * 1024
	}
	s := &Session{cfg: cfg, doneCh: make(chan struct{}), pumpStop: make(chan struct{})}
	s.state.Store(int32(StateAwaitingRequest))
	return s
}

// State devolve a fase atual.
func (s *Session) State() SessionState { return SessionState(s.state.Load()) }

// Stats devolve uma copia dos contadores.
func (s *Session) Stats() (toServer, toClient, frames int64) {
	return s.stats.BytesToServer.Load(), s.stats.BytesToClient.Load(), s.stats.FramesFromIn.Load()
}

// Done e fechado quando a sessao encerra.
func (s *Session) Done() <-chan struct{} { return s.doneCh }

func (s *Session) logf(format string, args ...any) {
	if s.cfg.Logf != nil {
		s.cfg.Logf(format, args...)
	}
}

func (s *Session) setState(st SessionState) { s.state.Store(int32(st)) }

// Run executa a sessao ate encerrar. Sempre fecha o transporte e a conexao TLS.
func (s *Session) Run(ctx context.Context, t Transport) error {
	if s.cfg.Server == nil {
		_ = t.Close()
		s.finish()
		return fmt.Errorf("rdcleanpath: Server obrigatorio")
	}

	defer func() {
		s.closeConn()
		_ = t.Close()
		s.setState(StateClosed)
		s.finish()
	}()

	// ── Fase 1: obter o RDCleanPath Request ──
	reqDER, err := s.readRequest(ctx, t)
	if err != nil {
		s.logf("[rdcleanpath] falha ao ler request: %v", err)
		// Erro de protocolo e comunicado in-band quando possivel.
		if errors.Is(err, errRequestTooLarge) {
			_ = t.Send(s.cfg.Server.errorPDU(ErrGeneralError, httpStatusBadRequest))
		}
		return err
	}

	// ── Fase 2: handshake (delega ao Server, que impoe o destino fail-closed) ──
	hsCtx, cancel := context.WithTimeout(ctx, s.cfg.HandshakeTimeout)
	respDER, conn, hsErr := s.cfg.Server.HandleRequest(hsCtx, reqDER)
	cancel()

	if respDER != nil {
		if sendErr := t.Send(respDER); sendErr != nil {
			s.logf("[rdcleanpath] falha ao enviar response: %v", sendErr)
			if conn != nil {
				_ = conn.Close()
			}
			return sendErr
		}
	}
	if hsErr != nil || conn == nil {
		// O Error PDU ja foi enviado; encerra a sessao.
		s.logf("[rdcleanpath] handshake recusado: %v", hsErr)
		return hsErr
	}

	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	// ── Fase 3: relay de bytes RDP crus ──
	s.setState(StateRelaying)
	s.logf("[rdcleanpath] handshake ok; iniciando relay")

	toServer, toClient, relayErr := s.relay(ctx, t, conn)
	s.stats.BytesToServer.Add(toServer)
	s.stats.BytesToClient.Add(toClient)
	if relayErr != nil && !errors.Is(relayErr, io.EOF) && !errors.Is(relayErr, context.Canceled) {
		s.logf("[rdcleanpath] relay encerrado com erro: %v", relayErr)
		return relayErr
	}
	return nil
}

// errRequestTooLarge indica request acima do teto.
var errRequestTooLarge = errors.New("rdcleanpath: request excede o tamanho maximo")

// readRequest acumula mensagens ate formar um DER completo.
//
// O transporte entrega BLOCOS, nao mensagens: um request DER grande pode chegar
// em varios publishs. Tentamos decodificar a cada bloco acumulado; quando o DER
// esta completo e valido, paramos.
func (s *Session) readRequest(ctx context.Context, t Transport) ([]byte, error) {
	deadline := time.Now().Add(s.cfg.RequestTimeout)
	var buf []byte

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("rdcleanpath: timeout aguardando request (%s)", s.cfg.RequestTimeout)
		}

		// Lê o próximo bloco com deadline derivado do prazo restante.
		remaining := time.Until(deadline)
		block, err := s.recvBlock(ctx, t, remaining)
		if err != nil {
			return nil, err
		}
		s.stats.FramesFromIn.Add(1)
		if len(block) == 0 {
			continue
		}

		buf = append(buf, block...)
		if len(buf) > s.cfg.MaxRequestBytes {
			return nil, errRequestTooLarge
		}

		// DER completo? Decode valida a estrutura inteira (inclui rest == 0).
		if _, derr := Decode(buf); derr == nil {
			return buf, nil
		}
		// Incompleto: continua acumulando ate o deadline.
	}
}

// recvBlock le um bloco respeitando contexto e timeout.
//
// Estrategia:
//  1. Se o transporte implementa RecvWithContext, usa o caminho NATIVO: o
//     cancelamento e observado dentro do proprio recv, sem goroutine alguma.
//     Isso e o caso do NatsTransport (producao).
//  2. Caso contrario, usa um leitor PERSISTENTE (pump) dono do Session: ha no
//     maximo UMA goroutine de leitura por sessao, e o bloco lido nunca e
//     descartado num timeout — ele fica no channel ate o proximo consumo.
//
// O padrao anterior (uma goroutine por chamada) vazava goroutines e PERDIA
// blocos: um timeout deixava o leitor preso em Recv() e o bloco seguinte podia
// ser entregue a esse leitor abandonado.
func (s *Session) recvBlock(ctx context.Context, t Transport, timeout time.Duration) ([]byte, error) {
	if native, ok := t.(RecvWithContext); ok {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		block, err := native.RecvContext(rctx)
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("rdcleanpath: timeout aguardando request")
		}
		return block, err
	}

	pump := s.ensurePump(t)
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r, ok := <-pump.out:
		if !ok {
			return nil, io.EOF
		}
		return r.data, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("rdcleanpath: timeout aguardando request")
	}
}

// pumpResult e um bloco (ou erro) entregue pelo leitor persistente.
type pumpResult struct {
	data []byte
	err  error
}

// recvPump e o leitor persistente de uma sessao (fallback sem RecvContext).
type recvPump struct {
	out  chan pumpResult
	once sync.Once
}

// ensurePump cria (uma unica vez) o leitor persistente da sessao.
func (s *Session) ensurePump(t Transport) *recvPump {
	s.pumpOnce.Do(func() {
		p := &recvPump{out: make(chan pumpResult, 1)}
		s.pump = p
		go func() {
			defer close(p.out)
			for {
				data, err := t.Recv()
				// Buffered (cap 1) + entrega obrigatoria: nunca descarta bloco.
				select {
				case p.out <- pumpResult{data: data, err: err}:
				case <-s.pumpStop:
					return
				}
				if err != nil {
					return
				}
			}
		}()
	})
	return s.pump
}

// relay faz a ponte entre o transporte e o socket TLS.
func (s *Session) relay(ctx context.Context, t Transport, conn net.Conn) (int64, int64, error) {
	var wg sync.WaitGroup
	wg.Add(2)

	var toServer, toClient int64
	var firstErr error
	var errMu sync.Mutex
	recordErr := func(e error) {
		if e == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		errMu.Unlock()
	}

	// closeServerOnce garante que a conexao TLS e fechada UMA vez quando qualquer
	// direcao termina — sem isso a direcao oposta fica presa em Read/Write
	// bloqueante para sempre (deadlock no fim da sessao).
	var closeServerOnce sync.Once
	closeServer := func() {
		closeServerOnce.Do(func() { _ = conn.Close() })
	}
	// Encerra a conexao quando o contexto for cancelado.
	ctxDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			closeServer()
		case <-ctxDone:
		}
	}()
	defer close(ctxDone)

	// cliente -> TermService
	go func() {
		defer wg.Done()
		defer closeServer() // destrava a direcao oposta
		var total int64
		for {
			select {
			case <-ctx.Done():
				toServer = total
				return
			default:
			}
			data, err := s.relayRecv(ctx, t)
			if len(data) > 0 {
				if _, wErr := conn.Write(data); wErr != nil {
					recordErr(wErr)
					toServer = total
					return
				}
				total += int64(len(data))
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					recordErr(err)
				}
				toServer = total
				return
			}
		}
	}()

	// TermService -> cliente
	go func() {
		defer wg.Done()
		defer closeServer() // destrava a direcao oposta
		var total int64
		buf := make([]byte, 32*1024)
		for {
			select {
			case <-ctx.Done():
				toClient = total
				return
			default:
			}
			n, rErr := conn.Read(buf)
			if n > 0 {
				if sErr := t.Send(buf[:n]); sErr != nil {
					recordErr(sErr)
					toClient = total
					return
				}
				total += int64(n)
			}
			if rErr != nil {
				if !errors.Is(rErr, io.EOF) {
					recordErr(rErr)
				}
				toClient = total
				return
			}
		}
	}()

	wg.Wait()
	errMu.Lock()
	defer errMu.Unlock()
	return toServer, toClient, firstErr
}

// relayRecv le do transporte no relay, respeitando o contexto.
//
// No relay NAO ha timeout: a sessao dura o quanto durar a conexao RDP. O que
// importa e o cancelamento (ctx) e nao vazar goroutine. Usa o caminho nativo
// quando disponivel e o leitor persistente caso contrario.
func (s *Session) relayRecv(ctx context.Context, t Transport) ([]byte, error) {
	if native, ok := t.(RecvWithContext); ok {
		return native.RecvContext(ctx)
	}
	pump := s.ensurePump(t)
	select {
	case r, ok := <-pump.out:
		if !ok {
			return nil, io.EOF
		}
		return r.data, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close aborta a sessao.
func (s *Session) Close() {
	if s.closed.Swap(true) {
		return
	}
	s.closeConn()
}

func (s *Session) closeConn() {
	s.mu.Lock()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

func (s *Session) finish() {
	close(s.doneCh)
	// Encerra o leitor persistente (se existir) para nao vazar goroutine.
	s.stopOnce.Do(func() { close(s.pumpStop) })
}
