package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"

	"discovery/app/netutil"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// errCloudBootstrapBusy indica que outro ciclo já está em andamento (loop
// automático × botão manual). NÃO é falha de rede: não deve gerar backoff.
var errCloudBootstrapBusy = errors.New("cloud bootstrap já em execução")

const (
	p2pCloudBootstrapEndpoint  = "/api/v1/agent-auth/me/p2p/bootstrap"
	p2pCloudBootstrapTimeout   = 15 * time.Second
	p2pCloudBootstrapConnectTO = 10 * time.Second

	// p2pCloudBootstrapInterval é o intervalo entre ciclos de cloud bootstrap
	// (opção A, decisão 2026-11-01). É PULL: cada agent puxa a lista de peers
	// do servidor; o servidor NÃO publica nada por heartbeat. Jitter de ±15%
	// evita sincronização de fase entre agents do mesmo cliente.
	p2pCloudBootstrapInterval = 60 * time.Minute
	// p2pCloudBootstrapStartupJitter limita o atraso aleatório da primeira
	// execução após o startup do coordinator (0..jitter).
	p2pCloudBootstrapStartupJitter = 60 * time.Second
	// p2pCloudBootstrapDisabledRecheck é o re-check quando o toggle está
	// desligado localmente (barato: só lê a config local).
	p2pCloudBootstrapDisabledRecheck = 5 * time.Minute
	// p2pCloudBootstrapBackoffMax é o teto do backoff exponencial em falhas
	// (60min → 2h → 4h → 6h…).
	p2pCloudBootstrapBackoffMax = 6 * time.Hour
)

// CloudBootstrapTimeout é o timeout da chamada de cloud bootstrap, exposto
// para o package app.
const CloudBootstrapTimeout = p2pCloudBootstrapTimeout

// p2pCloudBootstrapRequest é o payload enviado ao servidor.
type p2pCloudBootstrapRequest struct {
	AgentID  string   `json:"agentId"`
	ClientID string   `json:"clientId,omitempty"`
	PeerID   string   `json:"peerId"`
	Addrs    []string `json:"addrs"`
	Port     int      `json:"port"`
}

// p2pCloudBootstrapPeer é um peer retornado pelo servidor.
type p2pCloudBootstrapPeer struct {
	PeerID  string   `json:"peerId"`
	AgentID string   `json:"agentId,omitempty"`
	Addrs   []string `json:"addrs"`
	Port    int      `json:"port"`
}

// p2pCloudBootstrapResponse é a resposta do servidor.
type p2pCloudBootstrapResponse struct {
	Peers []p2pCloudBootstrapPeer `json:"peers"`
}

