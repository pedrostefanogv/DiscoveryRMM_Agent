package agentconn

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"discovery/internal/tlsutil"
)

func normalizeNATSURL(server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", fmt.Errorf("servidor NATS vazio")
	}
	if strings.Contains(server, "://") {
		u, err := url.Parse(server)
		if err != nil {
			return "", fmt.Errorf("url NATS invalida: %w", err)
		}
		scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
		switch scheme {
		case "nats", "ws", "wss":
			if strings.TrimSpace(u.Host) == "" {
				return "", fmt.Errorf("url NATS invalida: host ausente")
			}
			// Garante porta explicita para evitar dial :0 quando o host nao tem porta.
			ensureDefaultPort(u, scheme)
			return u.String(), nil
		default:
			return "", fmt.Errorf("url NATS invalida: scheme %s nao suportado", scheme)
		}
	}
	return "nats://" + server, nil
}

// ensureDefaultPort adiciona a porta padrao ao Host da URL quando ausente.
func ensureDefaultPort(u *url.URL, scheme string) {
	if strings.Contains(u.Host, ":") {
		return
	}
	switch strings.ToLower(scheme) {
	case "nats":
		u.Host += ":4222"
	case "wss":
		u.Host += ":443"
	case "ws":
		u.Host += ":80"
	}
}

func normalizeTLSCertHash(raw string) string {
	h := strings.ToUpper(strings.TrimSpace(raw))
	h = strings.ReplaceAll(h, ":", "")
	h = strings.ReplaceAll(h, " ", "")
	return h
}

func evaluateTLSPinPolicy(transport, observedHash, expectedHash string, enforce bool) error {
	transport = strings.TrimSpace(strings.ToLower(transport))
	observedHash = normalizeTLSCertHash(observedHash)
	expectedHash = normalizeTLSCertHash(expectedHash)

	if !enforce {
		return nil
	}
	if expectedHash == "" {
		return fmt.Errorf("politica de seguranca bloqueou %s: hash TLS esperado ausente com enforce=true", transport)
	}
	if observedHash == "" {
		return fmt.Errorf("politica de seguranca bloqueou %s: hash TLS observado ausente", transport)
	}
	if observedHash != expectedHash {
		return fmt.Errorf("politica de seguranca bloqueou %s: hash TLS divergente (observado=%s esperado=%s)", transport, observedHash, expectedHash)
	}
	return nil
}

func rewriteNATSHost(server, newHost string) (string, error) {
	server = strings.TrimSpace(server)
	newHost = strings.TrimSpace(newHost)
	if server == "" || newHost == "" {
		return server, nil
	}

	natsURL, err := normalizeNATSURL(server)
	if err != nil {
		return "", err
	}

	u, err := url.Parse(natsURL)
	if err != nil {
		return "", err
	}

	port := ""
	if _, p, splitErr := net.SplitHostPort(strings.TrimSpace(u.Host)); splitErr == nil {
		port = p
	}

	hostOnly := newHost
	if strings.Contains(newHost, ":") {
		if h, p, splitErr := net.SplitHostPort(newHost); splitErr == nil {
			hostOnly = strings.TrimSpace(h)
			if strings.TrimSpace(p) != "" {
				port = strings.TrimSpace(p)
			}
		}
	}
	hostOnly = strings.TrimSpace(strings.Trim(hostOnly, "[]"))
	if hostOnly == "" {
		return "", fmt.Errorf("natsServerHost invalido")
	}

	if strings.TrimSpace(port) != "" {
		u.Host = net.JoinHostPort(hostOnly, port)
	} else {
		u.Host = hostOnly
	}

	return u.String(), nil
}

func buildExternalNATSWSSURL(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("natsServerHost vazio")
	}
	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err != nil {
			return "", err
		}
		host = strings.TrimSpace(u.Host)
	}
	host = strings.Trim(strings.TrimSpace(host), "/")
	if host == "" {
		return "", fmt.Errorf("natsServerHost invalido")
	}
	// Garante porta 443 explicita para evitar dial :0.
	if !strings.Contains(host, ":") {
		host += ":443"
	}
	return "wss://" + host + "/nats/", nil
}

func observeTLSPeerCertHash(ctx context.Context, apiServer string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = handshakeTimeout
	}

	address, serverName, err := normalizeTLSTarget(apiServer)
	if err != nil {
		return "", err
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
	}
	if tlsutil.AllowInsecureTLS() {
		tlsConfig.InsecureSkipVerify = true
	}

	tlsDialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: timeout},
		Config:    tlsConfig,
	}

	conn, err := tlsDialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return "", fmt.Errorf("conexao TLS invalida para captura de certificado")
	}

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", fmt.Errorf("certificado TLS remoto ausente")
	}

	sum := sha256.Sum256(state.PeerCertificates[0].Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:])), nil
}

func normalizeTLSTarget(apiServer string) (string, string, error) {
	target := strings.TrimSpace(apiServer)
	if target == "" {
		return "", "", fmt.Errorf("apiServer vazio")
	}

	if strings.Contains(target, "://") {
		u, err := url.Parse(target)
		if err != nil {
			return "", "", fmt.Errorf("apiServer invalido: %w", err)
		}
		target = strings.TrimSpace(u.Host)
	}

	host := target
	port := "443"
	if h, p, err := net.SplitHostPort(target); err == nil {
		host = strings.TrimSpace(h)
		port = strings.TrimSpace(p)
	} else {
		target = strings.Trim(target, "[]")
		if target == "" {
			return "", "", fmt.Errorf("host TLS vazio")
		}
		host = target
	}

	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		return "", "", fmt.Errorf("host TLS vazio")
	}
	if port == "" {
		port = "443"
	}

	return net.JoinHostPort(host, port), host, nil
}

// validateTransportSecurity enforces secure transport in non-local endpoints.
// A local dev override exists via DISCOVERY_ALLOW_INSECURE_TRANSPORT.
func validateTransportSecurity(cfg Config) error {
	if allowInsecureTransport() {
		return nil
	}

	if cfg.ApiServer != "" && !isLocalTarget(cfg.ApiServer) && strings.TrimSpace(strings.ToLower(cfg.ApiScheme)) != "https" {
		return fmt.Errorf("apiScheme deve ser https para endpoints remotos")
	}

	return nil
}

func allowInsecureTransport() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("DISCOVERY_ALLOW_INSECURE_TRANSPORT")))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isLocalTarget(value string) bool {
	host := strings.TrimSpace(value)
	if host == "" {
		return false
	}

	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err == nil {
			host = strings.TrimSpace(u.Host)
		}
	}

	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else {
		if strings.HasPrefix(host, "[") {
			if idx := strings.Index(host, "]"); idx > 1 {
				host = host[1:idx]
			}
		} else if strings.Count(host, ":") == 1 {
			host = strings.SplitN(host, ":", 2)[0]
		}
	}

	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return false
	}

	if strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1" {
		return true
	}

	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() {
			return true
		}
		if v4 := ip.To4(); v4 != nil && v4[0] == 169 && v4[1] == 254 {
			return true
		}
		if strings.HasPrefix(ip.String(), "fe80:") {
			return true
		}
		return false
	}

	lower := strings.ToLower(host)
	if strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".localhost") {
		return true
	}

	return false
}
