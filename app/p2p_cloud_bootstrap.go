package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const (
	p2pCloudBootstrapEndpoint  = "/api/agent-auth/me/p2p/bootstrap"
	p2pCloudBootstrapTimeout   = 15 * time.Second
	p2pCloudBootstrapConnectTO = 10 * time.Second
)

// p2pCloudBootstrapRequest é o payload enviado ao servidor.
type p2pCloudBootstrapRequest struct {
	AgentID string   `json:"agentId"`
	PeerID  string   `json:"peerId"`
	Addrs   []string `json:"addrs"`
}

// p2pCloudBootstrapPeer é um peer retornado pelo servidor.
type p2pCloudBootstrapPeer struct {
	PeerID string   `json:"peerId"`
	Addrs  []string `json:"addrs"`
	Port   int      `json:"port"`
}

// p2pCloudBootstrapResponse é a resposta do servidor.
type p2pCloudBootstrapResponse struct {
	Peers []p2pCloudBootstrapPeer `json:"peers"`
}

// runCloudBootstrap realiza um ciclo de cloud bootstrap:
//  1. Carrega peers cacheados e tenta conectar imediatamente
//  2. Registra o próprio peer no servidor e obtém até 3 peers remotos
//  3. Tenta conectar nos peers retornados, atualiza o cache (sucesso=upsert, falha=remove)
func (c *p2pCoordinator) runCloudBootstrap(ctx context.Context) {
	cfg := c.app.GetP2PConfig()
	if !cfg.BootstrapConfig.CloudBootstrapEnabled {
		return
	}

	debugCfg := c.app.GetDebugConfig()
	agentID := strings.TrimSpace(debugCfg.AgentID)
	authToken := strings.TrimSpace(debugCfg.AuthToken)
	apiScheme := strings.TrimSpace(debugCfg.ApiScheme)
	apiServer := strings.TrimSpace(debugCfg.ApiServer)

	if agentID == "" || authToken == "" || apiScheme == "" || apiServer == "" {
		c.app.logs.append("[p2p][cloud-bootstrap] configuracao incompleta: agentId, authToken, apiScheme ou apiServer ausentes")
		return
	}

	h, _ := c.libp2pHostAndRegistry()
	if h == nil {
		c.app.logs.append("[p2p][cloud-bootstrap] host libp2p nao disponivel")
		return
	}

	// Carregar cache local e tentar conexões imediatas (antes de chamar a API).
	cachedPeers, _ := loadP2PPeerCache()
	cachedPeers = c.connectCachedPeers(ctx, h, cachedPeers)

	// Coletar IPs IPv4 roteáveis do próprio host.
	selfAddrs := extractRoutableIPv4Addrs(h)
	selfPeerID := h.ID().String()

	payload := p2pCloudBootstrapRequest{
		AgentID: agentID,
		PeerID:  selfPeerID,
		Addrs:   selfAddrs,
	}

	resp, err := c.callCloudBootstrapAPI(ctx, apiScheme, apiServer, authToken, payload)
	if err != nil {
		c.app.logs.append("[p2p][cloud-bootstrap] erro ao chamar API: " + err.Error())
		// Mesmo com falha na API, persistir o estado atual do cache (conexões locais já limpas).
		_ = saveP2PPeerCache(cachedPeers)
		return
	}

	c.app.logs.append(fmt.Sprintf("[p2p][cloud-bootstrap] API retornou %d peer(s)", len(resp.Peers)))

	// Conectar nos peers retornados pela API e atualizar cache.
	for _, rp := range resp.Peers {
		rp.PeerID = strings.TrimSpace(rp.PeerID)
		if rp.PeerID == "" || rp.Port <= 0 || len(rp.Addrs) == 0 {
			continue
		}

		addrInfo, err := buildAddrInfo(rp.PeerID, rp.Addrs, rp.Port)
		if err != nil {
			c.app.logs.append(fmt.Sprintf("[p2p][cloud-bootstrap] peer ignorado (addr invalido) peerId=%s: %v", rp.PeerID, err))
			continue
		}

		connCtx, cancel := context.WithTimeout(ctx, p2pCloudBootstrapConnectTO)
		err = h.Connect(connCtx, addrInfo)
		cancel()

		if err != nil {
			c.app.logs.append(fmt.Sprintf("[p2p][cloud-bootstrap] connect falhou peerId=%s: %v", rp.PeerID, err))
			cachedPeers = removeP2PPeerCacheEntry(cachedPeers, rp.PeerID)
			continue
		}

		c.app.logs.append(fmt.Sprintf("[p2p][cloud-bootstrap] conectado peerId=%s addrs=%v", rp.PeerID, rp.Addrs))
		cachedPeers = upsertP2PPeerCacheEntry(cachedPeers, p2pCachedPeer{
			PeerID:     rp.PeerID,
			Addrs:      rp.Addrs,
			Port:       rp.Port,
			LastSeenAt: time.Now().UTC(),
		})
	}

	if err := saveP2PPeerCache(cachedPeers); err != nil {
		c.app.logs.append("[p2p][cloud-bootstrap] erro ao salvar cache: " + err.Error())
	}
}

