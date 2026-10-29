package p2p

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"discovery/app/core/agentconn"
	debug "discovery/app/debug"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// testMeshNode é um agente P2P completo em processo (coordinator + transfer
// server + host libp2p), usado para validar a malha de ponta a ponta sem
// precisar de duas máquinas.
type testMeshNode struct {
	agentID  string
	clientID string
	deps     *mockDeps
	coord    *Coordinator
	transfer *TransferServer
	host     host.Host
	registry *libp2pPeerRegistry
	dir      string
}

func newTestMeshNode(t *testing.T, agentID, clientID string) *testMeshNode {
	t.Helper()
	dir := t.TempDir()
	deps := &mockDeps{
		cfg:      DefaultConfig(),
		tempDir:  dir,
		dataDir:  t.TempDir(),
		debugCfg: debug.Config{AgentID: agentID},
	}
	deps.agentCfg.ClientID = clientID

	transfer := NewTransferServer(deps, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := transfer.Start(ctx, DefaultConfig(), agentID, dir, func() []P2PPeerView { return nil }); err != nil {
		t.Fatalf("transfer server %s: %v", agentID, err)
	}
	t.Cleanup(transfer.Close)

	coord := NewCoordinator(deps)
	coord.transferServer = transfer
	// O TransferServer precisa do coordinator para anunciar peerID/addrs no
	// /p2p/health (é o que o probe de LAN consome para conectar).
	transfer.coord = coord

	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p host %s: %v", agentID, err)
	}
	t.Cleanup(func() { _ = h.Close() })

	self := p2pSelfEndpoint{AgentID: agentID, Host: "host-" + agentID, Port: 41080, ClientID: clientID}
	registry := newLibp2pPeerRegistry()
	h.SetStreamHandler(p2pLibP2PProtocolID, func(s network.Stream) {
		handleLibP2PHandshakeStream(s, self, clientID, registry, nil, func(p p2pDiscoveredPeer) {
			coord.upsertPeer(p)
		}, nil)
	})
	// Protocolos de transferência/gossip no mesmo host (como em produção).
	RegisterP2PProtocols(h, coord, transfer)

	coord.discoveryProvider = &p2pLibP2PProvider{h: h, registry: registry, coord: coord, transfer: transfer}

	return &testMeshNode{agentID: agentID, clientID: clientID, deps: deps, coord: coord, transfer: transfer, host: h, registry: registry, dir: dir}
}

// discoverAs simula o que o mDNS faz: conecta no outro nó e troca identidade.
func (n *testMeshNode) discoverAs(t *testing.T, other *testMeshNode) {
	t.Helper()
	notifee := &libp2pMDNSNotifee{
		h:        n.host,
		self:     p2pSelfEndpoint{AgentID: n.agentID, Host: "host-" + n.agentID, Port: 41080, ClientID: n.clientID},
		onPeer:   func(p p2pDiscoveredPeer) { n.coord.upsertPeer(p) },
		onTrace:  func(msg string) { t.Logf("[%s] %s", n.agentID, msg) },
		registry: n.registry,
		clientID: n.clientID,
	}
	notifee.exchangeWithPeer(peer.AddrInfo{ID: other.host.ID(), Addrs: other.host.Addrs()})
}

func findPeer(peers []P2PPeerView, agentID string) (P2PPeerView, bool) {
	for _, p := range peers {
		if p.AgentID == agentID {
			return p, true
		}
	}
	return P2PPeerView{}, false
}

// libp2pListenPort extrai a porta TCP do primeiro endereço do host.
func libp2pListenPort(h host.Host) int {
	for _, addr := range h.Addrs() {
		parts := strings.Split(addr.String(), "/")
		for i, part := range parts {
			if part == "tcp" && i+1 < len(parts) {
				var port int
				for _, ch := range parts[i+1] {
					if ch < '0' || ch > '9' {
						port = 0
						break
					}
					port = port*10 + int(ch-'0')
				}
				if port > 0 {
					return port
				}
			}
		}
	}
	return 0
}

