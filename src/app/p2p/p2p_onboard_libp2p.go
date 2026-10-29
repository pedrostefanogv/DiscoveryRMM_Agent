package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
)

// p2pOnboardStreamProtocol é o stream de onboarding peer-to-peer.
//
// Existe porque o listener HTTP que serve /p2p/config/onboard é loopback-only
// (hardening C4) — portanto INALCANÇÁVEL por outro agente da LAN. O stream
// libp2p transporta a mesma oferta assinada por um canal já autenticado pelo
// peer.ID do libp2p (Noise/TLS), preservando o C4 e destravando o zero-touch
// (achado Z1, 2026-10-29).
const p2pOnboardStreamProtocol = protocol.ID("/discovery-onboard/1.0.0")

// p2pOnboardStreamTimeout cobre a ida+volta incluindo a chamada da API para
// emissão do provisioning token no lado do provisionador.
// Precisa ser MAIOR que o orçamento da chamada à API (p2pOnboardAPI Budget=20s
// abaixo): igualá-los fazia a resposta estourar o deadline com o token de uso
// único já emitido/consumido (bug 2026-10-29).
const p2pOnboardStreamTimeout = 45 * time.Second

// p2pOnboardAPIBudget limita a chamada de emissão do token na API.
const p2pOnboardAPIBudget = 20 * time.Second

// Limites do canal de onboarding (hardening do substituto libp2p do HTTP
// loopback-only — o achado C4 continua valendo para o listener HTTP):
//
//   - p2pOnboardOfferCooldown: por peer, evita que o MESMO peer consuma
//     provisioning tokens de uso único em loop (DeployTokenService, MaxUses=1);
//   - p2pOnboardGlobalWindow/GlobalMax: limite GLOBAL, porque peer.ID é gratuito
//     — um atacante pode gerar identidades novas e furar o cooldown por peer.
const (
	// 20s: precisa ser MENOR que o primeiro retry do backoff do requisitante
	// (15–30 s), senão o retry "rápido" sempre recebia 429 (bug 2026-10-29).
	p2pOnboardOfferCooldown = 20 * time.Second
	p2pOnboardGlobalWindow  = 10 * time.Minute
	p2pOnboardGlobalMax     = 5
)

var (
	p2pOnboardOfferMu       sync.Mutex
	p2pOnboardOfferLastSeen = make(map[peer.ID]time.Time)
	p2pOnboardOfferGlobal   []time.Time
)

// onboardingOfferAllowed diz se o peer pode RECEBER uma oferta agora, SEM
// consumir a janela. A marcação só acontece depois de a oferta ser emitida com
// sucesso (um 503 da API não pode bloquear o peer honesto por 60 s).
func onboardingOfferAllowed(peerID peer.ID, now time.Time) bool {
	p2pOnboardOfferMu.Lock()
	defer p2pOnboardOfferMu.Unlock()
	if last, ok := p2pOnboardOfferLastSeen[peerID]; ok && now.Sub(last) < p2pOnboardOfferCooldown {
		return false
	}
	// Janela global: descarta entradas antigas e limita o total recente.
	kept := p2pOnboardOfferGlobal[:0]
	for _, ts := range p2pOnboardOfferGlobal {
		if now.Sub(ts) < p2pOnboardGlobalWindow {
			kept = append(kept, ts)
		}
	}
	p2pOnboardOfferGlobal = kept
	if len(p2pOnboardOfferGlobal) >= p2pOnboardGlobalMax {
		return false
	}
	if len(p2pOnboardOfferLastSeen) > 512 {
		for id, seen := range p2pOnboardOfferLastSeen {
			if now.Sub(seen) >= p2pOnboardOfferCooldown {
				delete(p2pOnboardOfferLastSeen, id)
			}
		}
	}
	return true
}

// markOnboardingOffer consome a janela (chamar SOMENTE após emitir a oferta).
func markOnboardingOffer(peerID peer.ID, now time.Time) {
	p2pOnboardOfferMu.Lock()
	p2pOnboardOfferLastSeen[peerID] = now
	p2pOnboardOfferGlobal = append(p2pOnboardOfferGlobal, now)
	p2pOnboardOfferMu.Unlock()
}

// allowOnboardingOffer mantém a assinatura antiga para os testes: checa E marca.
func allowOnboardingOffer(peerID peer.ID, now time.Time) bool {
	if !onboardingOfferAllowed(peerID, now) {
		return false
	}
	markOnboardingOffer(peerID, now)
	return true
}

