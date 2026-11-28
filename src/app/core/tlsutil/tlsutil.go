package tlsutil

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const allowInsecureTLSEnv = "DISCOVERY_ALLOW_INSECURE_TLS"

var configAllowInsecureTLS atomic.Bool

func SetConfigAllowInsecureTLS(allow bool) {
	configAllowInsecureTLS.Store(allow)
}

func ConfigAllowInsecureTLS() bool {
	return configAllowInsecureTLS.Load()
}

// AllowInsecureTLS habilita um bypass explicito de validacao TLS apenas para laboratorio.
func AllowInsecureTLS() bool {
	if ConfigAllowInsecureTLS() {
		return true
	}

	return allowInsecureTLSFromEnv()
}

func allowInsecureTLSFromEnv() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(allowInsecureTLSEnv)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// InsecureTLSConfig retorna uma configuracao TLS minima para ambiente de laboratorio.
func InsecureTLSConfig() *tls.Config {
	if !AllowInsecureTLS() {
		return nil
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
	}
}

func NewHTTPClient(timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	tlsCfg := InsecureTLSConfig()
	if tlsCfg == nil {
		return client
	}

	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
		clone := transport.Clone()
		clone.TLSClientConfig = tlsCfg
		client.Transport = clone
		return client
	}

	client.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	return client
}

func NewWebSocketDialer(handshakeTimeout time.Duration) websocket.Dialer {
	dialer := websocket.Dialer{HandshakeTimeout: handshakeTimeout}
	if tlsCfg := InsecureTLSConfig(); tlsCfg != nil {
		dialer.TLSClientConfig = tlsCfg
	}
	return dialer
}

// PinnedTLSConfig devolve uma configuracao TLS que, alem da verificacao padrao
// de cadeia, compara o SHA-256 do certificado folha com o hash pinado na
// configuracao do servidor (C2 - auditoria 2026-10-21).
//
// Semantica fail-closed, alinhada com agentconn.evaluateTLSPinPolicy:
//   - enforce=false            -> config sem pin (comportamento anterior: so reporta);
//   - enforce=true, hash vazio -> RECUSA qualquer conexao (operador pediu enforce
//     sem publicar hash: erro de configuracao nao abre mao silenciosamente do pin);
//   - enforce=true, hash presente -> hash divergente recusa o handshake.
//
// O pin e defesa ADICIONAL: a validacao de cadeia padrao permanece ativa
// (InsecureSkipVerify so via AllowInsecureTLS, ambiente de lab).
func PinnedTLSConfig(expectedHash string, enforce bool) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if insecure := InsecureTLSConfig(); insecure != nil {
		cfg.InsecureSkipVerify = true
	}

	expectedHash = normalizePinHash(expectedHash)
	if !enforce {
		return cfg
	}

	cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("tls pin: certificado do servidor ausente")
		}
		if expectedHash == "" {
			// Enforce sem hash publicado: recusa. Nao reusa evaluateTLSPinPolicy
			// para evitar importar agentconn em tlsutil (ciclo de pacote).
			return fmt.Errorf("tls pin: enforce=true sem apiTlsCertHash/natsTlsCertHash publicado pelo servidor")
		}
		sum := sha256.Sum256(rawCerts[0])
		observed := strings.ToUpper(hex.EncodeToString(sum[:]))
		if observed != expectedHash {
			return fmt.Errorf("tls pin: hash do certificado divergente (observado=%s esperado=%s)", observed, expectedHash)
		}
		return nil
	}
	return cfg
}

// normalizePinHash remove separadores e uniformiza maiusculas (mesma regra
// usada pelo agentconn para os hashes vindos da configuracao do servidor).
func normalizePinHash(raw string) string {
	h := strings.ToUpper(strings.TrimSpace(raw))
	h = strings.ReplaceAll(h, ":", "")
	h = strings.ReplaceAll(h, " ", "")
	return h
}

// NewPinnedHTTPClient e o NewHTTPClient com pin de certificado (C2).
// enforce=false equivale a NewHTTPClient.
func NewPinnedHTTPClient(timeout time.Duration, expectedHash string, enforce bool) *http.Client {
	tlsCfg := PinnedTLSConfig(expectedHash, enforce)
	if tlsCfg == nil {
		return NewHTTPClient(timeout)
	}

	client := &http.Client{Timeout: timeout}
	if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
		clone := transport.Clone()
		clone.TLSClientConfig = tlsCfg
		client.Transport = clone
		return client
	}
	client.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	return client
}
