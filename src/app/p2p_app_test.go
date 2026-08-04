package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"discovery/app/core/envutil"
)

// Testes do App que dependem de funções do package app (onboarding,
// zero-touch, temp dir) e dos bridges P2P.

func TestResolveP2PTempDir(t *testing.T) {
	windowsPath := resolveP2PTempDir("windows")
	wantWindows := filepath.Join("C:\\", "Windows", "Temp", "Discovery", "P2P_Temp")
	if !strings.EqualFold(filepath.Clean(windowsPath), filepath.Clean(wantWindows)) {
		t.Fatalf("windows path = %q, want %q", windowsPath, wantWindows)
	}

	linuxPath := resolveP2PTempDir("linux")
	wantLinux := filepath.Join(GetDataDir(), "TempP2P")
	if linuxPath != wantLinux {
		t.Fatalf("linux path = %q, want %q", linuxPath, wantLinux)
	}
}

func TestClearAllP2PArtifacts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("PROGRAMDATA", root)
	t.Setenv("WINDIR", root)
	// envutil cacheia variáveis via sync.Once — resetar para que t.Setenv
	// seja efetivo neste teste.
	envutil.Reset()

	a := &App{}
	dir := a.p2pTempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "artifact-a.bin"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write file failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "artifact-b.bin"), []byte("b"), 0o600); err != nil {
		t.Fatalf("write nested file failed: %v", err)
	}

	msg, err := a.ClearAllP2PArtifacts()
	if err != nil {
		t.Fatalf("ClearAllP2PArtifacts() returned error: %v", err)
	}
	if !strings.Contains(msg, "limpeza total concluida") {
		t.Fatalf("unexpected message: %q", msg)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty temp dir after clear-all, got %d entries", len(entries))
	}
}

func TestApplyOnboardingOfferExpired(t *testing.T) {
	offer := P2POnboardingRequest{
		ServerURL:    "https://srv",
		DeployKey:    "key",
		ExpiresAtUTC: time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339),
		SourceAgent:  "agent-src",
		Nonce:        "abc",
		Signature:    "irrelevant",
	}
	a := &App{}
	_, err := a.applyOnboardingOffer(offer)
	if err == nil {
		t.Fatal("expected error for expired offer")
	}
}

func TestApplyOnboardingOfferBadSignature(t *testing.T) {
	expiresAt := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339)
	offer := P2POnboardingRequest{
		ServerURL:    "https://srv",
		DeployKey:    "key",
		ExpiresAtUTC: expiresAt,
		SourceAgent:  "agent-src",
		Nonce:        "abc",
		Signature:    "badsig",
	}
	a := &App{}
	_, err := a.applyOnboardingOffer(offer)
	if err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

func TestParseZeroTouchRegisterResponseSupportsNestedSnakeCase(t *testing.T) {
	body := []byte(`{"result":{"auth_token":"token-1","agent_id":"agent-1"}}`)

	credentials, err := parseZeroTouchRegisterResponse(body, "https://tngplacas.com.br")
	if err != nil {
		t.Fatalf("parseZeroTouchRegisterResponse() error = %v", err)
	}
	if credentials.AuthToken != "token-1" {
		t.Fatalf("AuthToken = %q", credentials.AuthToken)
	}
	if credentials.AgentID != "agent-1" {
		t.Fatalf("AgentID = %q", credentials.AgentID)
	}
	if credentials.ApiScheme != "https" {
		t.Fatalf("ApiScheme = %q", credentials.ApiScheme)
	}
	if credentials.ApiServer != "tngplacas.com.br" {
		t.Fatalf("ApiServer = %q", credentials.ApiServer)
	}
}

func TestParseZeroTouchRegisterResponseUsesServerURLFromPayload(t *testing.T) {
	body := []byte(`{"authToken":"token-2","agentId":"agent-2","serverUrl":"https://srv.example/api/"}`)

	credentials, err := parseZeroTouchRegisterResponse(body, "")
	if err != nil {
		t.Fatalf("parseZeroTouchRegisterResponse() error = %v", err)
	}
	if credentials.ApiScheme != "https" {
		t.Fatalf("ApiScheme = %q", credentials.ApiScheme)
	}
	if credentials.ApiServer != "srv.example" {
		t.Fatalf("ApiServer = %q", credentials.ApiServer)
	}
}

func TestRequestOnboardingFromPeersNilStateDoesNotPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offer := P2POnboardingRequest{
			ServerURL:    "https://srv.local",
			DeployKey:    "deploy-key",
			ExpiresAtUTC: time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339),
			SourceAgent:  "peer-a",
			Nonce:        "nonce",
			Signature:    "invalid-signature",
		}
		_ = json.NewEncoder(w).Encode(offer)
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url parse: %v", err)
	}
	host, portRaw, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatalf("atoi port: %v", err)
	}

	a := &App{}
	a.p2pCoord = newP2PCoordinator(a)
	a.p2pCoord.UpsertPeer(p2pDiscoveredPeer{AgentID: "peer-a", Address: host, Port: port})

	panicObserved := false
	func() {
		defer func() {
			if recover() != nil {
				panicObserved = true
			}
		}()
		err = a.requestOnboardingFromPeers(context.Background(), nil)
	}()

	if panicObserved {
		t.Fatal("requestOnboardingFromPeers(nil) nao deveria panicar")
	}
	if err == nil {
		t.Fatal("esperava erro com oferta invalida")
	}
}

func TestComputeOnboardingSignatureConsistent(t *testing.T) {
	sig1 := computeOnboardingSignature("agent-a", "https://server.local", "key123", "2026-01-01T00:00:00Z", "nonce1")
	sig2 := computeOnboardingSignature("agent-a", "https://server.local", "key123", "2026-01-01T00:00:00Z", "nonce1")
	if sig1 != sig2 {
		t.Fatal("signature must be deterministic")
	}
	// Different nonce → different signature (replay prevention).
	sig3 := computeOnboardingSignature("agent-a", "https://server.local", "key123", "2026-01-01T00:00:00Z", "nonce2")
	if sig1 == sig3 {
		t.Fatal("different nonce must produce different signature")
	}
}

func TestBuildOnboardingOfferExpiry(t *testing.T) {
	offer, err := BuildOnboardingOffer("agent-src", "https://srv", "key", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := time.Parse(time.RFC3339, offer.ExpiresAtUTC)
	if err != nil {
		t.Fatalf("invalid expiresAt: %v", err)
	}
	if time.Until(exp) < 4*time.Minute || time.Until(exp) > 6*time.Minute {
		t.Fatalf("expiry out of expected range: %s", offer.ExpiresAtUTC)
	}
	// Verify offer self-validates.
	expected := computeOnboardingSignature(offer.SourceAgent, offer.ServerURL, offer.DeployKey, offer.ExpiresAtUTC, offer.Nonce)
	if offer.Signature != expected {
		t.Fatal("offer signature mismatch")
	}
}
