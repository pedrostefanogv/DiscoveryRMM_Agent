package rdcleanpath

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"time"
)

// Constantes do handshake RDP. O X.224 Connection Confirm do TermService e
// pequeno; 8 KiB cobre com folga qualquer resposta de negociacao real.
const (
	// handshakeReadTimeout limita a espera pelo X.224 Connection Confirm.
	handshakeReadTimeout = 15 * time.Second
	// tlsHandshakeTimeout limita o handshake TLS com o servidor RDP.
	tlsHandshakeTimeout = 15 * time.Second
	// maxX224ResponseBytes limita o tamanho do X.224 lido (anti-DoS).
	maxX224ResponseBytes = 8 * 1024
)

// HandshakeResult carrega o resultado do handshake contra o servidor RDP.
type HandshakeResult struct {
	// X224Response e o X.224 Connection Confirm devolvido pelo servidor.
	X224Response []byte
	// CertChain sao os certificados DER do servidor (leaf primeiro).
	CertChain [][]byte
	// Conn e o socket TLS ja estabelecido, pronto para o relay.
	Conn net.Conn
	// ServerAddr e o "host:port" efetivamente conectado.
	ServerAddr string
}

// HandshakeConfig configura o handshake RDCleanPath contra o servidor RDP.
type HandshakeConfig struct {
	// Dial e obrigatorio e deve aplicar a politica fail-closed (allowlist).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Addr e o destino "host:port" (para sessoes RDP sempre 127.0.0.1:<port>).
	Addr string
	// ServerName e o SNI enviado no handshake TLS.
	ServerName string
	// X224Request e o X.224 Connection Request vindo do cliente.
	X224Request []byte
	// InsecureSkipVerify aceita o certificado self-signed tipico do RDP.
	// O IronRDP faz o mesmo: a cadeia e entregue ao cliente para validacao.
	InsecureSkipVerify bool
	// DialTimeout e o timeout da conexao TCP.
	DialTimeout time.Duration
}

// Handshake executa a sequencia TCP -> X.224 -> TLS e devolve o socket TLS pronto
// para relay, junto com a resposta X.224 e a cadeia de certificados.
//
// Esta funcao NAO valida o destino: a validacao fail-closed e responsabilidade do
// Dial injetado (allowlist). Ver Server.RouteRequest, que forca 127.0.0.1:<port>.
func Handshake(ctx context.Context, cfg HandshakeConfig) (*HandshakeResult, error) {
	if cfg.Dial == nil {
		return nil, fmt.Errorf("rdcleanpath: Dial nao configurado")
	}
	if cfg.Addr == "" {
		return nil, fmt.Errorf("rdcleanpath: Addr obrigatorio")
	}
	if len(cfg.X224Request) == 0 {
		return nil, fmt.Errorf("rdcleanpath: X.224 Connection Request vazio")
	}

	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 10 * time.Second
	}

	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	conn, err := cfg.Dial(dialCtx, "tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("rdcleanpath: conexao ao servidor RDP falhou: %w", err)
	}

	// A partir daqui qualquer erro deve fechar o socket.
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()

	// ── Passo 1: enviar o X.224 Connection Request cru (sem TLS ainda) ──
	if _, err := conn.Write(cfg.X224Request); err != nil {
		return nil, fmt.Errorf("rdcleanpath: envio do X.224 falhou: %w", err)
	}

	// ── Passo 2: ler o X.224 Connection Confirm ──
	if dl, canDL := conn.(interface{ SetReadDeadline(time.Time) error }); canDL {
		_ = dl.SetReadDeadline(time.Now().Add(handshakeReadTimeout))
	}
	x224Response, err := readX224Response(conn)
	if err != nil {
		return nil, fmt.Errorf("rdcleanpath: leitura do X.224 falhou: %w", err)
	}
	// Limpa o deadline de leitura antes do TLS (o handshake tem timeout proprio).
	if dl, canDL := conn.(interface{ SetReadDeadline(time.Time) error }); canDL {
		_ = dl.SetReadDeadline(time.Time{})
	}

	// ── Passo 3: TLS sobre o MESMO socket, ja com o X.224 trocado ──
	serverName := cfg.ServerName
	if serverName == "" {
		host, _, splitErr := net.SplitHostPort(cfg.Addr)
		if splitErr == nil {
			serverName = host
		}
	}

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // RDP usa self-signed; a cadeia vai ao cliente
		MinVersion:         tls.VersionTLS12,
	})

	tlsCtx, tlsCancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
	defer tlsCancel()
	if err := tlsConn.HandshakeContext(tlsCtx); err != nil {
		return nil, fmt.Errorf("rdcleanpath: handshake TLS falhou: %w", err)
	}

	// ── Passo 4: extrair a cadeia de certificados ──
	certs, err := extractCertChain(tlsConn)
	if err != nil {
		return nil, fmt.Errorf("rdcleanpath: extracao de certificados falhou: %w", err)
	}

	ok = true
	return &HandshakeResult{
		X224Response: x224Response,
		CertChain:    certs,
		Conn:         tlsConn,
		ServerAddr:   cfg.Addr,
	}, nil
}

// readX224Response le o X.224 Connection Confirm.
//
// O TermService responde em uma unica mensagem TPKT, mas a leitura em pedacos e
// tratada para nao truncar a resposta: lemos ate obter o tamanho declarado no
// cabecalho TPKT (RFC 1006 / X.224), com teto de maxX224ResponseBytes.
func readX224Response(conn net.Conn) ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 4096)

	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) > maxX224ResponseBytes {
				return nil, fmt.Errorf("resposta X.224 excede %d bytes", maxX224ResponseBytes)
			}
			// Cabecalho TPKT: [0]=versao(3) [1]=reserved [2:4]=tamanho total BE.
			if len(buf) >= 4 && buf[0] == 0x03 {
				total := int(buf[2])<<8 | int(buf[3])
				if total >= 4 && len(buf) >= total {
					return buf[:total], nil
				}
			}
			continue
		}
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				return buf, nil
			}
			return nil, err
		}
	}
}

// extractCertChain devolve a cadeia DER apresentada pelo servidor RDP,
// com o leaf primeiro. Deduplica por fingerprint para evitar loop em
// certificados self-signed (onde IssuerCertificate aponta para si mesmo).
func extractCertChain(tlsConn *tls.Conn) ([][]byte, error) {
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(state.PeerCertificates))
	out := make([][]byte, 0, len(state.PeerCertificates))
	for _, cert := range state.PeerCertificates {
		if cert == nil {
			continue
		}
		fp := certFingerprint(cert)
		if _, dup := seen[fp]; dup {
			continue
		}
		seen[fp] = struct{}{}
		out = append(out, cert.Raw)
	}
	return out, nil
}

// certFingerprint identifica um certificado de forma estavel para deduplicacao.
func certFingerprint(cert *x509.Certificate) string {
	// RawSubject + RawIssuer + Serial identificam o certificado sem depender de hash externo.
	return string(cert.RawSubject) + "|" + string(cert.RawIssuer) + "|" + cert.SerialNumber.String()
}
