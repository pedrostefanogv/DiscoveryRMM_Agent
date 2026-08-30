package p2p

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

// connectPeerIfNotConnected conecta ao peer apenas se ainda não houver conexão
// ativa. Quando já conectado, apenas garante que os endereços estão no peerstore
// e retorna nil sem abrir nova conexão.
//
// Motivação: os loops de descoberta (gossip 45s, lan-probe 2min, mDNS, NATS)
// chamam h.Connect() repetidamente. Se a conexão caiu entre ticks, os DOIS
// agents podem discar um para o outro SIMULTANEAMENTE ao reagir ao mesmo tick.
// O Swarm do go-libp2p deduplica conexões redundantes fechando a "perdedora"
// com Close() gracioso — e antes de fechar, reseta TODOS os streams ativos
// dela. Isso mata transferências em andamento com o erro característico
// "connection closed (remote): Application error 0x0" em lotes (todos os
// chunks paralelos da conexão perdedora falham juntos).
//
// O jitter aleatório (0..jitterMax) dessincroniza os dois lados: quem discar
// primeiro encontra o peer já conectado e reusa; o segundo nem disca.
func connectPeerIfNotConnected(ctx context.Context, h host.Host, pi peer.AddrInfo, jitterMax time.Duration) error {
	if h == nil {
		return fmt.Errorf("host libp2p indisponível")
	}
	// Guard barato: já conectado → apenas atualiza TTL dos endereços.
	if h.Network().Connectedness(pi.ID) == network.Connected {
		if len(pi.Addrs) > 0 {
			h.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstore.AddressTTL)
		}
		return nil
	}
	if jitterMax > 0 {
		// Jitter dessincroniza dials simultâneos dos dois lados da conexão.
		time.Sleep(time.Duration(rand.Int63n(int64(jitterMax))))
		// Re-checa após o jitter: o outro lado pode ter conectado nesse meio-tempo.
		if h.Network().Connectedness(pi.ID) == network.Connected {
			if len(pi.Addrs) > 0 {
				h.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstore.AddressTTL)
			}
			return nil
		}
	}
	return h.Connect(ctx, pi)
}

// discoveryDialJitter é o jitter máximo aplicado antes de re-dial em loops de
// descoberta. 2s é suficiente para dessincronizar dois agents que reagem ao
// mesmo evento de rede, sem atrasar perceptivelmente a descoberta.
const discoveryDialJitter = 2 * time.Second