// runCloudBootstrap realiza um ciclo de cloud bootstrap:
//  1. Carrega peers cacheados e tenta conectar imediatamente
//  2. Registra o próprio peer no servidor e obtém até 3 peers remotos
//  3. Tenta conectar nos peers retornados, atualiza o cache (sucesso=upsert, falha=remove)
//
// Retorna a quantidade de peers retornada pela API.
func (c *Coordinator) RunCloudBootstrap(ctx context.Context) (int, error) {
	// Um ciclo por vez: o loop automático (StartCloudBootstrapLoop) e o botão
	// manual da UI podem competir; o segundo chamante desiste sem quebrar o
	// estado do cache de peers.
	if !c.cloudBootstrapRunMu.TryLock() {
		return 0, errCloudBootstrapBusy
	}
	defer c.cloudBootstrapRunMu.Unlock()

	cfg := c.deps.GetP2PConfig()
	if !cfg.BootstrapConfig.CloudBootstrapEnabled {
		return 0, fmt.Errorf("cloud bootstrap P2P desabilitado")
	}

	debugCfg := c.deps.GetDebugConfig()
	agentID := strings.TrimSpace(debugCfg.AgentID)
	authToken := strings.TrimSpace(debugCfg.AuthToken)
	apiScheme := debugCfg.APIScheme()
	apiServer := strings.TrimSpace(debugCfg.ApiServer)

	if agentID == "" || authToken == "" || apiServer == "" {
		err := fmt.Errorf("configuração incompleta: agentId, authToken ou apiServer ausentes")
		c.deps.Log("[p2p][cloud-bootstrap] " + err.Error())
		return 0, err
	}

	h, _ := c.libp2pHostAndRegistry()
	if h == nil {
		err := fmt.Errorf("host libp2p não disponível")
		c.deps.Log("[p2p][cloud-bootstrap] " + err.Error())
		return 0, err
	}

	// Carregar cache local e tentar conexões imediatas (antes de chamar a API).
	cachedPeers, _ := c.loadP2PPeerCache()
	cachedPeers = c.connectCachedPeers(ctx, h, cachedPeers)

	// Coletar IPs IPv4 roteáveis e a PORTA LIBP2P do próprio host.
	// BUGFIX D8 (2026-11-01): antes era anunciada a porta do transferServer
	// HTTP (loopback), mas buildAddrInfo diala TCP/QUIC na porta do LISTENER
	// libp2p — dial sempre na porta errada. GetP2PAddressingInfo extrai a
	// porta real dos multiaddrs de escuta (mesma fonte do heartbeat).
	selfPeerID, selfAddrs, selfPort := c.GetP2PAddressingInfo()

	localClientID := normalizeClientID(strings.TrimSpace(c.deps.GetAgentConfiguration().ClientID))

	payload := p2pCloudBootstrapRequest{
		AgentID:  agentID,
		ClientID: localClientID,
		PeerID:   selfPeerID,
		Addrs:    selfAddrs,
		Port:     selfPort,
	}

	resp, err := c.callCloudBootstrapAPI(ctx, apiScheme, apiServer, authToken, agentID, payload)
	if err != nil {
		c.deps.Log("[p2p][cloud-bootstrap] erro ao chamar API: " + err.Error())
		// Mesmo com falha na API, persistir o estado atual do cache (conexões locais já limpas).
		_ = c.saveP2PPeerCache(cachedPeers)
		return 0, err
	}

	c.deps.Log(fmt.Sprintf("[p2p][cloud-bootstrap] API retornou %d peer(s)", len(resp.Peers)))
	peerCount := len(resp.Peers)

	// Conectar nos peers retornados pela API e atualizar cache.
	for _, rp := range resp.Peers {
		rp.PeerID = strings.TrimSpace(rp.PeerID)
		if rp.PeerID == "" || rp.Port <= 0 || len(rp.Addrs) == 0 {
			continue
		}

		addrInfo, err := buildAddrInfo(rp.PeerID, rp.Addrs, rp.Port)
		if err != nil {
			c.deps.Log(fmt.Sprintf("[p2p][cloud-bootstrap] peer ignorado (addr inválido) peerId=%s: %v", rp.PeerID, err))
			continue
		}

		connCtx, cancel := context.WithTimeout(ctx, p2pCloudBootstrapConnectTO)
		err = h.Connect(connCtx, addrInfo)
		cancel()

		if err != nil {
			c.deps.Log(fmt.Sprintf("[p2p][cloud-bootstrap] connect falhou peerId=%s: %v", rp.PeerID, err))
			cachedPeers = removeP2PPeerCacheEntry(cachedPeers, rp.PeerID)
			continue
		}

		c.deps.Log(fmt.Sprintf("[p2p][cloud-bootstrap] conectado peerId=%s addrs=%v", rp.PeerID, rp.Addrs))
		cachedPeers = upsertP2PPeerCacheEntry(cachedPeers, p2pCachedPeer{
			PeerID:     rp.PeerID,
			Addrs:      rp.Addrs,
			Port:       rp.Port,
			LastSeenAt: time.Now().UTC(),
		})

		// BUGFIX (revisão 2026-11-01): conectar no libp2p NÃO basta — sem
		// upsertPeer o peer fica invisível para GetPeers()/UI/gossip/catálogo
		// (mesmo sintoma do antigo D4 no caminho peer.online). Registra o peer
		// no estado do coordinator, preservando a porta HTTP conhecida se ele
		// já era descoberto por outro caminho (mesma regra do peer.online).
		address := firstUsableSnapshotAddr(rp.Addrs)
		if address == "" {
			continue
		}
		peerKey := strings.ToLower(strings.TrimSpace(rp.AgentID))
		if peerKey == "" {
			// Servidor antigo sem agentId na resposta: usa o peerId como chave.
			peerKey = strings.ToLower(rp.PeerID)
		}
		port := rp.Port
		c.mu.RLock()
		if known, ok := c.peers[peerKey]; ok && known.Peer.Port > 0 {
			port = known.Peer.Port
		}
		c.mu.RUnlock()
		if inserted := c.upsertPeer(p2pDiscoveredPeer{
			AgentID:      peerKey,
			ClientID:     localClientID,
			Host:         address,
			Address:      address,
			Port:         port,
			Source:       "cloud-bootstrap",
			ConnectedVia: "cloud-bootstrap",
			TTLSeconds:   int(p2pPeerTTLFloor.Seconds()),
		}); inserted {
			peerCount++
		}
	}

	if err := c.saveP2PPeerCache(cachedPeers); err != nil {
		c.deps.Log("[p2p][cloud-bootstrap] erro ao salvar cache: " + err.Error())
		return peerCount, err
	}
	return peerCount, nil
}

