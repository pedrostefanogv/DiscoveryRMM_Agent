package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	debugsvc "discovery/app/debug"
)

func newP2PAPIContractTestApp(t *testing.T, serverURL, token string) *App {
	t.Helper()
	a := &App{ctx: context.Background()}
	a.debugSvc = debugsvc.NewService(debugsvc.Options{})
	a.debugSvc.ApplyRuntimeConnectionConfig("http", strings.TrimPrefix(serverURL, "http://"), token, "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7", "", "")
	return a
}

func TestPostP2PTelemetry_Accepts202(t *testing.T) {
	const token = "mdz_test_token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization invalido: %q", got)
		}
		if got := r.Header.Get("X-Agent-ID"); got != "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" {
			t.Fatalf("X-Agent-ID invalido: %q", got)
		}
		if r.URL.Path != p2pTelemetryEndpointPath {
			t.Fatalf("path inesperado: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"received":true}`))
	}))
	defer server.Close()

	a := newP2PAPIContractTestApp(t, server.URL, token)
	payload := P2PTelemetryPayload{CollectedAtUTC: time.Now().UTC().Format(time.RFC3339)}

	if err := a.postP2PTelemetryPayload(context.Background(), payload, ""); err != nil {
		t.Fatalf("esperava sucesso com 202, recebeu erro: %v", err)
	}
}

func TestPostP2PTelemetry_Handles429RetryAfter(t *testing.T) {
	const token = "mdz_test_token"
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Agent-ID"); got != "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" {
			t.Fatalf("X-Agent-ID invalido: %q", got)
		}
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limit de telemetria excedido","code":"RATE_LIMIT_EXCEEDED"}`))
	}))
	defer server.Close()

	a := newP2PAPIContractTestApp(t, server.URL, token)
	payload := P2PTelemetryPayload{CollectedAtUTC: time.Now().UTC().Format(time.RFC3339)}

	err := a.postP2PTelemetryPayload(context.Background(), payload, "")
	if err == nil {
		t.Fatalf("esperava erro HTTP 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("erro deveria mencionar 429, recebeu: %v", err)
	}

	err = a.postP2PTelemetryPayload(context.Background(), payload, "")
	if err == nil {
		t.Fatalf("esperava bloqueio local por rate limit")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "rate limit") {
		t.Fatalf("erro deveria mencionar rate limit local, recebeu: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("esperava apenas 1 request HTTP por causa do bloqueio local, recebeu %d", got)
	}
}

func TestPostP2PTelemetry_ParsesJSONErrorEnvelope(t *testing.T) {
	const token = "mdz_test_token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Agent-ID"); got != "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" {
			t.Fatalf("X-Agent-ID invalido: %q", got)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"payload invalido","field":"metrics","code":"METRIC_NEGATIVE"}`))
	}))
	defer server.Close()

	a := newP2PAPIContractTestApp(t, server.URL, token)
	payload := P2PTelemetryPayload{CollectedAtUTC: time.Now().UTC().Format(time.RFC3339)}

	err := a.postP2PTelemetryPayload(context.Background(), payload, "")
	if err == nil {
		t.Fatalf("esperava erro HTTP 400")
	}
	if !strings.Contains(err.Error(), "payload invalido") {
		t.Fatalf("erro deveria conter mensagem do envelope JSON, recebeu: %v", err)
	}
	if !strings.Contains(err.Error(), "METRIC_NEGATIVE") {
		t.Fatalf("erro deveria conter code do envelope JSON, recebeu: %v", err)
	}
}

func TestGetP2PDistributionStatusWithOptions_SendsQueryParams(t *testing.T) {
	const token = "mdz_test_token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization invalido: %q", got)
		}
		if got := r.Header.Get("X-Agent-ID"); got != "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" {
			t.Fatalf("X-Agent-ID invalido: %q", got)
		}
		if r.URL.Path != p2pDistributionStatusPath {
			t.Fatalf("path inesperado: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("artifactId"); got != "name:artifact.exe" {
			t.Fatalf("artifactId inesperado: %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "10" {
			t.Fatalf("limit inesperado: %q", got)
		}
		if got := r.URL.Query().Get("offset"); got != "20" {
			t.Fatalf("offset inesperado: %q", got)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	a := newP2PAPIContractTestApp(t, server.URL, token)
	_, err := a.GetP2PDistributionStatusWithOptions(context.Background(), P2PDistributionStatusQueryOptions{
		ArtifactID: "name:artifact.exe",
		Limit:      10,
		Offset:     20,
	})
	if err != nil {
		t.Fatalf("GetP2PDistributionStatusWithOptions erro: %v", err)
	}
}
