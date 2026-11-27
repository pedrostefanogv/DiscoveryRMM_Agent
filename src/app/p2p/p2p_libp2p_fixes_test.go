package p2p

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/core/testutil"
	"discovery/app/core/tlsutil"
	debug "discovery/app/debug"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	quic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/multiformats/go-multiaddr"
)

func TestBuildAddrInfoRespectsTCPOnly(t *testing.T) {
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	defer h.Close()

	tcpInfo, err := buildAddrInfo(h.ID().String(), []string{"192.168.10.20"}, 41121, true)
	if err != nil {
		t.Fatalf("buildAddrInfo(tcpOnly=true): %v", err)
	}
	if len(tcpInfo.Addrs) != 1 || !strings.Contains(tcpInfo.Addrs[0].String(), "/tcp/41121") {
		t.Fatalf("esperava apenas multiaddr TCP, obtido: %v", tcpInfo.Addrs)
	}

	dualInfo, err := buildAddrInfo(h.ID().String(), []string{"192.168.10.20"}, 41121, false)
	if err != nil {
		t.Fatalf("buildAddrInfo(tcpOnly=false): %v", err)
	}
	if len(dualInfo.Addrs) != 2 {
		t.Fatalf("esperava 2 multiaddrs (TCP + QUIC), obtido: %v", dualInfo.Addrs)
	}
	hasTCP, hasQUIC := false, false
	for _, a := range dualInfo.Addrs {
		s := a.String()
		if strings.Contains(s, "/tcp/41121") {
			hasTCP = true
		}
		if strings.Contains(s, "/udp/41121/quic-v1") {
			hasQUIC = true
		}
	}
	if !hasTCP || !hasQUIC {
		t.Fatalf("esperava TCP e QUIC, obtido: %v", dualInfo.Addrs)
	}
}

func TestLibp2pProviderTCPOnlyRejectsQUICAndStartsMDNS(t *testing.T) {
	t.Setenv("DISCOVERY_P2P_LIBP2P_PORT", "0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &p2pLibP2PProvider{
		tcpOnly:  true,
		registry: newLibp2pPeerRegistry(),
	}
	err := p.Start(ctx, p2pSelfEndpoint{
		AgentID:  "agent-tcp-only",
		ClientID: "client-1",
		Port:     41080,
	}, func(p2pDiscoveredPeer) {}, nil)
	if err != nil {
		t.Fatalf("provider.Start: %v", err)
	}
	defer p.Close()

	if p.svc == nil {
		t.Fatal("esperava p.svc != nil apos Start (svc.Start deve ser chamado)")
	}

	// Peer remoto escutando apenas em QUIC/v1.
	quicPeer, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
		libp2p.Transport(quic.NewTransport),
	)
	if err != nil {
		t.Fatalf("quicPeer: %v", err)
	}
	defer quicPeer.Close()

	var quicAddrs []multiaddr.Multiaddr
	for _, a := range quicPeer.Addrs() {
		if strings.Contains(a.String(), "quic") {
			quicAddrs = append(quicAddrs, a)
		}
	}
	if len(quicAddrs) == 0 {
		t.Fatal("quicPeer sem multiaddr QUIC")
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, 2*time.Second)
	defer dialCancel()
	if err := p.h.Connect(dialCtx, peer.AddrInfo{ID: quicPeer.ID(), Addrs: quicAddrs}); err == nil {
		t.Fatal("host tcpOnly=true conseguiu discar para multiaddr QUIC-only")
	}
}

