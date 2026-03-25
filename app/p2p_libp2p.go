package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/multiformats/go-multiaddr"
)

const (
	p2pDiscoveryLibP2P        = "libp2p"
	p2pLibP2PRendezvous       = "discovery-agent-v1"
	p2pLibP2PProtocolID       = "/discovery-p2p/1.0.0"
	p2pLibP2PHandshakeTimeout = 5 * time.Second
)

// p2pLibP2PPeerInfo is exchanged over the /discovery-p2p/1.0.0 stream.
type p2pLibP2PPeerInfo struct {
	AgentID  string `json:"agentId"`
	HTTPPort int    `json:"httpPort"`
}

// p2pLibP2PProvider implements p2pDiscoveryProvider using go-libp2p mDNS.
// It advertises the local agent on the LAN and discovers peers via libp2p's
// built-in mDNS service (distinct from the existing grandcat/zeroconf path).
// When a peer is found, a /discovery-p2p/1.0.0 stream is opened to exchange
// {agentId, httpPort}. The full P2P transport protocols (/artifact/*, /discovery/peers)
// are registered on the same host so artifact transfers avoid HTTP entirely.
type p2pLibP2PProvider struct {
	// bootstrapPeers holds optional static multiaddr strings (including peer IDs)
	// to connect to at startup, enabling discovery in non-multicast networks.
	bootstrapPeers []string

	// coord and transfer are injected by startDiscovery so the host can serve
	// the libp2p transport protocols (peers/access/manifest/get/replicate).
	coord    *p2pCoordinator
	transfer *p2pTransferServer

	// registry maps agentID -> libp2p peer.ID for client-side calls.
	registry *libp2pPeerRegistry

	// host is exported after Start so the coordinator can open outbound streams.
	h host.Host
}

func (p *p2pLibP2PProvider) Name() string { return p2pDiscoveryLibP2P }

func (p *p2pLibP2PProvider) Start(
	ctx context.Context,
	self p2pSelfEndpoint,
	onPeer func(peer p2pDiscoveredPeer),
	onTrace func(string),
) error {
	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
	)
	if err != nil {
		return fmt.Errorf("libp2p host: %w", err)
	}

	// Stream handler (responder side): when a peer opens a stream to us,
	// read their info, respond with ours, then emit onPeer.
	h.SetStreamHandler(p2pLibP2PProtocolID, func(s network.Stream) {
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(p2pLibP2PHandshakeTimeout))

		var remote p2pLibP2PPeerInfo
		if err := json.NewDecoder(bufio.NewReader(s)).Decode(&remote); err != nil {
			return
		}
		mine := p2pLibP2PPeerInfo{AgentID: self.AgentID, HTTPPort: self.Port}
		if err := json.NewEncoder(s).Encode(mine); err != nil {
			return
		}

		if strings.TrimSpace(remote.AgentID) == "" || remote.HTTPPort <= 0 {
			return
		}
		remoteAddr := extractIPFromMultiaddr(s.Conn().RemoteMultiaddr().String())
		if remoteAddr == "" {
			return
		}
		if onTrace != nil {
			onTrace(fmt.Sprintf("libp2p peer (inbound): agentId=%s addr=%s:%d",
				remote.AgentID, remoteAddr, remote.HTTPPort))
		}
		// Registrar mapeamento agentID → peer.ID para transfer streams.
		if p.registry != nil {
			p.registry.Register(remote.AgentID, s.Conn().RemotePeer())
		}
		onPeer(p2pDiscoveredPeer{
			AgentID:      strings.TrimSpace(remote.AgentID),
			Host:         remoteAddr,
			Address:      remoteAddr,
			Port:         remote.HTTPPort,
			Source:       p2pDiscoveryLibP2P,
			ConnectedVia: p2pDiscoveryLibP2P,
		})
	})

	// Registrar todos os protocolos de transporte P2P no host.
	if p.coord != nil && p.transfer != nil {
		RegisterP2PProtocols(h, p.coord, p.transfer)
	}
	if p.registry == nil {
		p.registry = newLibp2pPeerRegistry()
	}
	p.h = h

	notifee := &libp2pMDNSNotifee{h: h, self: self, onPeer: onPeer, onTrace: onTrace, registry: p.registry}
	svc := mdns.NewMdnsService(h, p2pLibP2PRendezvous, notifee)

	// Connect to static bootstrap peers, if configured.
	// These are used in corporate/VPN networks where mDNS multicast is blocked.
	for _, addrStr := range p.bootstrapPeers {
		addrStr = strings.TrimSpace(addrStr)
		if addrStr == "" {
			continue
		}
		ma, err := multiaddr.NewMultiaddr(addrStr)
		if err != nil {
			if onTrace != nil {
				onTrace(fmt.Sprintf("libp2p bootstrap peer multiaddr invalido %q: %v", addrStr, err))
			}
			continue
		}
		pi, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			if onTrace != nil {
				onTrace(fmt.Sprintf("libp2p bootstrap peer info invalido %q: %v", addrStr, err))
			}
			continue
		}
		if err := h.Connect(ctx, *pi); err != nil {
			if onTrace != nil {
				onTrace(fmt.Sprintf("libp2p bootstrap connect falhou %s: %v", addrStr, err))
			}
		} else if onTrace != nil {
			onTrace(fmt.Sprintf("libp2p bootstrap peer conectado: %s", addrStr))
		}
	}

	go func() {
		<-ctx.Done()
		_ = svc.Close()
		_ = h.Close()
	}()

	if onTrace != nil {
		onTrace(fmt.Sprintf("libp2p host iniciado: peerID=%s", h.ID()))
	}
	return nil
}

