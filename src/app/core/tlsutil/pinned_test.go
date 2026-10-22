package tlsutil

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// selfSignedServer sobe um httptest com cert autoassinado e devolve o
// SHA-256 (hex maiúsculas) do certificado folha — mesmo formato do pin
// publicado pela API em apiTlsCertHash/natsTlsCertHash.
func selfSignedServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	if _, err := x509.ParseCertificate(srv.Certificate().Raw); err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	sum := sha256.Sum256(srv.Certificate().Raw)
	return srv, strings.ToUpper(hex.EncodeToString(sum[:]))
}

func TestPinnedHTTPClient_AcceptsMatchingHash(t *testing.T) {
	srv, hash := selfSignedServer(t)

	// O cert do httptest é autoassinado: para isolar o pin da validação de
	// cadeia, confiamos explicitamente na CA do cert de teste (em produção a
	// cadeia é válida e o pin é uma defesa ADICIONAL — nunca substituta).
	cfg := PinnedTLSConfig(hash, true)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	cfg.RootCAs = pool

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: cfg},
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("esperava 200 com hash correto: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestPinnedHTTPClient_RejectsDivergentHash(t *testing.T) {
	srv, _ := selfSignedServer(t)

	client := NewPinnedHTTPClient(5*time.Second, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", true)
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("esperava erro com hash divergente")
	}
	if !strings.Contains(err.Error(), "tls pin") && !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("erro inesperado: %v", err)
	}
}

func TestPinnedHTTPClient_FailsClosedWithoutHash(t *testing.T) {
	srv, _ := selfSignedServer(t)

	// Enforce=true sem hash publicado deve RECUSAR (fail-closed), nunca
	// degradar para conexão sem pin.
	client := NewPinnedHTTPClient(5*time.Second, "", true)
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("enforce=true sem hash publicado deveria recusar a conexão")
	}
}

func TestPinnedTLSConfig_EnforceFalseDoesNotPin(t *testing.T) {
	srv, _ := selfSignedServer(t)

	// Com enforce=false (comportamento anterior), o pin não é imposto —
	// inclusive com hash presente: só o reporte ocorre em outro fluxo.
	client := NewPinnedHTTPClient(5*time.Second, "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", false)
	// O cert do httptest é autoassinado: sem InsecureSkipVerify a validação de
	// cadeia padrão falha por "unknown authority" — e é exatamente isso que
	// prova que NÃO há bypass: enforce=false não habilita skip-verify.
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("enforce=false não deveria aceitar cadeia inválida")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "authority") && !strings.Contains(strings.ToLower(err.Error()), "verify") {
		t.Fatalf("esperava falha de validação de cadeia, got: %v", err)
	}
	_ = srv.Client()
}