func TestRunCloudBootstrapRegistersPeerInRegistry(t *testing.T) {
	remoteHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("remoteHost: %v", err)
	}
	defer remoteHost.Close()

	remotePort := libp2pListenPort(remoteHost)
	if remotePort <= 0 {
		t.Fatal("porta do remoteHost nao encontrada")
	}

	localHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("localHost: %v", err)
	}
	defer localHost.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != p2pCloudBootstrapEndpoint {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p2pCloudBootstrapResponse{
			Peers: []p2pCloudBootstrapPeer{{
				PeerID:  remoteHost.ID().String(),
				AgentID: "agent-cloud-remote",
				Addrs:   []string{"127.0.0.1"},
				Port:    remotePort,
			}},
		})
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	cfg := DefaultConfig()
	cfg.BootstrapConfig.CloudBootstrapEnabled = true
	deps := &mockDeps{
		cfg: cfg,
		debugCfg: debug.Config{
			AgentID:     "11111111-1111-4111-8111-111111111111",
			AuthToken:   "mdz_test_token",
			ApiServer:   u.Host,
			ApiInsecure: true,
		},
		agentCfg: agentconfig.AgentConfiguration{ClientID: "client-1"},
		tempDir:  testutil.TempDir(t),
		dataDir:  testutil.TempDir(t),
	}
	coord := NewCoordinator(deps)
	registry := newLibp2pPeerRegistry()
	coord.discoveryProvider = &p2pLibP2PProvider{h: localHost, registry: registry}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	count, err := coord.RunCloudBootstrap(ctx)
	if err != nil {
		t.Fatalf("RunCloudBootstrap: %v (count=%d)", err, count)
	}

	gotID, ok := registry.Lookup("agent-cloud-remote")
	if !ok || gotID != remoteHost.ID() {
		t.Fatalf("registry.Lookup(agent-cloud-remote) = (%s, %v), want (%s, true)", gotID, ok, remoteHost.ID())
	}

	peers := coord.GetPeers()
	if _, found := findPeer(peers, "agent-cloud-remote"); !found {
		t.Fatalf("peer nao encontrado em coord.GetPeers(): %s", fmt.Sprintf("%+v", peers))
	}
}

func TestFilterP2PAdvertiseAddrs(t *testing.T) {
	mustMA := func(s string) multiaddr.Multiaddr {
		m, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			t.Fatalf("NewMultiaddr(%q): %v", s, err)
		}
		return m
	}

	apipa := mustMA("/ip4/169.254.10.5/tcp/41121")
	loop := mustMA("/ip4/127.0.0.1/tcp/41121")
	lan := mustMA("/ip4/192.168.1.50/tcp/41121")

	gotMixed := filterP2PAdvertiseAddrs([]multiaddr.Multiaddr{apipa, loop, lan})
	if len(gotMixed) != 1 || gotMixed[0].String() != lan.String() {
		t.Fatalf("esperava apenas LAN %s, obtido: %v", lan, gotMixed)
	}

	gotLoopOnly := filterP2PAdvertiseAddrs([]multiaddr.Multiaddr{apipa, loop})
	if len(gotLoopOnly) != 1 || gotLoopOnly[0].String() != loop.String() {
		t.Fatalf("esperava fallback loopback %s, obtido: %v", loop, gotLoopOnly)
	}
}

func TestBuildP2PResourceManager(t *testing.T) {
	rm, err := buildP2PResourceManager()
	if err != nil {
		t.Fatalf("buildP2PResourceManager: %v", err)
	}
	if rm == nil {
		t.Fatal("ResourceManager nil")
	}
	_ = rm.Close()
}

func TestChunkSchedulerPrefersLowerRTTOnErrorTie(t *testing.T) {
	hSlow, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatalf("hSlow: %v", err)
	}
	defer hSlow.Close()
	hFast, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatalf("hFast: %v", err)
	}
	defer hFast.Close()

	sched := newP2PChunkScheduler()
	sched.recordRTT(hSlow.ID(), 80*time.Millisecond)
	sched.recordRTT(hFast.ID(), 5*time.Millisecond)

	peers := []libp2pPeer{
		{agentID: "slow", peerID: hSlow.ID()},
		{agentID: "fast", peerID: hFast.ID()},
	}
	picked := sched.pickPeer(0, peers)
	if picked.peerID != hFast.ID() {
		t.Fatalf("esperava peer mais rapido %s, escolhido %s", hFast.ID(), picked.peerID)
	}

	// Erro continua tendo prioridade sobre RTT.
	sched.recordError(hFast.ID())
	pickedAfterErr := sched.pickPeer(1, peers)
	if pickedAfterErr.peerID != hSlow.ID() {
		t.Fatalf("esperava peer sem erros %s, escolhido %s", hSlow.ID(), pickedAfterErr.peerID)
	}
}

