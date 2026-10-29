package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestOnboardingBackoffDelayBoundsAndJitter cobre o backoff exponencial com
// jitter do loop de onboarding (achados Z6/Z13): o primeiro retry precisa ser
// rápido, o atraso cresce de forma exponencial e nunca passa do teto.
func TestOnboardingBackoffDelayBoundsAndJitter(t *testing.T) {
	minDelay := onboardingRetryBase / 2

	// rnd=0 e rnd=1 delimitam as bordas do jitter.
	for attempt := 0; attempt <= 10; attempt++ {
		low := onboardingBackoffDelay(attempt, func() float64 { return 0 })
		high := onboardingBackoffDelay(attempt, func() float64 { return 1 })

		if low < minDelay {
			t.Fatalf("attempt=%d: atraso %s abaixo do mínimo %s", attempt, low, minDelay)
		}
		if high > onboardingRetryMax {
			t.Fatalf("attempt=%d: atraso %s acima do teto %s", attempt, high, onboardingRetryMax)
		}
		if low > high {
			t.Fatalf("attempt=%d: borda inferior %s > superior %s", attempt, low, high)
		}
	}

	// Primeiro retry é rápido (antes era um ticker fixo de 60s).
	if first := onboardingBackoffDelay(0, func() float64 { return 0.5 }); first > onboardingRetryBase {
		t.Fatalf("primeiro retry = %s, esperava <= %s", first, onboardingRetryBase)
	}

	// Crescimento monotônico até o teto (mesma semente de jitter).
	prev := time.Duration(0)
	for attempt := 0; attempt <= 8; attempt++ {
		d := onboardingBackoffDelay(attempt, func() float64 { return 0.5 })
		if d < prev {
			t.Fatalf("attempt=%d: atraso %s menor que o anterior %s", attempt, d, prev)
		}
		prev = d
	}
	// Com jitter, o atraso saturado fica em [teto/2, teto]; a borda superior
	// precisa bater exatamente no teto (não ultrapassar).
	if prev < onboardingRetryMax/2 {
		t.Fatalf("após várias tentativas esperava saturação no teto %s, obtive %s", onboardingRetryMax, prev)
	}
	if capped := onboardingBackoffDelay(8, func() float64 { return 1 }); capped != onboardingRetryMax {
		t.Fatalf("borda superior saturada = %s, esperava %s", capped, onboardingRetryMax)
	}

	// Jitter: com o mesmo attempt, sementes diferentes produzem atrasos diferentes.
	a := onboardingBackoffDelay(3, func() float64 { return 0.1 })
	b := onboardingBackoffDelay(3, func() float64 { return 0.9 })
	if a == b {
		t.Fatalf("esperava jitter diferente para sementes distintas, ambos = %s", a)
	}

	// rnd=nil não deve panicar (usa math/rand/v2).
	if d := onboardingBackoffDelay(2, nil); d <= 0 {
		t.Fatalf("onboardingBackoffDelay com rnd=nil retornou %s", d)
	}
}

// TestSummarizeOnboardingCauses garante que o erro agregado lista as causas por
// peer e trunca o excesso preservando a contagem (Z9).
func TestSummarizeOnboardingCauses(t *testing.T) {
	if got := summarizeOnboardingCauses(nil); got != "sem detalhes" {
		t.Fatalf("vazio: %q", got)
	}

	causes := []string{"libp2p: host indisponível", "http peer=a: connection refused"}
	got := summarizeOnboardingCauses(causes)
	for _, want := range causes {
		if !strings.Contains(got, want) {
			t.Fatalf("esperava %q em %q", want, got)
		}
	}

	many := make([]string, 0, onboardingMaxPeerCauses+3)
	for i := 0; i < onboardingMaxPeerCauses+3; i++ {
		many = append(many, "causa-"+strconv.Itoa(i))
	}
	truncated := summarizeOnboardingCauses(many)
	if !strings.Contains(truncated, "(+3 causas)") {
		t.Fatalf("esperava contagem de truncamento em %q", truncated)
	}
	if strings.Contains(truncated, "causa-"+strconv.Itoa(onboardingMaxPeerCauses+2)) {
		t.Fatalf("não deveria listar causas além do limite: %q", truncated)
	}
}