// connectCachedPeers tenta conectar nos peers do cache local.
// Remove entradas inválidas e retorna o cache atualizado.
func (c *p2pCoordinator) connectCachedPeers(ctx context.Context, h interface {
	Connect(context.Context, peer.AddrInfo) error
}, cached []p2pCachedPeer) []p2pCachedPeer {
	if len(cached) == 0 {
		return cached
	}
	valid := cached[:0]
	for _, cp := range cached {
		if cp.PeerID == "" || cp.Port <= 0 || len(cp.Addrs) == 0 {
			continue
		}
		addrInfo, err := buildAddrInfo(cp.PeerID, cp.Addrs, cp.Port)
		if err != nil {
			continue
		}
		connCtx, cancel := context.WithTimeout(ctx, p2pCloudBootstrapConnectTO)
		err = h.Connect(connCtx, addrInfo)
		cancel()
		if err != nil {
			c.app.logs.append(fmt.Sprintf("[p2p][cloud-bootstrap] cache: connect falhou peerId=%s (removendo): %v", cp.PeerID, err))
			// Peer inválido — não adiciona ao slice válido (remoção implícita).
			continue
		}
		c.app.logs.append(fmt.Sprintf("[p2p][cloud-bootstrap] cache: reconectado peerId=%s", cp.PeerID))
		cp.LastSeenAt = time.Now().UTC()
		valid = append(valid, cp)
	}
	return valid
}

// callCloudBootstrapAPI faz POST no endpoint de bootstrap e retorna a resposta parseada.
func (c *p2pCoordinator) callCloudBootstrapAPI(ctx context.Context, scheme, server, token string, payload p2pCloudBootstrapRequest) (*p2pCloudBootstrapResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	url := scheme + "://" + server + p2pCloudBootstrapEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("criar request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: p2pCloudBootstrapTimeout}
	httpResp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("servidor retornou status %d", httpResp.StatusCode)
	}

	var resp p2pCloudBootstrapResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode resposta: %w", err)
	}
	return &resp, nil
}

// buildAddrInfo constrói um peer.AddrInfo a partir de IPs, porta e peerID string.
// Gera entradas TCP e QUIC/v1 para cada IP válido.
func buildAddrInfo(peerIDStr string, addrs []string, port int) (peer.AddrInfo, error) {
	pid, err := peer.Decode(peerIDStr)
	if err != nil {
		return peer.AddrInfo{}, fmt.Errorf("peerID invalido: %w", err)
	}

	var mas []multiaddr.Multiaddr
	for _, addr := range addrs {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		ip := net.ParseIP(addr)
		if ip == nil || ip.To4() == nil {
			continue // somente IPv4
		}
		// TCP
		if maTCP, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%d", ip.To4().String(), port)); err == nil {
			mas = append(mas, maTCP)
		}
		// QUIC/v1
		if maQUIC, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/%s/udp/%d/quic-v1", ip.To4().String(), port)); err == nil {
			mas = append(mas, maQUIC)
		}
	}

	if len(mas) == 0 {
		return peer.AddrInfo{}, fmt.Errorf("nenhum multiaddr IPv4 valido gerado para %s", peerIDStr)
	}
	return peer.AddrInfo{ID: pid, Addrs: mas}, nil
}

// extractRoutableIPv4Addrs retorna os IPs IPv4 roteáveis do host libp2p
// (sem loopback, sem link-local).
func extractRoutableIPv4Addrs(h interface{ Addrs() []multiaddr.Multiaddr }) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, ma := range h.Addrs() {
		val, err := ma.ValueForProtocol(multiaddr.P_IP4)
		if err != nil {
			continue
		}
		ip := net.ParseIP(val)
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if _, ok := seen[val]; !ok {
			seen[val] = struct{}{}
			out = append(out, val)
		}
	}
	return out
}
