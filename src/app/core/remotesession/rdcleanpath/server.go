package rdcleanpath

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server implementa o lado proxy do RDCleanPath para sessoes RDP do agent.
//
// Seguranca (fail-closed): o campo `destination` do request vem do cliente e e
// IGNORADO. O destino e sempre 127.0.0.1:<RDPPort>, imposto por este servidor e
// validado pela allowlist injetada. Isso impede que um cliente use o tunel como
// SSRF para alcancar outros hosts ou portas.
type Server struct {
	// RDPPort e a porta do TermService no proprio host (default 3389).
	RDPPort int
	// DialConnectTimeout limita a conexao TCP ao TermService.
	DialConnectTimeout time.Duration
	// AllowInsecureTLS aceita o certificado self-signed do RDP (padrao do IronRDP).
	AllowInsecureTLS bool
	// Authenticate valida o proxy_auth do cliente. Obrigatorio: quando nil, todo
	// request e recusado (fail-closed). Recebe o valor cru e o destination pedido.
	Authenticate func(ctx context.Context, proxyAuth, destination string) error
	// Logf e um logger opcional.
	Logf func(format string, args ...any)
	// dial injetavel para testes; quando nil usa o dial fail-closed padrao.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// rdpLoopbackHost e o unico destino permitido para sessoes RDP.
const rdpLoopbackHost = "127.0.0.1"

// ErrUnauthorized indica proxy_auth invalido/ausente.
var ErrUnauthorized = fmt.Errorf("rdcleanpath: proxy_auth invalido")

// TargetAddr devolve o destino fail-closed "127.0.0.1:<port>".
func (s *Server) TargetAddr() string {
	return net.JoinHostPort(rdpLoopbackHost, strconv.Itoa(s.EffectiveRDPPort()))
}

// EffectiveRDPPort normaliza a porta RDP configurada.
func (s *Server) EffectiveRDPPort() int {
	if s.RDPPort <= 0 || s.RDPPort > 65535 {
		return 3389
	}
	return s.RDPPort
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// DialFailClosed e o dial padrao: apenas 127.0.0.1:<RDPPort> e alcancavel.
// Qualquer outro destino e recusado ANTES de abrir o socket.
func (s *Server) DialFailClosed(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("endereco invalido %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("porta invalida %q: %w", portStr, err)
	}
	if host != rdpLoopbackHost || port != s.EffectiveRDPPort() {
		return nil, fmt.Errorf("allowlist_denied: sessao RDP permite apenas %s", s.TargetAddr())
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// dialFn devolve o dial efetivo (injetado em testes, fail-closed em producao).
func (s *Server) dialFn() func(ctx context.Context, network, addr string) (net.Conn, error) {
	if s.dial != nil {
		return s.dial
	}
	return s.DialFailClosed
}

// HandleRequest processa um RDCleanPath Request e devolve o Response PDU que deve
// ser enviado ao cliente, junto com o socket TLS para o relay.
//
// Quando o handshake falha, devolve um Error PDU (nunca um erro de Go): o
// protocolo exige que a falha seja comunicada in-band ao cliente IronRDP.
func (s *Server) HandleRequest(ctx context.Context, requestDER []byte) (responseDER []byte, conn net.Conn, err error) {
	req, decErr := Decode(requestDER)
	if decErr != nil {
		s.logf("[rdcleanpath] request invalido: %v", decErr)
		return s.errorPDU(ErrGeneralError, httpStatusBadRequest), nil, nil
	}

	// ── Autorizacao fail-closed ──
	if s.Authenticate == nil {
		s.logf("[rdcleanpath] recusado: Authenticate nao configurado")
		return s.errorPDU(ErrGeneralError, httpStatusUnauthorized), nil, ErrUnauthorized
	}
	destination := ""
	if req.Destination != nil {
		destination = *req.Destination
	}
	proxyAuth := ""
	if req.ProxyAuth != nil {
		proxyAuth = *req.ProxyAuth
	}
	if authErr := s.Authenticate(ctx, proxyAuth, destination); authErr != nil {
		s.logf("[rdcleanpath] proxy_auth recusado: %v", authErr)
		return s.errorPDU(ErrGeneralError, httpStatusUnauthorized), nil, authErr
	}

	// ── O destino do cliente e registrado mas NUNCA usado ──
	target := s.TargetAddr()
	if destination != "" && !s.destinationMatches(destination) {
		// Nao e erro: o IronRDP envia o endereco que "acha" que vai conectar.
		// Registramos para auditoria e seguimos com o destino fail-closed.
		s.logf("[rdcleanpath] destination do cliente %q ignorado; usando %s", destination, target)
	}

	if len(req.X224ConnectionPdu) == 0 {
		s.logf("[rdcleanpath] request sem x224_connection_pdu")
		// Para o VMConnect shape o X.224 vem ausente; nao suportado nesta versao.
		return s.errorPDU(ErrNegotiationError, httpStatusBadRequest), nil, fmt.Errorf("rdcleanpath: x224 ausente")
	}

	res, hsErr := Handshake(ctx, HandshakeConfig{
		Dial:               s.dialFn(),
		Addr:               target,
		ServerName:         rdpLoopbackHost,
		X224Request:        req.X224ConnectionPdu,
		InsecureSkipVerify: s.AllowInsecureTLS,
		DialTimeout:        s.DialConnectTimeout,
	})
	if hsErr != nil {
		code := classifyHandshakeError(hsErr)
		s.logf("[rdcleanpath] handshake falhou (%d): %v", code, hsErr)
		return s.errorPDU(code, httpStatusBadGateway), nil, hsErr
	}

	resp := NewResponse(res.ServerAddr, res.X224Response, res.CertChain)
	respDER, encErr := resp.Encode()
	if encErr != nil {
		_ = res.Conn.Close()
		s.logf("[rdcleanpath] falha ao codificar response: %v", encErr)
		return s.errorPDU(ErrGeneralError, httpStatusInternalServerError), nil, encErr
	}
	return respDER, res.Conn, nil
}

// destinationMatches informa se o destination do cliente coincide com o alvo
// fail-closed (usado apenas para logging/auditoria).
func (s *Server) destinationMatches(destination string) bool {
	d := strings.TrimSpace(destination)
	if d == "" {
		return true
	}
	host, portStr, err := net.SplitHostPort(d)
	if err != nil {
		// Sem porta explicita: compara so o host, assumindo 3389.
		return strings.EqualFold(d, rdpLoopbackHost) && s.EffectiveRDPPort() == 3389
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return false
	}
	return host == rdpLoopbackHost && port == s.EffectiveRDPPort()
}

// Error PDU HTTP status codes usados nas respostas de falha.
const (
	httpStatusBadRequest          uint16 = 400
	httpStatusUnauthorized        uint16 = 401
	httpStatusBadGateway          uint16 = 502
	httpStatusInternalServerError uint16 = 500
)

// errorPDU monta o Error PDU correspondente.
func (s *Server) errorPDU(code, httpStatus uint16) []byte {
	pdu := NewErrorResponse(code, &httpStatus)
	der, err := pdu.Encode()
	if err != nil {
		// Fallback minimo: nunca deve acontecer (encoder nao falha para estes campos).
		s.logf("[rdcleanpath] falha ao codificar Error PDU: %v", err)
		return nil
	}
	return der
}

// classifyHandshakeError mapeia o erro do handshake para o codigo RDCleanPath.
func classifyHandshakeError(err error) uint16 {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "allowlist_denied"):
		// Destino bloqueado pela politica: tratamos como alvo inalcancavel para
		// nao vazar detalhes da allowlist ao cliente.
		return ErrTargetUnreachable
	case strings.Contains(msg, "handshake tls"):
		return ErrTLSHandshakeFailed
	case strings.Contains(msg, "conexao ao servidor rdp falhou"):
		return ErrTargetUnreachable
	default:
		return ErrGeneralError
	}
}

// ── Relay ───────────────────────────────────────────────────────────────────

// Relay faz a ponte bidirecional entre o canal do cliente (write/read de bytes
// crus do RDCleanPath) e o socket TLS do servidor RDP.
//
// writeToClient recebe bytes que devem ir ao cliente; readFromClient devolve os
// bytes enviados pelo cliente (bloqueante). O relay termina quando qualquer lado
// fecha ou o contexto e cancelado.
type ClientStream interface {
	// Read devolve o proximo bloco do cliente. io.EOF encerra o relay.
	Read(p []byte) (int, error)
	// Write envia um bloco ao cliente.
	Write(p []byte) (int, error)
}

// Relay copia dados entre o cliente e o socket TLS ate um dos lados encerrar.
// Devolve os bytes transferidos em cada direcao.
func Relay(ctx context.Context, stream ClientStream, server net.Conn) (toServer, toClient int64, err error) {
	var wg sync.WaitGroup
	wg.Add(2)

	errCh := make(chan error, 2)
	var mu sync.Mutex

	// client -> servidor RDP
	go func() {
		defer wg.Done()
		n, cpErr := copyWithContext(ctx, server, stream)
		mu.Lock()
		toServer = n
		mu.Unlock()
		if cpErr != nil && cpErr != context.Canceled {
			errCh <- cpErr
		}
		// Meia-fechamento: sinaliza EOF ao servidor RDP.
		if cw, ok := server.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()

	// servidor RDP -> client
	go func() {
		defer wg.Done()
		n, cpErr := copyWithContext(ctx, stream, server)
		mu.Lock()
		toClient = n
		mu.Unlock()
		if cpErr != nil && cpErr != context.Canceled {
			errCh <- cpErr
		}
	}()

	wg.Wait()
	close(errCh)
	for e := range errCh {
		if e != nil {
			err = e
			break
		}
	}
	return toServer, toClient, err
}

// copyWithContext copia de src para dst respeitando o cancelamento do contexto.
func copyWithContext(ctx context.Context, dst ClientStream, src ClientStream) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		n, rErr := src.Read(buf)
		if n > 0 {
			if _, wErr := dst.Write(buf[:n]); wErr != nil {
				return total, wErr
			}
			total += int64(n)
		}
		if rErr != nil {
			return total, rErr
		}
	}
}