// TestDescribeOnboardingPeers cobre o diagnóstico de "preso" (endpoint por peer).
func TestDescribeOnboardingPeers(t *testing.T) {
	if got := describeOnboardingPeers(nil, 5); got != "nenhum" {
		t.Fatalf("nil: %q", got)
	}

	peers := []P2PPeerView{
		{AgentID: "peer-a", Address: "10.0.0.5", Port: 9911, ConnectedVia: "libp2p"},
		{AgentID: "peer-b", Address: "10.0.0.6", Port: 9912},
		{AgentID: "", Address: ""},
	}
	got := describeOnboardingPeers(peers, 2)
	if !strings.Contains(got, "peer-a@10.0.0.5:9911/libp2p") {
		t.Fatalf("esperava endpoint do peer-a em %q", got)
	}
	if !strings.Contains(got, "(+1 peers)") {
		t.Fatalf("esperava truncamento em %q", got)
	}

	if got := describeOnboardingPeers(peers[2:], 5); !strings.Contains(got, "sem-agent-id") || !strings.Contains(got, "sem-endereço") {
		t.Fatalf("esperava rótulos de peer incompleto em %q", got)
	}
}

// TestFetchOnboardingOfferHTTP cobre a exposição explícita das causas (antes
// ECONNREFUSED e status != 200 eram descartados em silêncio — Z9).
func TestFetchOnboardingOfferHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != p2pOnboardingEndpoint {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(P2POnboardingRequest{
			ServerURL:    "https://srv.local",
			DeployKey:    "deploy-key",
			ExpiresAtUTC: time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339),
			SourceAgent:  "peer-a",
			Nonce:        "nonce",
			Signature:    "assinatura-invalida",
		})
	}))
	defer server.Close()

	a := &App{}
	offer, err := a.fetchOnboardingOfferHTTP(context.Background(), server.URL+p2pOnboardingEndpoint)
	if err != nil {
		t.Fatalf("esperava oferta decodificada: %v", err)
	}
	if offer.SourceAgent != "peer-a" || offer.DeployKey != "deploy-key" {
		t.Fatalf("oferta inesperada: %+v", offer)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provisioning token unavailable", http.StatusServiceUnavailable)
	}))
	defer failing.Close()

	_, err = a.fetchOnboardingOfferHTTP(context.Background(), failing.URL+p2pOnboardingEndpoint)
	if err == nil {
		t.Fatal("esperava erro para HTTP 503")
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "provisioning token unavailable") {
		t.Fatalf("erro sem causa explícita (status+corpo): %v", err)
	}

	// Endereço sem listener: a causa deve citar a recusa (não silenciar).
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	if _, err := a.fetchOnboardingOfferHTTP(context.Background(), closedURL+p2pOnboardingEndpoint); err == nil {
		t.Fatal("esperava erro de conexão para endpoint fechado")
	}
}

// TestRequestOnboardingFromPeersAggregatesCauses garante que o erro final
// agrega a causa do caminho libp2p E a do caminho HTTP por peer (achados Z1/Z9).
func TestRequestOnboardingFromPeersAggregatesCauses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(P2POnboardingRequest{
			ServerURL:    "https://srv.local",
			DeployKey:    "deploy-key",
			ExpiresAtUTC: time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339),
			SourceAgent:  "peer-a",
			Nonce:        "nonce",
			Signature:    "assinatura-invalida",
		})
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
	a.P2PCoord = newP2PCoordinator(a)
	a.P2PCoord.UpsertPeer(p2pDiscoveredPeer{AgentID: "peer-a", Address: host, Port: port})

	state := &p2pOnboardingState{}
	onboardingErr := a.requestOnboardingFromPeers(context.Background(), state)
	if onboardingErr == nil {
		t.Fatal("esperava erro: nenhum peer entregou oferta válida")
	}

	msg := onboardingErr.Error()
	for _, want := range []string{"libp2p:", "http peer=peer-a", "p2p/config/onboard", "assinatura invalida"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("erro agregado sem %q: %s", want, msg)
		}
	}

	state.mu.Lock()
	lastErr := state.lastError
	auditLen := len(state.audit)
	state.mu.Unlock()
	if lastErr == "" {
		t.Fatal("esperava state.lastError preenchido para o diagnóstico de preso")
	}
	if auditLen == 0 {
		t.Fatal("esperava evento de auditoria da oferta rejeitada")
	}
}
