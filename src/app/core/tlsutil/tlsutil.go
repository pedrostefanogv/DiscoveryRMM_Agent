package tlsutil

import (
	"crypto/tls"
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