func TestTransitiveGossipConnectsAndRegistersPeer(t *testing.T) {
	prevLoopback := allowLoopbackGossipDialForTest
	allowLoopbackGossipDialForTest = true
	t.Cleanup(func() { allowLoopbackGossipDialForTest = prevLoopback })

	nodeC, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("nodeC: %v", err)
	}
	defer nodeC.Close()
	portC := libp2pListenPort(nodeC)

	nodeA, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("nodeA: %v", err)
	}
	defer nodeA.Close()

	deps := &mockDeps{
		cfg: DefaultConfig(),
		debugCfg: debug.Config{
			AgentID: "agent-A",
		},
		agentCfg: agentconfig.AgentConfiguration{ClientID: "client-1"},
		tempDir:  testutil.TempDir(t),
		dataDir:  testutil.TempDir(t),
	}
	coordA := NewCoordinator(deps)
	if err := coordA.Startup(context.Background()); err != nil {
		t.Fatalf("Startup: %v", err)
	}
	defer func() { _ = coordA.Shutdown() }()

	registryA := newLibp2pPeerRegistry()
	coordA.discoveryProvider = &p2pLibP2PProvider{h: nodeA, registry: registryA}

	coordA.applyGossipResponse("agent-B", []P2PPeerView{{
		AgentID:      "agent-C",
		ClientID:     "client-1",
		Host:         "host-C",
		Address:      "127.0.0.1",
		Port:         41080,
		LibP2PPeerID: nodeC.ID().String(),
		LibP2PPort:   portC,
	}}, nil, "gossip")

	deadline := time.Now().Add(8 * time.Second)
	for {
		if gotID, ok := registryA.Lookup("agent-C"); ok && gotID == nodeC.ID() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gossip transitivo nao conectou/registrou agent-C no registry")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestUpsertPeerPreservesLibP2PMetadata(t *testing.T) {
	deps := &mockDeps{cfg: DefaultConfig(), tempDir: testutil.TempDir(t), dataDir: testutil.TempDir(t)}
	coord := NewCoordinator(deps)

	coord.upsertPeer(p2pDiscoveredPeer{
		AgentID:      "agent-preserve",
		Host:         "host-1",
		Address:      "192.168.1.10",
		Port:         41080,
		LibP2PPeerID: "12D3KooWTestPeer",
		LibP2PPort:   41121,
		Source:       "libp2p",
	})

	// Atualização posterior por caminho sem metadados libp2p (ex.: lan-probe)
	coord.upsertPeer(p2pDiscoveredPeer{
		AgentID: "agent-preserve",
		Host:    "host-1",
		Address: "192.168.1.10",
		Port:    41080,
		Source:  "lan-probe",
	})

	peers := coord.GetPeers()
	got, ok := findPeer(peers, "agent-preserve")
	if !ok {
		t.Fatal("peer nao encontrado")
	}
	if got.LibP2PPeerID != "12D3KooWTestPeer" || got.LibP2PPort != 41121 {
		t.Fatalf("metadados libp2p perdidos no upsertPeer: %+v", got)
	}
}

func TestNormalizeConfigEnvOverrides(t *testing.T) {
	t.Setenv("DISCOVERY_P2P_TCP_ONLY", "false")
	t.Setenv("DISCOVERY_P2P_HOLE_PUNCHING", "true")
	t.Setenv("DISCOVERY_P2P_STATIC_RELAYS", "/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWRelay1, /ip4/5.6.7.8/udp/4001/quic-v1/p2p/12D3KooWRelay2")

	cfg := NormalizeConfig(DefaultConfig())
	if cfg.TCPOnlyEnabled() {
		t.Fatal("esperava TCPOnlyEnabled() == false com DISCOVERY_P2P_TCP_ONLY=false")
	}
	if !cfg.BootstrapConfig.EnableHolePunching {
		t.Fatal("esperava EnableHolePunching == true com DISCOVERY_P2P_HOLE_PUNCHING=true")
	}
	if len(cfg.BootstrapConfig.StaticRelays) != 2 {
		t.Fatalf("esperava 2 static relays, obtido: %v", cfg.BootstrapConfig.StaticRelays)
	}
}

