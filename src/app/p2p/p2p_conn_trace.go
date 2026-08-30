package p2p

import (
	"fmt"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/multiformats/go-multiaddr"
)

// p2pConnTraceNotifiee registra eventos de conexão/desconexão libp2p para
// diagnóstico. O Notifiee do go-libp2p NÃO expõe o motivo do fechamento, mas
// registra QUANDO e QUAL conexão (transporte + endereço remoto) caiu —
// suficiente para correlacionar quedas com ticks de descoberta e com
// transferências ativas em produção.
//
// Loga apenas desconexões (Connected é ruído em malhas pequenas); desconexões
// durante transferência ativa são marcadas com um aviso explícito.
type p2pConnTraceNotifiee struct {
	mu sync.Mutex

	// activeTransfers rastreia streams abertos por protocolo para saber se
	// havia transferência em andamento quando a conexão caiu.
	activeStreams map[string]int // peerID -> contagem de streams abertos
	logf          func(string)
}

func newP2PConnTraceNotifiee(logf func(string)) *p2pConnTraceNotifiee {
	return &p2pConnTraceNotifiee{
		activeStreams: make(map[string]int),
		logf:          logf,
	}
}

func (n *p2pConnTraceNotifiee) Connected(_ network.Network, _ network.Conn) {}

func (n *p2pConnTraceNotifiee) Disconnected(_ network.Network, conn network.Conn) {
	if n == nil || n.logf == nil {
		return
	}
	peerID := conn.RemotePeer().String()
	transport := transportFromMultiaddr(conn.RemoteMultiaddr().String())
	remote := conn.RemoteMultiaddr().String()

	n.mu.Lock()
	streams := n.activeStreams[peerID]
	n.mu.Unlock()

	if streams > 0 {
		n.logf(fmt.Sprintf("[p2p][conn] DESCONECTADO com %d stream(s) ativo(s) peer=%s transporte=%s remoto=%s — transferências em andamento foram abortadas",
			streams, shortPeerID(peerID), transport, remote))
	} else {
		n.logf(fmt.Sprintf("[p2p][conn] desconectado peer=%s transporte=%s remoto=%s",
			shortPeerID(peerID), transport, remote))
	}
}

func (n *p2pConnTraceNotifiee) OpenedStream(_ network.Network, stream network.Stream) {
	n.mu.Lock()
	n.activeStreams[stream.Conn().RemotePeer().String()]++
	n.mu.Unlock()
}

func (n *p2pConnTraceNotifiee) ClosedStream(_ network.Network, stream network.Stream) {
	n.mu.Lock()
	key := stream.Conn().RemotePeer().String()
	if n.activeStreams[key] > 0 {
		n.activeStreams[key]--
	}
	if n.activeStreams[key] == 0 {
		delete(n.activeStreams, key)
	}
	n.mu.Unlock()
}

func (n *p2pConnTraceNotifiee) Listen(_ network.Network, _ multiaddr.Multiaddr)      {}
func (n *p2pConnTraceNotifiee) ListenClose(_ network.Network, _ multiaddr.Multiaddr) {}

// shortPeerID abrevia um peer.ID para logs.
func shortPeerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// transportFromMultiaddr extrai o transporte (quic/tcp/ws) de um multiaddr.
func transportFromMultiaddr(addr string) string {
	for _, proto := range []string{"quic-v1", "quic", "tcp", "ws", "wss"} {
		if strings.Contains(addr, proto) {
			return proto
		}
	}
	return "desconhecido"
}