// libp2pMDNSNotifee handles peers discovered by libp2p's mDNS service.
type libp2pMDNSNotifee struct {
	h        host.Host
	self     p2pSelfEndpoint
	onPeer   func(p2pDiscoveredPeer)
	onTrace  func(string)
	registry *libp2pPeerRegistry
}

func (n *libp2pMDNSNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.h.ID() {
		return // ignore self
	}

	ctx, cancel := context.WithTimeout(context.Background(), p2pLibP2PHandshakeTimeout)
	defer cancel()

	if err := n.h.Connect(ctx, pi); err != nil {
		if n.onTrace != nil {
			n.onTrace(fmt.Sprintf("libp2p connect falhou peer=%s: %v", pi.ID, err))
		}
		return
	}

	s, err := n.h.NewStream(ctx, pi.ID, p2pLibP2PProtocolID)
	if err != nil {
		if n.onTrace != nil {
			n.onTrace(fmt.Sprintf("libp2p stream falhou peer=%s: %v", pi.ID, err))
		}
		return
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(p2pLibP2PHandshakeTimeout))

	// Initiator sends first, then reads response.
	mine := p2pLibP2PPeerInfo{AgentID: n.self.AgentID, HTTPPort: n.self.Port}
	if err := json.NewEncoder(s).Encode(mine); err != nil {
		return
	}
	var remote p2pLibP2PPeerInfo
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&remote); err != nil {
		return
	}

	if strings.TrimSpace(remote.AgentID) == "" || remote.HTTPPort <= 0 {
		return
	}

	// Prefer a non-loopback address from the peer's advertised addrs.
	remoteAddr := ""
	for _, addr := range pi.Addrs {
		ip := extractIPFromMultiaddr(addr.String())
		if ip != "" && ip != "127.0.0.1" && ip != "::1" {
			remoteAddr = ip
			break
		}
	}
	if remoteAddr == "" && len(pi.Addrs) > 0 {
		remoteAddr = extractIPFromMultiaddr(pi.Addrs[0].String())
	}
	if remoteAddr == "" {
		if n.onTrace != nil {
			n.onTrace(fmt.Sprintf("libp2p peer sem endereço IP: peerID=%s", pi.ID))
		}
		return
	}

	if n.onTrace != nil {
		n.onTrace(fmt.Sprintf("libp2p peer encontrado: agentId=%s addr=%s:%d",
			remote.AgentID, remoteAddr, remote.HTTPPort))
	}
	// Registrar mapeamento agentID → peer.ID para uso em streams de transferência.
	if n.registry != nil {
		n.registry.Register(remote.AgentID, pi.ID)
	}
	n.onPeer(p2pDiscoveredPeer{
		AgentID:      strings.TrimSpace(remote.AgentID),
		Host:         remoteAddr,
		Address:      remoteAddr,
		Port:         remote.HTTPPort,
		Source:       p2pDiscoveryLibP2P,
		ConnectedVia: p2pDiscoveryLibP2P,
	})
}

// p2pMultiProvider runs two discovery providers concurrently (used for hybrid mode).
// Peers emitted by either provider are forwarded to the coordinator with their
// respective Source field set, so de-dup by agentID happens naturally in upsertPeer.
type p2pMultiProvider struct {
	providers []p2pDiscoveryProvider
}

func (m *p2pMultiProvider) Name() string { return "multi" }

func (m *p2pMultiProvider) Start(
	ctx context.Context,
	self p2pSelfEndpoint,
	onPeer func(p2pDiscoveredPeer),
	onTrace func(string),
) error {
	started := make([]p2pDiscoveryProvider, 0, len(m.providers))
	cancelCtx, cancel := context.WithCancel(ctx)
	for _, p := range m.providers {
		if err := p.Start(cancelCtx, self, onPeer, onTrace); err != nil {
			// Cancelar o contexto dos providers já iniciados para liberar
			// goroutines e sockets antes de retornar o erro.
			cancel()
			_ = started // já serão encerrados via cancelCtx
			return fmt.Errorf("provider %s falhou: %w", p.Name(), err)
		}
		started = append(started, p)
	}
	// Todos iniciados com sucesso: propagar cancelamento do parent.
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return nil
}

// pickDiscoveryProvider returns the correct provider based on P2PConfig.
// This is the single place where transport selection is resolved.
// pickDiscoveryProvider returns the correct provider based on P2PConfig.
// coord e transfer são injetados nos providers libp2p para que o host
// registre os protocolos de transporte de artifacts.
func pickDiscoveryProvider(cfg P2PConfig, coord *p2pCoordinator, transfer *p2pTransferServer) p2pDiscoveryProvider {
	registry := newLibp2pPeerRegistry()
	switch cfg.P2PMode {
	case P2PModeLibp2pOnly:
		return &p2pLibP2PProvider{
			bootstrapPeers: cfg.BootstrapConfig.BootstrapPeers,
			coord:          coord,
			transfer:       transfer,
			registry:       registry,
		}
	case P2PModeHybrid:
		return &p2pMultiProvider{providers: []p2pDiscoveryProvider{
			&p2pMDNSProvider{},
			&p2pLibP2PProvider{
				bootstrapPeers: cfg.BootstrapConfig.BootstrapPeers,
				coord:          coord,
				transfer:       transfer,
				registry:       registry,
			},
		}}
	default: // legacy
		if cfg.DiscoveryMode == p2pDiscoveryUDP {
			return &p2pUDPProvider{}
		}
		return &p2pMDNSProvider{}
	}
}

// extractIPFromMultiaddr extracts the IP address from a libp2p multiaddr string.
// E.g. "/ip4/192.168.1.5/tcp/41080" → "192.168.1.5".
func extractIPFromMultiaddr(ma string) string {
	parts := strings.Split(ma, "/")
	for i, part := range parts {
		if (part == "ip4" || part == "ip6") && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}
