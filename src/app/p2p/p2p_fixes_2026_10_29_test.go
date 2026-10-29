package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	debug "discovery/app/debug"
	"discovery/app/p2pmeta"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// newTestLibP2PHost sobe um host libp2p efêmero em loopback (apenas para teste).
func newTestLibP2PHost(t *testing.T) (host.Host, func()) {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p host: %v", err)
	}
	return h, func() { _ = h.Close() }
}

// TestLibP2PHandshakeDeliversPeer é a regressão do achado D1 (CRÍTICO): o
// cancel() era chamado ANTES do NewStream, o stream de handshake herdava um
// contexto já cancelado e o peer NUNCA era entregue ao coordinator. Com o bug,
// mDNS descobria e conectava mas a malha ficava vazia.
func TestLibP2PHandshakeDeliversPeer(t *testing.T) {
	h1, close1 := newTestLibP2PHost(t)
	defer close1()
	h2, close2 := newTestLibP2PHost(t)
	defer close2()

	self1 := p2pSelfEndpoint{AgentID: "agent-1", Host: "host-1", Port: 41080, ClientID: "client-A"}
	registry1 := newLibp2pPeerRegistry()
	h1.SetStreamHandler(p2pLibP2PProtocolID, func(s network.Stream) {
		handleLibP2PHandshakeStream(s, self1, "client-A", registry1, nil, func(p2pDiscoveredPeer) {}, nil)
	})

	got := make(chan p2pDiscoveredPeer, 1)
	notifee := &libp2pMDNSNotifee{
		h:        h2,
		self:     p2pSelfEndpoint{AgentID: "agent-2", Host: "host-2", Port: 41081, ClientID: "client-A"},
		onPeer:   func(p p2pDiscoveredPeer) { got <- p },
		onTrace:  func(msg string) { t.Log(msg) },
		registry: newLibp2pPeerRegistry(),
		clientID: "client-A",
	}
	notifee.exchangeWithPeer(peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()})

	select {
	case p := <-got:
		if p.AgentID != "agent-1" {
			t.Fatalf("agentId = %q, want agent-1", p.AgentID)
		}
		if p.Port != 41080 {
			t.Fatalf("port = %d, want 41080", p.Port)
		}
		if p.Address != "127.0.0.1" {
			t.Fatalf("address = %q, want 127.0.0.1", p.Address)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("peer não foi entregue: regressão do cancel prematuro (D1)")
	}

	// O registry do lado que descobre precisa mapear agentId → peer.ID para que
	// gossip/transfer abram streams depois do handshake.
	if id, ok := notifee.registry.Lookup("agent-1"); !ok || id != h1.ID() {
		t.Fatalf("registry não mapeou agent-1 → %s (ok=%v, got=%s)", h1.ID(), ok, id)
	}
}

// TestLibP2PHandshakeAcceptsUnprovisionedPeer garante que um agente sem
// clientId (ainda não provisionado) NÃO é rejeitado — é o caso do zero-touch
// (achado F7): o peer precisa existir na malha para receber a config.
func TestLibP2PHandshakeAcceptsUnprovisionedPeer(t *testing.T) {
	h1, close1 := newTestLibP2PHost(t)
	defer close1()
	h2, close2 := newTestLibP2PHost(t)
	defer close2()

	h1.SetStreamHandler(p2pLibP2PProtocolID, func(s network.Stream) {
		handleLibP2PHandshakeStream(s,
			p2pSelfEndpoint{AgentID: "agent-provisioned", Host: "host-1", Port: 41080},
			"client-A", newLibp2pPeerRegistry(), nil, func(p2pDiscoveredPeer) {}, nil)
	})

	got := make(chan p2pDiscoveredPeer, 1)
	notifee := &libp2pMDNSNotifee{
		h:        h2,
		self:     p2pSelfEndpoint{AgentID: "agent-new", Host: "host-2", Port: 41081},
		onPeer:   func(p p2pDiscoveredPeer) { got <- p },
		onTrace:  func(msg string) { t.Log(msg) },
		registry: newLibp2pPeerRegistry(),
		clientID: "client-A",
	}
	notifee.exchangeWithPeer(peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()})

	select {
	case p := <-got:
		if p.AgentID != "agent-provisioned" {
			t.Fatalf("agentId = %q", p.AgentID)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("agente sem clientId deveria ser aceito (zero-touch)")
	}
}