// StartCloudBootstrapLoop roda o cloud bootstrap automaticamente enquanto o
// coordinator vive (antes só existia o botão manual da UI - revisão 2026-11-01).
// Primeira execução após jitter de 0..60s; intervalo padrão 60 min com jitter
// de mais/menos 15%; toggle desligado re-checka em 5 min; falha dobra o
// intervalo até o teto de 6h, com reset no primeiro sucesso. É PULL: o
// servidor nao publica nada por heartbeat; cada agent puxa a lista de peers
// online do seu cliente em POST /me/p2p/bootstrap (HeartbeatCache).
func (c *Coordinator) StartCloudBootstrapLoop(ctx context.Context) {
	if c == nil || c.deps == nil {
		return
	}
	go func() {
		interval := p2pCloudBootstrapInterval
		startDelay := time.Duration(rand.Int63n(int64(p2pCloudBootstrapStartupJitter) + 1))
		timer := time.NewTimer(startDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				interval = c.cloudBootstrapCycle(interval)
				timer.Reset(jitteredInterval(interval))
			}
		}
	}()
}

// cloudBootstrapCycle executa um ciclo e retorna o intervalo para a próxima
// tentativa (padrão em sucesso, dobrado em falha, até o teto).
func (c *Coordinator) cloudBootstrapCycle(current time.Duration) time.Duration {
	cfg := c.deps.GetP2PConfig()
	if !cfg.Enabled || !cfg.BootstrapConfig.CloudBootstrapEnabled {
		return p2pCloudBootstrapDisabledRecheck
	}
	bootstrapCtx, cancel := context.WithTimeout(c.Ctx(), CloudBootstrapTimeout+10*time.Second)
	defer cancel()
	peers, err := c.RunCloudBootstrap(bootstrapCtx)
	if errors.Is(err, errCloudBootstrapBusy) {
		// Outro ciclo (botão manual) está rodando: mantém o intervalo, sem backoff.
		return current
	}
	if err != nil {
		next := current * 2
		if next > p2pCloudBootstrapBackoffMax {
			next = p2pCloudBootstrapBackoffMax
		}
		c.deps.Log(fmt.Sprintf("[p2p][cloud-bootstrap] ciclo falhou (proxima tentativa em %s): %v", next, err))
		return next
	}
	if peers > 0 {
		c.deps.Log(fmt.Sprintf("[p2p][cloud-bootstrap] ciclo concluído: %d peer(s) via nuvem", peers))
	}
	return p2pCloudBootstrapInterval
}

// jitteredInterval aplica jitter de mais/menos 15% a um intervalo (minimo 1s).
func jitteredInterval(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	span := base / 7
	if span <= 0 {
		span = time.Second
	}
	j := time.Duration(rand.Int63n(int64(2*span)+1)) - span
	out := base + j
	if out < time.Second {
		out = time.Second
	}
	return out
}

// connectCachedPeers tenta conectar nos peers do cache local.
// Remove entradas inválidas e retorna o cache atualizado.
func (c *Coordinator) connectCachedPeers(ctx context.Context, h interface {
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
			// Incrementa contador de falhas. Só remove o peer do cache
			// após p2pCachedPeerMaxFailures consecutivas — tolera flutuações
			// temporárias de rede sem descartar peers que voltam a responder.
			cp.FailCount++
			if cp.FailCount >= p2pCachedPeerMaxFailures {
				c.deps.Log(fmt.Sprintf("[p2p][cloud-bootstrap] cache: connect falhou peerId=%s (removendo após %d falhas consecutivas): %v", cp.PeerID, cp.FailCount, err))
				continue // não adiciona ao slice válido (remoção implícita)
			}
			c.deps.Log(fmt.Sprintf("[p2p][cloud-bootstrap] cache: connect falhou peerId=%s (falha %d/%d, mantendo no cache): %v", cp.PeerID, cp.FailCount, p2pCachedPeerMaxFailures, err))
			valid = append(valid, cp)
			continue
		}
		c.deps.Log(fmt.Sprintf("[p2p][cloud-bootstrap] cache: reconectado peerId=%s", cp.PeerID))
		cp.LastSeenAt = time.Now().UTC()
		cp.FailCount = 0 // reset em sucesso
		valid = append(valid, cp)
	}
	return valid
}

// callCloudBootstrapAPI faz POST no endpoint de bootstrap e retorna a resposta parseada.
func (c *Coordinator) callCloudBootstrapAPI(ctx context.Context, scheme, server, token, agentID string, payload p2pCloudBootstrapRequest) (*p2pCloudBootstrapResponse, error) {
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
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return nil, err
	}

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