// isPrivateOrLoopback reports whether o endereço remoto pertence a uma rede
// privada/local: RFC1918, loopback, link-local, CGNAT (100.64/10 — Tailscale e
// muitas VPNs corporativas) e IPv6 ULA (fc00::/7) / link-local / loopback.
// Endereços públicos são recusados: a oferta de zero-touch só sai na LAN.
func isPrivateOrLoopback(ma multiaddr.Multiaddr) bool {
	if ma == nil {
		return false
	}
	var ip net.IP
	if raw, err := ma.ValueForProtocol(multiaddr.P_IP4); err == nil && raw != "" {
		ip = net.ParseIP(raw)
	} else if raw6, err6 := ma.ValueForProtocol(multiaddr.P_IP6); err6 == nil && raw6 != "" {
		ip = net.ParseIP(raw6)
	}
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 100.64.0.0/10 (CGNAT: Tailscale, VPNs corporativas).
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
		return false
	}
	// fc00::/7 = ULA (equivalente ao RFC1918 em IPv6).
	return len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc
}

// multiaddrIP extrai o IP (v4 ou v6) de um multiaddr; nil quando não há IP.
func multiaddrIP(ma multiaddr.Multiaddr) net.IP {
	if ma == nil {
		return nil
	}
	if raw, err := ma.ValueForProtocol(multiaddr.P_IP4); err == nil && raw != "" {
		return net.ParseIP(raw)
	}
	if raw6, err6 := ma.ValueForProtocol(multiaddr.P_IP6); err6 == nil && raw6 != "" {
		return net.ParseIP(raw6)
	}
	return nil
}

// isSameSubnetAsLocal informa se o IP pertence a alguma sub-rede configurada em
// uma interface local. A oferta de zero-touch carrega uma deploy key de uso
// único do nosso site: só deve sair para a mesma sub-rede.
func isSameSubnetAsLocal(ip net.IP) bool {
	if ip == nil {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true // sem como avaliar: não bloqueia (o gate de IP privado já vale)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet == nil {
			continue
		}
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// currentClientID devolve o clientId deste agente (vazio se não configurado).
func currentClientID(transfer *TransferServer) string {
	if transfer == nil || transfer.deps == nil {
		return ""
	}
	return normalizeClientID(strings.TrimSpace(transfer.deps.GetAgentConfiguration().ClientID))
}

type p2pOnboardStreamRequest struct {
	AgentID  string `json:"agentId,omitempty"`
	ClientID string `json:"clientId,omitempty"`
}

type p2pOnboardStreamResponse struct {
	Offer *P2POnboardingRequest `json:"offer,omitempty"`
	Error string                `json:"error,omitempty"`
}

// RegisterOnboardingProtocols registra o handler do stream de onboarding no
// host libp2p. Chamado por p2pLibP2PProvider.Start.
func RegisterOnboardingProtocols(h host.Host, transfer *TransferServer) {
	if h == nil || transfer == nil {
		return
	}
	h.SetStreamHandler(p2pOnboardStreamProtocol, func(s network.Stream) {
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(p2pOnboardStreamTimeout))

		// Request é opcional (compatibilidade): só serve para auditoria.
		var req p2pOnboardStreamRequest
		if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil && err != io.EOF {
			req = p2pOnboardStreamRequest{}
		}

		remotePeer := s.Conn().RemotePeer()
		now := time.Now()
		if !isPrivateOrLoopback(s.Conn().RemoteMultiaddr()) {
			_ = json.NewEncoder(s).Encode(p2pOnboardStreamResponse{Error: "403: onboarding restrito à rede local"})
			return
		}
		// Segunda trava (defesa em profundidade): aceitar só peer da MESMA
		// sub-rede de uma interface local — a oferta carrega uma deploy key de
		// uso único do nosso site, então não deve sair para outra sub-rede.
		if !isSameSubnetAsLocal(multiaddrIP(s.Conn().RemoteMultiaddr())) {
			_ = json.NewEncoder(s).Encode(p2pOnboardStreamResponse{Error: "403: onboarding restrito à mesma sub-rede"})
			if transfer.deps != nil {
				transfer.deps.Log(fmt.Sprintf("[zero-touch][libp2p] oferta negada (sub-rede diferente) peer=%s", remotePeer))
			}
			return
		}
		// Guarda multi-tenant: se o requisitante DECLARA um clientId diferente
		// do nosso, é agente de outro cliente — não recebe oferta do nosso site.
		if declared := normalizeClientID(strings.TrimSpace(req.ClientID)); declared != "" {
			if own := currentClientID(transfer); own != "" && !strings.EqualFold(declared, own) {
				_ = json.NewEncoder(s).Encode(p2pOnboardStreamResponse{Error: "403: clientId incompatível"})
				if transfer.deps != nil {
					transfer.deps.Log(fmt.Sprintf("[zero-touch][libp2p] oferta negada (clientId %s != %s) peer=%s", declared, own, remotePeer))
				}
				return
			}
		}
		// Checa a janela SEM consumir: um 503 da API não pode bloquear o peer
		// honesto por 60 s (bug 2026-10-29).
		if !onboardingOfferAllowed(remotePeer, now) {
			_ = json.NewEncoder(s).Encode(p2pOnboardStreamResponse{Error: "429: aguarde antes de pedir nova oferta"})
			return
		}

		// A troca de token com a API pode demorar; usa deadline próprio para não
		// deixar goroutine pendurada depois que o stream já expirou.
		offerCtx, cancelOffer := context.WithTimeout(context.Background(), p2pOnboardAPIBudget)
		offer, status, err := transfer.buildOnboardingOffer(offerCtx)
		cancelOffer()
		resp := p2pOnboardStreamResponse{}
		if err != nil {
			resp.Error = fmt.Sprintf("%d: %v", status, err)
			if transfer.deps != nil {
				transfer.deps.Log(fmt.Sprintf("[zero-touch][libp2p] oferta negada ao peer=%s requester=%s: %s",
					s.Conn().RemotePeer(), strings.TrimSpace(req.AgentID), resp.Error))
			}
		} else {
			// Só consome a janela quando a oferta é realmente emitida.
			markOnboardingOffer(remotePeer, now)
			offerCopy := offer
			resp.Offer = &offerCopy
			if transfer.deps != nil {
				transfer.deps.Log(fmt.Sprintf("[zero-touch][libp2p] oferta entregue ao peer=%s requester=%s",
					s.Conn().RemotePeer(), strings.TrimSpace(req.AgentID)))
			}
		}
		_ = json.NewEncoder(s).Encode(resp)
	})
}