// TestLANHealthServerServesHealth cobre o achado D2: o listener principal do
// TransferServer é loopback-only (hardening C4), então o probe de LAN nunca
// alcançava o /p2p/health do peer. O servidor de health de LAN resolve isso
// sem expor as rotas sensíveis.
func TestLANHealthServerServesHealth(t *testing.T) {
	deps := &mockDeps{cfg: DefaultConfig(), tempDir: t.TempDir(), dataDir: t.TempDir()}
	srv := NewTransferServer(deps, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx, DefaultConfig(), "agent-health-1", t.TempDir(), func() []P2PPeerView { return nil }); err != nil {
		t.Fatalf("start transfer server: %v", err)
	}
	defer srv.Close()

	coord := &Coordinator{deps: deps, transferServer: srv}
	ls, err := coord.startLANHealthServerOn([]string{"127.0.0.1"}, 0)
	if err != nil {
		t.Fatalf("startLANHealthServerOn: %v", err)
	}
	if ls == nil || len(ls.urls) != 1 {
		t.Fatalf("health server sem listener: %+v", ls)
	}
	defer ls.Close()

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(ls.urls[0])
	if err != nil {
		t.Fatalf("GET %s: %v", ls.urls[0], err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var health p2pHealthResponse
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("decode health: %v (body=%s)", err, string(body))
	}
	if !health.OK {
		t.Fatalf("health.ok = false (body=%s)", string(body))
	}
	if health.AgentID != "agent-health-1" {
		t.Fatalf("agentId = %q, want agent-health-1", health.AgentID)
	}

	// Rota sensível NÃO pode existir neste listener. A asserção precisa ser
	// positiva (responder 404/405), senão uma conexão recusada passaria como
	// "ok" e o teste não provaria nada (achado da revisão 2026-10-29).
	blocked, err := (&http.Client{Timeout: 5 * time.Second}).Get(strings.TrimSuffix(ls.urls[0], p2pLANHealthPath) + "/p2p/config/onboard")
	if err != nil {
		t.Fatalf("GET /p2p/config/onboard no listener de LAN: %v (esperava 404)", err)
	}
	defer blocked.Body.Close()
	if blocked.StatusCode != http.StatusNotFound && blocked.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("/p2p/config/onboard no listener de LAN retornou %d; só /p2p/health deve existir", blocked.StatusCode)
	}
}

// TestPeerTTLFloorKeepsPeer cobre o achado D5: TTL de 2 min < intervalo do
// probe de 5 min fazia o peer expirar antes de ser redescoberto ("pisca").
func TestPeerTTLFloorKeepsPeer(t *testing.T) {
	deps := &mockDeps{cfg: DefaultConfig(), tempDir: t.TempDir(), dataDir: t.TempDir()}
	c := NewCoordinator(deps)
	peer := p2pDiscoveredPeer{AgentID: "agent-old", Address: "192.168.10.9", Port: 41080, Source: "lan-probe"}

	c.mu.Lock()
	c.peers["agent-old"] = p2pPeerState{Peer: peer, LastSeenUTC: time.Now().Add(-3 * time.Minute)}
	c.mu.Unlock()

	if err := c.discoveryTick(time.Now()); err != nil {
		t.Fatalf("discoveryTick: %v", err)
	}
	if _, ok := c.peers["agent-old"]; !ok {
		t.Fatal("peer descoberto por probe expirou antes do próximo sweep (regressão D5)")
	}

	c.mu.Lock()
	c.peers["agent-old"] = p2pPeerState{Peer: peer, LastSeenUTC: time.Now().Add(-11 * time.Minute)}
	c.mu.Unlock()
	if err := c.discoveryTick(time.Now()); err != nil {
		t.Fatalf("discoveryTick: %v", err)
	}
	if _, ok := c.peers["agent-old"]; ok {
		t.Fatal("peer realmente inativo (11 min) deveria ser expirado")
	}
}