func TestLibp2pProviderQUICCanaryEnablesQUICTransport(t *testing.T) {
	t.Setenv("DISCOVERY_P2P_LIBP2P_PORT", "0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &p2pLibP2PProvider{
		tcpOnly:  false,
		registry: newLibp2pPeerRegistry(),
	}
	if err := p.Start(ctx, p2pSelfEndpoint{
		AgentID:  "agent-quic-canary",
		ClientID: "client-1",
		Port:     41080,
	}, func(p2pDiscoveredPeer) {}, nil); err != nil {
		t.Fatalf("provider.Start: %v", err)
	}
	defer p.Close()

	hasQUIC := false
	for _, a := range p.h.Addrs() {
		if strings.Contains(a.String(), "/quic-v1") {
			hasQUIC = true
			break
		}
	}
	if !hasQUIC {
		t.Fatalf("esperava listen addr /quic-v1 com tcpOnly=false, obtido: %v", p.h.Addrs())
	}

	quicPeer, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/udp/0/quic-v1"),
		libp2p.Transport(quic.NewTransport),
	)
	if err != nil {
		t.Fatalf("quicPeer: %v", err)
	}
	defer quicPeer.Close()

	var quicAddrs []multiaddr.Multiaddr
	for _, a := range quicPeer.Addrs() {
		if strings.Contains(a.String(), "/quic-v1") {
			quicAddrs = append(quicAddrs, a)
		}
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, 3*time.Second)
	defer dialCancel()
	if err := p.h.Connect(dialCtx, peer.AddrInfo{ID: quicPeer.ID(), Addrs: quicAddrs}); err != nil {
		t.Fatalf("host tcpOnly=false falhou ao discar QUIC: %v", err)
	}
}

