package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/core/testutil"
	debug "discovery/app/debug"

	libp2p "github.com/libp2p/go-libp2p"
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