// TestP2PLibP2PFixedPort cobre o achado D9 (porta efêmera do libp2p).
func TestP2PLibP2PFixedPort(t *testing.T) {
	t.Setenv("DISCOVERY_P2P_LIBP2P_PORT", "")
	if got := p2pLibP2PFixedPort(); got != 41121 {
		t.Fatalf("default = %d, want 41121", got)
	}
	t.Setenv("DISCOVERY_P2P_LIBP2P_PORT", "45000")
	if got := p2pLibP2PFixedPort(); got != 45000 {
		t.Fatalf("override = %d, want 45000", got)
	}
	t.Setenv("DISCOVERY_P2P_LIBP2P_PORT", "abc")
	if got := p2pLibP2PFixedPort(); got != 41121 {
		t.Fatalf("inválido deveria cair no default, got %d", got)
	}
	t.Setenv("DISCOVERY_P2P_LIBP2P_PORT", "0")
	if got := p2pLibP2PFixedPort(); got != 0 {
		t.Fatalf("0 deveria forçar efêmera, got %d", got)
	}
}

// onboardingTestDeps é um AppDeps configurado, capaz de emitir oferta.
type onboardingTestDeps struct {
	*mockDeps
	// expiresAt permite forçar o cenário de expiresAt vazio/no passado
	// (regressão do TTL da oferta). Vazio = +10 min.
	expiresAt string
}

func (d *onboardingTestDeps) IsAgentConfigured() bool { return true }

func (d *onboardingTestDeps) RequestProvisioningToken(ctx context.Context) (string, string, error) {
	exp := d.expiresAt
	if exp == "" {
		exp = time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	}
	return "zt-deploy-key", exp, nil
}

func (d *onboardingTestDeps) LoadInstallerConfig() (debug.InstallerConfig, string, error) {
	return debug.InstallerConfig{ApiServer: "api.example.test"}, "", nil
}

func (d *onboardingTestDeps) BuildOnboardingOffer(sourceAgentID, serverURL, deployKey string, ttl time.Duration) (p2pmeta.OnboardingRequest, error) {
	return p2pmeta.OnboardingRequest{
		ServerURL:    serverURL,
		DeployKey:    deployKey,
		SourceAgent:  sourceAgentID,
		ExpiresAtUTC: time.Now().Add(ttl).UTC().Format(time.RFC3339),
		Nonce:        "nonce",
		Signature:    "sig",
	}, nil
}