func TestLibp2pProviderNATTraversalOptions(t *testing.T) {
	t.Setenv("DISCOVERY_P2P_LIBP2P_PORT", "0")

	relayHost, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatalf("relayHost: %v", err)
	}
	defer relayHost.Close()

	validRelay := fmt.Sprintf("/ip4/127.0.0.1/tcp/41121/p2p/%s", relayHost.ID())
	parsed := parseStaticRelayAddrs([]string{"", "invalid-addr", validRelay}, nil)
	if len(parsed) != 1 || parsed[0].ID != relayHost.ID() {
		t.Fatalf("parseStaticRelayAddrs inesperado: %+v", parsed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &p2pLibP2PProvider{
		tcpOnly:            true,
		enableHolePunching: true,
		staticRelays:       []string{validRelay},
		registry:           newLibp2pPeerRegistry(),
	}
	if err := p.Start(ctx, p2pSelfEndpoint{
		AgentID:  "agent-nat-traversal",
		ClientID: "client-1",
		Port:     41080,
	}, func(p2pDiscoveredPeer) {}, nil); err != nil {
		t.Fatalf("provider.Start com NAT traversal: %v", err)
	}
	defer p.Close()
}

func TestLibp2pProviderMDNSDisabledByPolicy(t *testing.T) {
	t.Setenv("DISCOVERY_P2P_LIBP2P_PORT", "0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &p2pLibP2PProvider{
		tcpOnly:      true,
		mdnsDisabled: true,
		registry:     newLibp2pPeerRegistry(),
	}
	if err := p.Start(ctx, p2pSelfEndpoint{
		AgentID:  "agent-mdns-off",
		ClientID: "client-1",
		Port:     41080,
	}, func(p2pDiscoveredPeer) {}, nil); err != nil {
		t.Fatalf("provider.Start: %v", err)
	}
	defer p.Close()
	if p.svc != nil {
		t.Fatal("esperava p.svc == nil quando mdnsDisabled=true")
	}
}

func TestLibp2pProviderMDNSSkippedOnPublicNetwork(t *testing.T) {
	t.Setenv("DISCOVERY_P2P_LIBP2P_PORT", "0")
	prevFn := isPublicNetworkProfileFn
	isPublicNetworkProfileFn = func() (bool, string) {
		return true, "perfil Publica simulado"
	}
	t.Cleanup(func() { isPublicNetworkProfileFn = prevFn })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &p2pLibP2PProvider{
		tcpOnly:  true,
		registry: newLibp2pPeerRegistry(),
	}
	if err := p.Start(ctx, p2pSelfEndpoint{
		AgentID:  "agent-public-wifi",
		ClientID: "client-1",
		Port:     41080,
	}, func(p2pDiscoveredPeer) {}, nil); err != nil {
		t.Fatalf("provider.Start: %v", err)
	}
	defer p.Close()
	if p.svc != nil {
		t.Fatal("esperava p.svc == nil em perfil de rede Publica")
	}
}

func TestIsAllowedTransitiveGossipAddr(t *testing.T) {
	prevLoopback := allowLoopbackGossipDialForTest
	allowLoopbackGossipDialForTest = false
	t.Cleanup(func() { allowLoopbackGossipDialForTest = prevLoopback })

	if !isAllowedTransitiveGossipAddr("192.168.1.25", false) {
		t.Fatal("esperava RFC1918 permitido")
	}
	if !isAllowedTransitiveGossipAddr("100.64.10.5", false) {
		t.Fatal("esperava CGNAT permitido")
	}
	if isAllowedTransitiveGossipAddr("127.0.0.1", false) {
		t.Fatal("loopback nao deve ser permitido em producao")
	}
	if isAllowedTransitiveGossipAddr("169.254.1.10", true) {
		t.Fatal("link-local nao deve ser permitido")
	}
	if isAllowedTransitiveGossipAddr("8.8.8.8", false) {
		t.Fatal("IP publico nao deve ser permitido sem NAT traversal")
	}
	if !isAllowedTransitiveGossipAddr("8.8.8.8", true) {
		t.Fatal("IP publico deve ser permitido com NAT traversal")
	}
}

func TestResolveLibP2PUserAgentDoesNotLeakAgentID(t *testing.T) {
	deps := &mockDeps{
		debugCfg: debug.Config{AgentID: "11111111-1111-4111-8111-111111111111"},
	}
	coord := NewCoordinator(deps)
	ua := resolveLibP2PUserAgent(coord)
	if strings.Contains(ua, "11111111") {
		t.Fatalf("UserAgent vazou AgentID: %q", ua)
	}
	if ua != "discovery-agent" {
		t.Fatalf("UserAgent = %q, want discovery-agent", ua)
	}
}

func makeTestP2PMtlsIdentity(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, caPEM, agentID, clientID string, serial int64) *tlsutil.AgentMtlsIdentity {
	t.Helper()
	dir := t.TempDir()
	key, err := tlsutil.LoadOrCreateMtlsKey(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateMtlsKey: %v", err)
	}
	csrPEM, err := tlsutil.CreateAgentCSR(key, agentID)
	if err != nil {
		t.Fatalf("CreateAgentCSR: %v", err)
	}
	block, _ := pem.Decode([]byte(csrPEM))
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}
	agentURI, _ := url.Parse("urn:discovery:agent:" + agentID)
	clientURI, _ := url.Parse("urn:discovery:client:" + clientID)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject: pkix.Name{
			CommonName:         "agent:" + agentID,
			OrganizationalUnit: []string{"client:" + clientID},
			Organization:       []string{"DiscoveryRMM"},
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{agentURI, clientURI},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, csr.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if err := tlsutil.SaveMtlsCerts(dir, certPEM, caPEM); err != nil {
		t.Fatalf("SaveMtlsCerts: %v", err)
	}
	id, err := tlsutil.LoadMtlsIdentity(dir)
	if err != nil {
		t.Fatalf("LoadMtlsIdentity: %v", err)
	}
	return id
}

func TestP2PStrictMtlsHandshakeAndAccessEnforcement(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("caKey: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test P2P CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("caCert: %v", err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))

	idA := makeTestP2PMtlsIdentity(t, caCert, caKey, caPEM, "agent-mtls-A", "client-1", 10)
	idB := makeTestP2PMtlsIdentity(t, caCert, caKey, caPEM, "agent-mtls-B", "client-1", 11)
	idOtherTenant := makeTestP2PMtlsIdentity(t, caCert, caKey, caPEM, "agent-mtls-C", "client-2", 12)

	nodeA := newTestMeshNode(t, "agent-mtls-A", "client-1")
	nodeB := newTestMeshNode(t, "agent-mtls-B", "client-1")
	nodeC := newTestMeshNode(t, "agent-mtls-C", "client-1") // fingindo mesmo clientId no JSON

	nodeA.coord.SetMtlsIdentity(idA)
	nodeB.coord.SetMtlsIdentity(idB)
	nodeC.coord.SetMtlsIdentity(idOtherTenant)

	// Re-registrar handler com mtlsIdentity atualizado
	nodeA.host.SetStreamHandler(p2pLibP2PProtocolID, func(s network.Stream) {
		handleLibP2PHandshakeStream(s, p2pSelfEndpoint{AgentID: "agent-mtls-A", ClientID: "client-1", Port: 41080},
			"client-1", nodeA.registry, nil, nodeA.coord.getMtlsIdentity(), func(p2pDiscoveredPeer) {}, nil)
	})

	// 1. Mesmo tenant com cert valido -> sucesso e MarkMtlsVerified
	notifB := &libp2pMDNSNotifee{
		h:        nodeB.host,
		self:     p2pSelfEndpoint{AgentID: "agent-mtls-B", ClientID: "client-1", Port: 41080},
		onPeer:   func(p2pDiscoveredPeer) {},
		registry: nodeB.registry,
		clientID: "client-1",
		coord:    nodeB.coord,
	}
	notifB.exchangeWithPeer(peer.AddrInfo{ID: nodeA.host.ID(), Addrs: nodeA.host.Addrs()})
	if !nodeB.registry.IsMtlsVerified(nodeA.host.ID()) {
		t.Fatal("esperava nodeA marcado como IsMtlsVerified em nodeB")
	}

	// 2. Tenant divergente no certificado (client-2 tentando entrar em client-1) -> rejeitado
	notifC := &libp2pMDNSNotifee{
		h:        nodeC.host,
		self:     p2pSelfEndpoint{AgentID: "agent-mtls-C", ClientID: "client-1", Port: 41080},
		onPeer:   func(p2pDiscoveredPeer) {},
		registry: nodeC.registry,
		clientID: "client-1",
		coord:    nodeC.coord,
	}
	notifC.exchangeWithPeer(peer.AddrInfo{ID: nodeA.host.ID(), Addrs: nodeA.host.Addrs()})
	if nodeA.registry.IsMtlsVerified(nodeC.host.ID()) {
		t.Fatal("peer de tenant divergente nao deveria passar no handshake mTLS estrito")
	}

	// 3. Peer sem mTLS verificado tentando /artifact/access -> recusado com erro de auth
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := nodeC.host.Connect(ctx, peer.AddrInfo{ID: nodeA.host.ID(), Addrs: nodeA.host.Addrs()}); err != nil {
		t.Fatalf("connect C->A: %v", err)
	}
	_, err = libp2pRequestDiscoveryAccess(ctx, nodeC.host, nodeA.host.ID(), "agent-mtls-C")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "mtls") {
		t.Fatalf("esperava rejeicao mTLS em /artifact/access, obtido: %v", err)
	}
}
