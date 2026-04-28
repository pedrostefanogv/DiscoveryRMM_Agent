package tlsutil

import (
	"net/http"
	"testing"
	"time"
)

func TestAllowInsecureTLS(t *testing.T) {
	SetConfigAllowInsecureTLS(false)
	t.Cleanup(func() {
		SetConfigAllowInsecureTLS(false)
	})

	t.Setenv(allowInsecureTLSEnv, "true")
	if !AllowInsecureTLS() {
		t.Fatal("AllowInsecureTLS deveria retornar true")
	}

	t.Setenv(allowInsecureTLSEnv, "0")
	if AllowInsecureTLS() {
		t.Fatal("AllowInsecureTLS deveria retornar false")
	}
}

func TestAllowInsecureTLS_ConfigFlag(t *testing.T) {
	SetConfigAllowInsecureTLS(false)
	t.Cleanup(func() {
		SetConfigAllowInsecureTLS(false)
	})

	t.Setenv(allowInsecureTLSEnv, "0")
	SetConfigAllowInsecureTLS(true)

	if !AllowInsecureTLS() {
		t.Fatal("AllowInsecureTLS deveria retornar true quando configurado via config")
	}
	if !ConfigAllowInsecureTLS() {
		t.Fatal("ConfigAllowInsecureTLS deveria retornar true")
	}
}

func TestNewHTTPClient_InsecureTLS(t *testing.T) {
	SetConfigAllowInsecureTLS(false)
	t.Cleanup(func() {
		SetConfigAllowInsecureTLS(false)
	})

	t.Setenv(allowInsecureTLSEnv, "1")

	client := NewHTTPClient(5 * time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatal("esperava transporte HTTP customizado")
	}
	if transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("esperava InsecureSkipVerify=true")
	}
	if client.Timeout != 5*time.Second {
		t.Fatalf("timeout = %s", client.Timeout)
	}
}

func TestNewWebSocketDialer_InsecureTLS(t *testing.T) {
	SetConfigAllowInsecureTLS(false)
	t.Cleanup(func() {
		SetConfigAllowInsecureTLS(false)
	})

	t.Setenv(allowInsecureTLSEnv, "on")

	dialer := NewWebSocketDialer(7 * time.Second)
	if dialer.TLSClientConfig == nil || !dialer.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("esperava TLSClientConfig com InsecureSkipVerify=true")
	}
	if dialer.HandshakeTimeout != 7*time.Second {
		t.Fatalf("HandshakeTimeout = %s", dialer.HandshakeTimeout)
	}
}