// TestOnboardingOfferStreamOverLibP2P cobre Z1: a oferta de zero-touch precisa
// viajar pelo libp2p porque o HTTP de onboarding é loopback-only.
func TestOnboardingOfferStreamOverLibP2P(t *testing.T) {
	deps := &onboardingTestDeps{mockDeps: &mockDeps{cfg: DefaultConfig(), tempDir: t.TempDir(), dataDir: t.TempDir()}}
	transfer := &TransferServer{deps: deps, agentID: "agent-provisioner"}

	h1, close1 := newTestLibP2PHost(t)
	defer close1()
	h2, close2 := newTestLibP2PHost(t)
	defer close2()

	RegisterOnboardingProtocols(h1, transfer)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := h2.Connect(ctx, peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	offer, err := libp2pFetchOnboardingOffer(ctx, h2, h1.ID(), "agent-new", "")
	if err != nil {
		t.Fatalf("libp2pFetchOnboardingOffer: %v", err)
	}
	if offer.DeployKey != "zt-deploy-key" {
		t.Fatalf("deployKey = %q, want zt-deploy-key", offer.DeployKey)
	}
	if offer.ServerURL != "https://api.example.test" && offer.ServerURL != "http://api.example.test" {
		t.Fatalf("serverURL = %q", offer.ServerURL)
	}
	if offer.SourceAgent != "agent-provisioner" {
		t.Fatalf("sourceAgent = %q", offer.SourceAgent)
	}

	// O canal de onboarding tem cooldown por peer (cada oferta consome um token
	// de uso único na API); zera o estado para exercitar o segundo caminho.
	p2pOnboardOfferMu.Lock()
	p2pOnboardOfferLastSeen = make(map[peer.ID]time.Time)
	p2pOnboardOfferMu.Unlock()

	// FetchOnboardingOfferFromPeers: caminho usado pelo agente não configurado.
	registry := newLibp2pPeerRegistry()
	registry.RegisterStrict("agent-provisioner", h1.ID())
	coord := &Coordinator{
		deps:              deps,
		peers:             make(map[string]p2pPeerState),
		peerArtifacts:     make(map[string]p2pPeerArtifactState),
		peerLastAttempt:   make(map[string]time.Time),
		sha256Cache:       make(map[string]artifactSHA256CacheEntry),
		manifestHealth:    make(map[string]manifestHealthEntry),
		servingSessions:   make(map[string]*servingSession),
		downloadLocks:     make(map[string]*downloadLockEntry),
		discoveryProvider: &p2pLibP2PProvider{h: h2, registry: registry},
	}
	coord.peers["agent-provisioner"] = p2pPeerState{
		Peer:        p2pDiscoveredPeer{AgentID: "agent-provisioner", Address: "127.0.0.1", Port: 41080},
		LastSeenUTC: time.Now(),
	}
	fromPeers, err := coord.FetchOnboardingOfferFromPeers(ctx)
	if err != nil {
		t.Fatalf("FetchOnboardingOfferFromPeers: %v", err)
	}
	if fromPeers.DeployKey != "zt-deploy-key" {
		t.Fatalf("deployKey = %q", fromPeers.DeployKey)
	}
}

// TestOnboardingOfferRateLimit garante que o mesmo peer não consome um
// provisioning token (uso único) a cada request — 1 oferta por cooldown.
func TestOnboardingOfferRateLimit(t *testing.T) {
	now := time.Now()
	pid := peer.ID("peer-x")
	resetOnboardOfferState := func() {
		p2pOnboardOfferMu.Lock()
		p2pOnboardOfferLastSeen = make(map[peer.ID]time.Time)
		p2pOnboardOfferMu.Unlock()
	}
	resetOnboardOfferState()
	t.Cleanup(resetOnboardOfferState)

	if !allowOnboardingOffer(pid, now) {
		t.Fatal("primeira oferta deveria ser permitida")
	}
	if allowOnboardingOffer(pid, now.Add(5*time.Second)) {
		t.Fatal("segunda oferta dentro do cooldown deveria ser recusada")
	}
	if !allowOnboardingOffer(pid, now.Add(p2pOnboardOfferCooldown+time.Second)) {
		t.Fatal("oferta após o cooldown deveria ser permitida")
	}
	if !allowOnboardingOffer(peer.ID("peer-y"), now) {
		t.Fatal("peer diferente não deve ser afetado pelo cooldown de outro")
	}
}

// TestIsPrivateOrLoopback valida a restrição de rede do canal de onboarding.
func TestIsPrivateOrLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"/ip4/127.0.0.1/tcp/41121", true},
		{"/ip4/192.168.10.68/tcp/41121", true},
		{"/ip4/10.0.0.5/tcp/41121", true},
		{"/ip4/8.8.8.8/tcp/41121", false},
		{"/ip4/172.16.5.4/tcp/41121", true},
		// CGNAT 100.64/10 (Tailscale/VPN corporativa) — antes era recusado e
		// quebrava zero-touch em frota com VPN (achado da revisão 2026-10-29).
		{"/ip4/100.64.0.1/tcp/41121", true},
		{"/ip4/100.127.255.254/tcp/41121", true},
		{"/ip4/100.128.0.1/tcp/41121", false},
		// IPv6: loopback, link-local e ULA são locais; global é recusado.
		{"/ip6/::1/tcp/41121", true},
		{"/ip6/fe80::1/tcp/41121", true},
		{"/ip6/fd00::1/tcp/41121", true},
		{"/ip6/2804:858::1/tcp/41121", false},
	}
	for _, tc := range cases {
		ma, err := multiaddr.NewMultiaddr(tc.addr)
		if err != nil {
			t.Fatalf("multiaddr %q: %v", tc.addr, err)
		}
		if got := isPrivateOrLoopback(ma); got != tc.want {
			t.Fatalf("isPrivateOrLoopback(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// TestOnboardingOfferTTLCap garante que a oferta NUNCA anuncia mais validade que
// o token do servidor (regressão: expiresAt vazio caía no fallback de 30 min
// contra token de 10 min → 401/403 no registro).
func TestOnboardingOfferTTLCap(t *testing.T) {
	// expiresAt vazio → usa o teto onboardingDeployKeyTTL.
	depsEmpty := &onboardingTestDeps{mockDeps: &mockDeps{cfg: DefaultConfig(), tempDir: t.TempDir(), dataDir: t.TempDir()}}
	depsEmpty.expiresAt = ""
	transfer := &TransferServer{deps: depsEmpty, agentID: "agent-ttl"}
	offer, status, err := transfer.buildOnboardingOffer(context.Background())
	if err != nil || status != 200 {
		t.Fatalf("buildOnboardingOffer: status=%d err=%v", status, err)
	}
	exp, err := time.Parse(time.RFC3339, offer.ExpiresAtUTC)
	if err != nil {
		t.Fatalf("parse expiresAtUTC %q: %v", offer.ExpiresAtUTC, err)
	}
	if max := time.Now().Add(onboardingDeployKeyTTL + 5*time.Second); exp.After(max) {
		t.Fatalf("oferta anuncia validade %s além do teto %s", exp, max)
	}

	// expiresAt no passado → recusa (não emite oferta com TTL inventado).
	depsPast := &onboardingTestDeps{mockDeps: &mockDeps{cfg: DefaultConfig(), tempDir: t.TempDir(), dataDir: t.TempDir()}}
	depsPast.expiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	transferPast := &TransferServer{deps: depsPast, agentID: "agent-ttl"}
	if _, status, err = transferPast.buildOnboardingOffer(context.Background()); err == nil {
		t.Fatal("token expirado deveria recusar a oferta")
	} else if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

// TestOnboardingOfferAllowedDoesNotConsume garante que a checagem não consome a
// janela: só markOnboardingOffer consome (um 503 da API não pode bloquear o peer
// honesto pelo cooldown).
func TestOnboardingOfferAllowedDoesNotConsume(t *testing.T) {
	now := time.Now()
	pid := peer.ID("peer-check")
	reset := func() {
		p2pOnboardOfferMu.Lock()
		p2pOnboardOfferLastSeen = make(map[peer.ID]time.Time)
		p2pOnboardOfferGlobal = nil
		p2pOnboardOfferMu.Unlock()
	}
	reset()
	t.Cleanup(reset)

	for i := 0; i < 3; i++ {
		if !onboardingOfferAllowed(pid, now) {
			t.Fatalf("checagem %d deveria permitir (e não consumir)", i)
		}
	}
	markOnboardingOffer(pid, now)
	if onboardingOfferAllowed(pid, now.Add(time.Second)) {
		t.Fatal("após marcar, o peer deveria estar em cooldown")
	}
	// Limite global (peer.ID é gratuito): 5 ofertas na janela.
	reset()
	for i := 0; i < p2pOnboardGlobalMax; i++ {
		other := peer.ID(fmt.Sprintf("peer-global-%d", i))
		if !onboardingOfferAllowed(other, now) {
			t.Fatalf("oferta global %d deveria ser permitida", i)
		}
		markOnboardingOffer(other, now)
	}
	if onboardingOfferAllowed(peer.ID("peer-global-x"), now) {
		t.Fatal("limite global deveria bloquear ofertas além do teto")
	}
	if !onboardingOfferAllowed(peer.ID("peer-global-x"), now.Add(p2pOnboardGlobalWindow+time.Second)) {
		t.Fatal("janela global deveria expirar")
	}
}

// TestAccessTokenCacheKeyIncludesRequester garante que um token emitido para um
// requesterID não é reaproveitado por outro (o HMAC amarra token↔requester; após
// o zero-touch o AgentID muda em runtime).
func TestAccessTokenCacheKeyIncludesRequester(t *testing.T) {
	cache := newArtifactAccessTokenCache()
	pid := peer.ID("peer-cache")
	exp := time.Now().Add(10 * time.Minute)
	cache.store(pid, "artifact.exe", "requester-A", "token-A", exp)
	if tok, ok := cache.cached(pid, "artifact.exe", "requester-A"); !ok || tok != "token-A" {
		t.Fatalf("token do requester A não recuperado: %q ok=%v", tok, ok)
	}
	if _, ok := cache.cached(pid, "artifact.exe", "requester-B"); ok {
		t.Fatal("cache não pode servir token de outro requester")
	}
	cache.invalidate(pid)
	if _, ok := cache.cached(pid, "artifact.exe", "requester-A"); ok {
		t.Fatal("invalidate deveria limpar todos os tokens do peer")
	}
}

// TestLegacyVsAuthClassification garante que rejeição de token NÃO é
// classificada como peer legacy (bug: "peer: " casava qualquer erro e congelava
// o catálogo por 10 min com log mentindo "agente antigo").
func TestLegacyVsAuthClassification(t *testing.T) {
	authErr := fmt.Errorf("peer: token de acesso inválido: assinatura invalida (código auth)")
	if !isArtifactAccessAuthError(authErr) {
		t.Fatal("erro de token deveria ser classificado como auth")
	}
	if isLegacyDiscoveryPeerError(authErr) {
		t.Fatal("erro de token NÃO pode ser classificado como legacy")
	}
	legacyErr := fmt.Errorf("peer: artifact inválido (código unsupported)")
	if !isLegacyDiscoveryPeerError(legacyErr) {
		t.Fatal("rejeicao do artifact wildcard deveria ser legacy")
	}
	netErr := fmt.Errorf("stream peers: dial timeout")
	if isLegacyDiscoveryPeerError(netErr) || isArtifactAccessAuthError(netErr) {
		t.Fatal("erro de rede não pode ser legacy nem auth")
	}
}

// TestInvalidateArtifactOnManifestError garante que erro de CREDENCIAL não
// remove o artifact do catálogo (só staleness real remove).
func TestInvalidateArtifactOnManifestError(t *testing.T) {
	deps := &mockDeps{cfg: DefaultConfig(), tempDir: t.TempDir(), dataDir: t.TempDir()}
	c := NewCoordinator(deps)
	c.mu.Lock()
	canonicalID := CanonicalArtifactID("", "pacote.exe", "")
	c.peerArtifacts["agent-x"] = p2pPeerArtifactState{Artifacts: []P2PArtifactView{{ArtifactID: canonicalID, ArtifactName: "pacote.exe"}}}
	c.mu.Unlock()

	c.invalidateArtifactOnManifestError("agent-x", "pacote.exe", fmt.Errorf("peer: token expirado"))
	if got := c.PeersWithArtifactScored(canonicalID); len(got) != 1 {
		t.Fatalf("erro de credencial removeu o artifact do catálogo (%d fontes)", len(got))
	}
	c.invalidateArtifactOnManifestError("agent-x", "pacote.exe", fmt.Errorf("artifact no encontrado"))
	if got := c.PeersWithArtifactScored(canonicalID); len(got) != 0 {
		t.Fatalf("staleness real deveria remover o artifact (%d fontes)", len(got))
	}
}

// TestSameIPSet garante a reconciliação correta dos listeners de health quando
// os IPs da máquina mudam (DHCP/VPN/wifi->cabo).
func TestSameIPSet(t *testing.T) {
	cases := []struct {
		a, b []string
		want bool
	}{
		{nil, nil, true},
		{[]string{"192.168.10.68"}, []string{"192.168.10.68"}, true},
		{[]string{"192.168.10.68", "10.0.0.5"}, []string{"10.0.0.5", "192.168.10.68"}, true},
		{[]string{"192.168.10.68"}, []string{"192.168.10.69"}, false},
		{[]string{"192.168.10.68"}, nil, false},
		{[]string{"192.168.10.68"}, []string{"192.168.10.68", "10.0.0.5"}, false},
	}
	for _, tc := range cases {
		if got := sameIPSet(tc.a, tc.b); got != tc.want {
			t.Fatalf("sameIPSet(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestFetchOnboardingOfferFromPeersWithoutPeers garante erro explícito (e não
// silêncio) quando não há peer — o operador precisa saber a causa (Z9).
func TestFetchOnboardingOfferFromPeersWithoutPeers(t *testing.T) {
	deps := &onboardingTestDeps{mockDeps: &mockDeps{cfg: DefaultConfig(), tempDir: t.TempDir(), dataDir: t.TempDir()}}
	h, closeH := newTestLibP2PHost(t)
	defer closeH()
	coord := &Coordinator{
		deps:              deps,
		peers:             make(map[string]p2pPeerState),
		peerArtifacts:     make(map[string]p2pPeerArtifactState),
		peerLastAttempt:   make(map[string]time.Time),
		sha256Cache:       make(map[string]artifactSHA256CacheEntry),
		manifestHealth:    make(map[string]manifestHealthEntry),
		servingSessions:   make(map[string]*servingSession),
		downloadLocks:     make(map[string]*downloadLockEntry),
		discoveryProvider: &p2pLibP2PProvider{h: h, registry: newLibp2pPeerRegistry()},
	}
	_, err := coord.FetchOnboardingOfferFromPeers(context.Background())
	if err == nil {
		t.Fatal("esperava erro explícito sem peers conhecidos")
	}
	if !strings.Contains(err.Error(), "nenhum peer") {
		t.Fatalf("erro deveria explicar a causa, got %q", err.Error())
	}
	_ = fmt.Sprintf("%v", err)
}