// TestP2PEndToEndDiscoveryAndCatalog valida a malha ponta a ponta:
//  1. handshake libp2p (achado D1) entrega e REGISTRA o peer nos dois lados;
//  2. o peer aparece em GetPeers() (o que a UI mostra e o gossip consome);
//  3. o gossip autenticado por token de discovery (achado T1) traz o catálogo
//     de artifacts do outro nó para o estado local.
func TestP2PEndToEndDiscoveryAndCatalog(t *testing.T) {
	const clientID = "client-1"
	nodeA := newTestMeshNode(t, "agent-A", clientID)
	nodeB := newTestMeshNode(t, "agent-B", clientID)

	// Artifact publicado por A (arquivo real no P2P_Temp de A).
	if err := os.WriteFile(filepath.Join(nodeA.dir, "pacote-teste.exe"), []byte("conteudo-do-artifact"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	nodeB.discoverAs(t, nodeA)

	// 1+2: os dois lados conhecem o peer (B via notifiee, A via responder).
	if p, ok := findPeer(nodeB.coord.GetPeers(), "agent-A"); !ok {
		t.Fatalf("B não registrou A: %+v", nodeB.coord.GetPeers())
	} else if p.Address != "127.0.0.1" {
		t.Fatalf("endereço de A em B = %q", p.Address)
	}
	if _, ok := findPeer(nodeA.coord.GetPeers(), "agent-B"); !ok {
		t.Fatalf("A não registrou B (responder não emitiu onPeer): %+v", nodeA.coord.GetPeers())
	}

	// 3: gossip autenticado (token de discovery "*") popula o catálogo em B.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// RefreshPeerArtifactIndex = pullPeerGossip: negocia token por peer e aplica
	// a resposta (é o caminho de produção usado pelo ticker de 45 s).
	nodeB.coord.RefreshPeerArtifactIndex(ctx, "test")

	deadline := time.Now().Add(10 * time.Second)
	for {
		nodeB.coord.mu.RLock()
		state, ok := nodeB.coord.peerArtifacts["agent-a"]
		nodeB.coord.mu.RUnlock()
		if ok && len(state.Artifacts) > 0 {
			found := false
			for _, a := range state.Artifacts {
				if a.ArtifactName == "pacote-teste.exe" {
					found = true
					if a.ChecksumSHA256 == "" {
						t.Fatalf("artifact sem checksum: %+v", a)
					}
				}
			}
			if !found {
				t.Fatalf("catálogo de A não contém pacote-teste.exe: %+v", state.Artifacts)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("catálogo via gossip não populado em B (T1). peers=%+v", nodeB.coord.GetPeers())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestP2PEndToEndLANHealthProbe valida que o health de LAN responde ao probe com
// agentId e peerID/addrs do libp2p (D2) — é isso que permite ao probe descobrir
// um peer sem mDNS.
func TestP2PEndToEndLANHealthProbe(t *testing.T) {
	node := newTestMeshNode(t, "agent-lan", "client-1")
	ls, err := node.coord.startLANHealthServerOn([]string{"127.0.0.1"}, 0)
	if err != nil {
		t.Fatalf("startLANHealthServerOn: %v", err)
	}
	defer ls.Close()

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(ls.urls[0])
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer resp.Body.Close()
	var health p2pHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health.AgentID != "agent-lan" {
		t.Fatalf("agentId = %q", health.AgentID)
	}
	if health.LibP2PPeerID != node.host.ID().String() {
		t.Fatalf("libp2pPeerId = %q, want %q", health.LibP2PPeerID, node.host.ID().String())
	}
	// Em produção o host escuta em 0.0.0.0 e o health anuncia os multiaddrs com
	// o IP da LAN. No teste o host escuta só em 127.0.0.1, e
	// buildP2PAdvertiseAddrs descarta loopback por desenho — só exigimos addrs
	// quando houver endereço roteável.
	routable := false
	for _, addr := range node.host.Addrs() {
		if !strings.Contains(addr.String(), "127.0.0.1") {
			routable = true
		}
	}
	if routable && len(health.LibP2PAddrs) == 0 {
		t.Fatal("health sem libp2pAddrs com endereço roteável disponível")
	}
}

// TestPeerOnlineEventRegistersPeer cobre o achado D4: o evento NATS peer.online
// conectava no libp2p mas NÃO registrava o peer no estado do coordinator, então
// a UI/gossip continuavam sem o peer.
func TestPeerOnlineEventRegistersPeer(t *testing.T) {
	nodeA := newTestMeshNode(t, "agent-online-A", "client-1")
	nodeB := newTestMeshNode(t, "agent-online-B", "client-1")

	port := libp2pListenPort(nodeA.host)
	if port <= 0 {
		t.Fatal("porta do libp2p de A não encontrada")
	}
	nodeB.coord.HandlePeerOnlineEvent(agentconn.PeerEventMessage{
		EventType: "peer.online",
		ClientID:  "client-1",
		AgentID:   "agent-online-A",
		PeerID:    nodeA.host.ID().String(),
		Addrs:     []string{"127.0.0.1"},
		Port:      port,
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		if p, ok := findPeer(nodeB.coord.GetPeers(), "agent-online-A"); ok {
			if p.Source != "nats-event" {
				t.Fatalf("source = %q, want nats-event", p.Source)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("peer.online não registrou o peer no estado (D4): %+v", nodeB.coord.GetPeers())
		}
		time.Sleep(100 * time.Millisecond)
	}
}