// FetchOnboardingOfferFromPeers pede a oferta de onboarding a cada peer
// conhecido via libp2p (caminho alcançável na LAN) e devolve a primeira válida.
//
// Usado pelo agente NÃO configurado. Retorna erro agregado quando nenhum peer
// entrega oferta, para que o chamador registre a causa (hoje o HTTP silenciava
// ECONNREFUSED e o operador não via nada — achado Z9).
func (c *Coordinator) FetchOnboardingOfferFromPeers(ctx context.Context) (P2POnboardingRequest, error) {
	if c == nil || c.deps == nil {
		return P2POnboardingRequest{}, fmt.Errorf("coordinator P2P indisponível")
	}
	h, registry := c.libp2pHostAndRegistry()
	if h == nil || registry == nil {
		return P2POnboardingRequest{}, fmt.Errorf("host libp2p indisponível para onboarding")
	}
	peers := c.GetPeers()
	if len(peers) == 0 {
		return P2POnboardingRequest{}, fmt.Errorf("nenhum peer conhecido na rede")
	}

	selfAgentID := strings.TrimSpace(c.deps.GetDebugConfig().AgentID)
	selfClientID := normalizeClientID(strings.TrimSpace(c.deps.GetAgentConfiguration().ClientID))

	var lastErr error
	tried := 0
	for _, p := range peers {
		peerID, ok := registry.Lookup(p.AgentID)
		if !ok {
			continue
		}
		tried++
		offer, err := libp2pFetchOnboardingOffer(ctx, h, peerID, selfAgentID, selfClientID)
		if err != nil {
			lastErr = err
			c.deps.Log(fmt.Sprintf("[zero-touch][libp2p] peer=%s nao entregou oferta: %v", strings.TrimSpace(p.AgentID), err))
			continue
		}
		c.deps.Log(fmt.Sprintf("[zero-touch][libp2p] oferta recebida de peer=%s", strings.TrimSpace(p.AgentID)))
		return offer, nil
	}
	if tried == 0 {
		return P2POnboardingRequest{}, fmt.Errorf("nenhum peer com peerID libp2p registrado (descoberta incompleta)")
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nenhum peer forneceu configuração válida")
	}
	return P2POnboardingRequest{}, fmt.Errorf("nenhum peer forneceu configuração válida: %w", lastErr)
}

// libp2pFetchOnboardingOffer abre o stream de onboarding e devolve a oferta.
func libp2pFetchOnboardingOffer(ctx context.Context, h host.Host, peerID peer.ID, agentID, clientID string) (P2POnboardingRequest, error) {
	streamCtx, cancel := context.WithTimeout(ctx, p2pOnboardStreamTimeout)
	defer cancel()

	s, err := h.NewStream(streamCtx, peerID, p2pOnboardStreamProtocol)
	if err != nil {
		return P2POnboardingRequest{}, fmt.Errorf("stream onboard: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(p2pOnboardStreamTimeout))

	if err := json.NewEncoder(s).Encode(p2pOnboardStreamRequest{
		AgentID:  strings.TrimSpace(agentID),
		ClientID: normalizeClientID(clientID),
	}); err != nil {
		return P2POnboardingRequest{}, fmt.Errorf("encode onboard req: %w", err)
	}

	var resp p2pOnboardStreamResponse
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&resp); err != nil {
		return P2POnboardingRequest{}, fmt.Errorf("decode onboard resp: %w", err)
	}
	if strings.TrimSpace(resp.Error) != "" {
		return P2POnboardingRequest{}, fmt.Errorf("peer: %s", strings.TrimSpace(resp.Error))
	}
	if resp.Offer == nil || strings.TrimSpace(resp.Offer.DeployKey) == "" {
		return P2POnboardingRequest{}, fmt.Errorf("peer devolveu oferta vazia")
	}
	return *resp.Offer, nil
}
