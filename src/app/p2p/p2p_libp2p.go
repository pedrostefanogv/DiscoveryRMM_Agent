package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	noise "github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
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
	Host     string `json:"host,omitempty"`
	ClientID string `json:"clientId"`
	HTTPPort int    `json:"httpPort"`
}

// p2pLibP2PProvider implements p2pDiscoveryProvider using go-libp2p mDNS.
// It advertises the local agent on the LAN and discovers peers via libp2p's
// built-in mDNS service (distinct from the existing grandcat/zeroconf path).
// When a peer is found, a /discovery-p2p/1.0.0 stream is opened to exchange
// {agentId, clientId, httpPort}. The full P2P transport protocols (/artifact/*, /discovery/peers)
// are registered on the same host so artifact transfers avoid HTTP entirely.
type p2pLibP2PProvider struct {
	// bootstrapPeers holds optional static multiaddr strings (including peer IDs)
	// to connect to at startup, enabling discovery in non-multicast networks.
	bootstrapPeers []string

	// tcpOnly restringe o host ao transporte TCP (desativa QUIC). QUIC/UDP no
	// Windows é suspeito recorrente de quedas de conexão durante transferências
	// ("Application error 0x0"); com TCP-only eliminamos o par TCP+QUIC que
	// amplia corridas de dial/dedup. Default: true (via TCPOnlyEnabled()).
	tcpOnly bool

	// coord and transfer are injected by startDiscovery so the host can serve
	// the libp2p transport protocols (peers/access/manifest/get/replicate).
	coord    *Coordinator
	transfer *TransferServer

	// registry maps agentID -> libp2p peer.ID for client-side calls.
	registry *libp2pPeerRegistry

	// host is exported after Start so the coordinator can open outbound streams.
	h host.Host

	// gater allows blocking misbehaving peers at the connection level.
	gater *p2pConnectionGater

	// clientID is the logical namespace for this P2P mesh.
	clientID string

	// svc é o serviço mDNS, guardado para cleanup síncrono no Close.
	svc mdns.Service

	// closeMu protege Close contra chamadas concorrentes.
	closeMu sync.Mutex
	closed  bool
}

func (p *p2pLibP2PProvider) Name() string { return p2pDiscoveryLibP2P }

// Close desliga o provider libp2p de forma síncrona: fecha o mDNS service e o
// host libp2p. É idempotente. Se Start não foi chamado, é no-op.
func (p *p2pLibP2PProvider) Close() {
	if p == nil {
		return
	}
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	if p.svc != nil {
		_ = p.svc.Close()
		p.svc = nil
	}
	if p.h != nil {
		_ = p.h.Close()
		p.h = nil
	}
}

func (p *p2pLibP2PProvider) Start(
	ctx context.Context,
	self p2pSelfEndpoint,
	onPeer func(peer p2pDiscoveredPeer),
	onTrace func(string),
) error {
	p.clientID = normalizeClientID(self.ClientID)

	// ConnManager: limites do p2pConnManagerLimits (default 8–24; override por
	// DISCOVERY_P2P_MAX_CONNS). Cada conexão ativa sustenta buffers de transporte
	// (TCP/yamux/noise), goroutines e entradas no peerstore — os 20–60 anteriores
	// mantinham dezenas de conexões paradas em repouso, consumindo RAM sem uso.
	connLow, connHigh := p2pConnManagerLimits()
	cm, err := connmgr.NewConnManager(connLow, connHigh, connmgr.WithGracePeriod(2*time.Minute))
	if err != nil {
		return fmt.Errorf("connmgr: %w", err)
	}

	gater := newP2PConnectionGater()
	p.gater = gater

	// Transportes: TCP sempre; QUIC/v1 apenas quando tcpOnly=false.
	// QUIC/UDP no Windows tem histórico de quedas durante transferências
	// paralelas; a flag permite A/B test e rollback rápido.
	//
	// BUGFIX D9 (2026-10-29): a porta era SEMPRE efêmera (/tcp/0). Isso torna
	// impossível ter regra de firewall por porta, invalida o cache de peers e
	// quebra qualquer NAT a cada restart. Agora usa porta fixa (default 41121,
	// override DISCOVERY_P2P_LIBP2P_PORT) com fallback para efêmera caso a
	// porta esteja ocupada — antes, uma falha de bind derrubaria a descoberta.
	fixedPort := p2pLibP2PFixedPort()

	// TCP + QUIC/v1 sobre IPv4 apenas (IPv6 desativado).
	// Segurança declarada explicitamente: Noise (preferido) e TLS 1.3.
	// A5: identidade Ed25519 persistente em <DataDir>/p2p — sem isso o
	// peer.ID mudava a cada restart e peers conheciam conflito de identidade
	// (BlockPeer permanente). Falha ao persistir → identidade efêmera
	// (comportamento antigo) + log de trace.
	var identity crypto.PrivKey
	if p.coord != nil && p.coord.deps != nil {
		if priv, idErr := loadOrCreateLibp2PIdentity(p.coord.deps.GetDataDir()); idErr == nil {
			identity = priv
		} else if onTrace != nil {
			onTrace(fmt.Sprintf("identidade libp2p persistente indisponivel (gerando efemera): %v", idErr))
		}
	}
	buildOpts := func(port int) []libp2p.Option {
		listenAddrs := []string{fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port)}
		if !p.tcpOnly {
			listenAddrs = append(listenAddrs, fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", port))
		}
		opts := []libp2p.Option{
			libp2p.ListenAddrStrings(listenAddrs...),
			libp2p.Security(noise.ID, noise.New),
			libp2p.Security(libp2ptls.ID, libp2ptls.New),
			libp2p.ConnectionManager(cm),
			libp2p.ConnectionGater(gater),
		}
		if identity != nil {
			opts = append(opts, libp2p.Identity(identity))
		}
		return opts
	}
	var h host.Host
	var hostErr error
	if fixedPort > 0 {
		// Até 3 tentativas: o RestartProvider fecha o host anterior e reabre a
		// MESMA porta logo em seguida — o rebind pode falhar por um instante
		// (handle ainda não liberado). Sem retry, cairíamos calados em porta
		// efêmera e a regra de firewall de 41121 ficaria inútil.
		for attempt := 1; attempt <= 3; attempt++ {
			h, hostErr = libp2p.New(buildOpts(fixedPort)...)
			if hostErr == nil {
				break
			}
			if onTrace != nil {
				onTrace(fmt.Sprintf("libp2p: bind da porta fixa %d falhou (tentativa %d/3): %v", fixedPort, attempt, hostErr))
			}
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		if hostErr != nil {
			// Porta realmente ocupada por outro processo: degrada para efêmera em
			// vez de derrubar a descoberta (a regra de firewall por porta deixa de
			// cobrir este processo — o log acima deixa isso explícito).
			if onTrace != nil {
				onTrace(fmt.Sprintf("libp2p: porta fixa %d indisponivel, usando porta efemera: %v", fixedPort, hostErr))
			}
			h, hostErr = libp2p.New(buildOpts(0)...)
		}
	} else {
		h, hostErr = libp2p.New(buildOpts(0)...)
	}
	if hostErr != nil {
		return fmt.Errorf("libp2p host: %w", hostErr)
	}

	// Notifiee de diagnóstico: registra desconexões (com transporte e se havia
	// streams ativos) para correlacionar quedas com ticks de descoberta.
	if p.coord != nil && p.coord.deps != nil {
		h.Network().Notify(newP2PConnTraceNotifiee(p.coord.deps.Log))
	}

	// Stream handler (responder side): when a peer opens a stream to us,
	// read their info, respond with ours, validate clientId, then emit onPeer.
	h.SetStreamHandler(p2pLibP2PProtocolID, func(s network.Stream) {
		handleLibP2PHandshakeStream(s, self, p.clientID, p.registry, p.gater, onPeer, onTrace)
	})
	// (handler extraído para handleLibP2PHandshakeStream — testável)

	// Registrar todos os protocolos de transporte P2P no host.
	if p.coord != nil && p.transfer != nil {
		RegisterP2PProtocols(h, p.coord, p.transfer)
		// Onboarding por stream: o HTTP local é loopback-only (C4), então a
		// oferta de zero-touch precisa viajar pelo libp2p para alcançar a LAN.
		RegisterOnboardingProtocols(h, p.transfer)
	}
	if p.registry == nil {
		p.registry = newLibp2pPeerRegistry()
	}
	p.h = h

	notifee := &libp2pMDNSNotifee{h: h, self: self, onPeer: onPeer, onTrace: onTrace, registry: p.registry, clientID: p.clientID}
	svc := mdns.NewMdnsService(h, p2pLibP2PRendezvous, notifee)
	p.svc = svc

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
				onTrace(fmt.Sprintf("libp2p bootstrap peer multiaddr inválido %q: %v", addrStr, err))
			}
			continue
		}
		pi, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			if onTrace != nil {
				onTrace(fmt.Sprintf("libp2p bootstrap peer info inválido %q: %v", addrStr, err))
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

	// Cleanup assíncrono quando o ctx é cancelado (backup — Close() síncrono
	// também pode ser chamado pelo Coordinator.Shutdown).
	go func() {
		<-ctx.Done()
		p.Close()
	}()

	if onTrace != nil {
		onTrace(fmt.Sprintf("libp2p host iniciado: peerID=%s", h.ID()))
	}
	return nil
}

// p2pLibP2PFixedPort é a porta TCP fixa do host libp2p (default 41121).
//
// Porta fixa é pré-requisito para: (a) regra de firewall por porta no
// instalador, (b) cache de peers estável entre restarts, (c) NAT/port-forward
// previsível. Override: DISCOVERY_P2P_LIBP2P_PORT; 0 força porta efêmera
// (comportamento antigo, útil para testes).
func p2pLibP2PFixedPort() int {
	const defaultPort = 41121
	raw := strings.TrimSpace(os.Getenv("DISCOVERY_P2P_LIBP2P_PORT"))
	if raw == "" {
		return defaultPort
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 0 || port > 65535 {
		return defaultPort
	}
	return port
}

// p2pConnManagerLimits devolve os limites (low, high) do ConnManager do libp2p.
//
// Otimização de memória: os valores anteriores (20–60) sustentavam dezenas de
// conexões em repouso numa malha grande; cada conexão carrega buffers de
// transporte, yamux/noise e estado no peerstore. 8–24 cobrem o caso real (poucos
// peers por transferência) e continuam podando o excedente com grace period.
//
// Override: DISCOVERY_P2P_MAX_CONNS define o teto (4..200); o mínimo fica em 1/3.
func p2pConnManagerLimits() (int, int) {
	const (
		defaultLow  = 8
		defaultHigh = 24
	)
	raw := strings.TrimSpace(os.Getenv("DISCOVERY_P2P_MAX_CONNS"))
	if raw == "" {
		return defaultLow, defaultHigh
	}
	high, err := strconv.Atoi(raw)
	if err != nil || high < 4 {
		return defaultLow, defaultHigh
	}
	if high > 200 {
		high = 200
	}
	low := high / 3
	if low < 2 {
		low = 2
	}
	return low, high
}

// libp2pMDNSNotifee handles peers discovered by libp2p's mDNS service.
type libp2pMDNSNotifee struct {
	h        host.Host
	self     p2pSelfEndpoint
	onPeer   func(p2pDiscoveredPeer)
	onTrace  func(string)
	registry *libp2pPeerRegistry
	clientID string
}

func (n *libp2pMDNSNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.h.ID() {
		return // ignore self
	}

	// Filtra endereços link-local (APIPA 169.254.x.x) e loopback para evitar
	// timeouts durante dial — esses endereços não são roteáveis entre máquinas
	// diferentes e só causam tentativas de conexão fracassadas.
	filteredAddrs := make([]multiaddr.Multiaddr, 0, len(pi.Addrs))
	for _, addr := range pi.Addrs {
		ipStr := extractIPFromMultiaddr(addr.String())
		if ipStr == "" {
			continue
		}
		ip := net.ParseIP(ipStr)
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			if n.onTrace != nil {
				n.onTrace(fmt.Sprintf("libp2p filtrado endereco link-local: %s (peer=%s)", addr.String(), pi.ID))
			}
			continue
		}
		filteredAddrs = append(filteredAddrs, addr)
	}
	if len(filteredAddrs) == 0 {
		if n.onTrace != nil {
			n.onTrace(fmt.Sprintf("libp2p peer ignorado (apenas enderecos link-local): peerID=%s", pi.ID))
		}
		return
	}
	pi.Addrs = filteredAddrs
	n.exchangeWithPeer(pi)
}

// exchangeWithPeer conecta no peer e troca identidade pelo handshake. Separado
// de HandlePeerFound para permitir teste em processo (loopback), onde o filtro
// de endereços link-local do HandlePeerFound descartaria o peer.
func (n *libp2pMDNSNotifee) exchangeWithPeer(pi peer.AddrInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), p2pLibP2PHandshakeTimeout+discoveryDialJitter)
	// BUGFIX D1 (2026-10-29): o cancel() NÃO pode vir antes do NewStream abaixo.
	// Antes, o stream de handshake herdava um contexto JÁ CANCELADO, falhava
	// sempre com "context canceled" e o peer era descartado — mDNS descobria,
	// conectava e nunca registrava o agentId (malha P2P ficava vazia).
	defer cancel()
	if err := connectPeerIfNotConnected(ctx, n.h, pi, discoveryDialJitter); err != nil {
		if n.onTrace != nil {
			n.onTrace(fmt.Sprintf("libp2p connect falhou peer=%s: %v", pi.ID, err))
		}
		return
	}

	// Remove do peerstore quaisquer endereços link-local que ainda estejam
	// armazenados de descobertas anteriores — eles não são roteáveis.
	cleanPeerstoreAddrs(n.h, pi.ID)

	s, err := n.h.NewStream(ctx, pi.ID, p2pLibP2PProtocolID)
	if err != nil {
		if n.onTrace != nil {
			n.onTrace(fmt.Sprintf("libp2p stream falhou peer=%s: %v", pi.ID, err))
		}
		return
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(p2pLibP2PHandshakeTimeout))

	// Initiator sends first (with clientId), then reads response.
	clientID := normalizeClientID(n.self.ClientID)
	mine := p2pLibP2PPeerInfo{AgentID: n.self.AgentID, Host: n.self.Host, ClientID: clientID, HTTPPort: n.self.Port}
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

	// Validação de clientId: rejeita peers de malhas diferentes, mas aceita peer
	// sem clientId (agente não provisionado) — ver F7/2026-10-29 no responder.
	remoteClientID := normalizeClientID(remote.ClientID)
	if clientID != "" && remoteClientID != "" && !strings.EqualFold(remoteClientID, clientID) {
		if n.onTrace != nil {
			n.onTrace(fmt.Sprintf("peer rejeitado por clientId divergente: peer=%s clientId=%s esperado=%s",
				strings.TrimSpace(remote.AgentID), strings.TrimSpace(remote.ClientID), clientID))
		}
		_ = n.h.Network().ClosePeer(pi.ID)
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
		if ok, existing, conflict := n.registry.RegisterStrict(remote.AgentID, pi.ID); conflict {
			if n.onTrace != nil {
				n.onTrace(fmt.Sprintf("libp2p peer rejeitado por conflito de identidade: agentId=%s peerAtual=%s peerRegistrado=%s",
					strings.TrimSpace(remote.AgentID), pi.ID, existing))
			}
			_ = n.h.Network().ClosePeer(pi.ID)
			return
		} else if !ok {
			return
		}
	}
	peerHost := strings.TrimSpace(remote.Host)
	if peerHost == "" {
		peerHost = remoteAddr
	}
	n.onPeer(p2pDiscoveredPeer{
		AgentID:      strings.TrimSpace(remote.AgentID),
		ClientID:     strings.TrimSpace(remote.ClientID),
		Host:         peerHost,
		Address:      remoteAddr,
		Port:         remote.HTTPPort,
		Source:       p2pDiscoveryLibP2P,
		ConnectedVia: p2pDiscoveryLibP2P,
	})
}

// handleLibP2PHandshakeStream é o lado responder do handshake /discovery-p2p/1.0.0:
// lê a identidade do peer, responde com a nossa, valida clientId, registra o
// mapeamento agentId→peer.ID e emite onPeer.
//
// Extraído de Start para ser testável (regressão do cancel prematuro do
// contexto — achado D1/2026-10-29).
func handleLibP2PHandshakeStream(
	s network.Stream,
	self p2pSelfEndpoint,
	clientID string,
	registry *libp2pPeerRegistry,
	gater *p2pConnectionGater,
	onPeer func(p2pDiscoveredPeer),
	onTrace func(string),
) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(p2pLibP2PHandshakeTimeout))

	var remote p2pLibP2PPeerInfo
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&remote); err != nil {
		return
	}
	mine := p2pLibP2PPeerInfo{AgentID: self.AgentID, Host: self.Host, ClientID: clientID, HTTPPort: self.Port}
	if err := json.NewEncoder(s).Encode(mine); err != nil {
		return
	}

	if strings.TrimSpace(remote.AgentID) == "" || remote.HTTPPort <= 0 {
		return
	}

	// Validação de clientId: rejeita peers de malhas diferentes, mas ACEITA
	// peer sem clientId — é o agente ainda não provisionado, que precisa
	// existir na malha para pedir config no zero-touch (F7/2026-10-29).
	remoteClientID := normalizeClientID(remote.ClientID)
	if clientID != "" && remoteClientID != "" && !strings.EqualFold(remoteClientID, clientID) {
		if onTrace != nil {
			onTrace(fmt.Sprintf("peer rejeitado por clientId divergente: peer=%s clientId=%s esperado=%s",
				strings.TrimSpace(remote.AgentID), strings.TrimSpace(remote.ClientID), clientID))
		}
		_ = s.Reset()
		return
	}

	remoteAddr := extractIPFromMultiaddr(s.Conn().RemoteMultiaddr().String())
	if remoteAddr == "" {
		return
	}
	if onTrace != nil {
		onTrace(fmt.Sprintf("libp2p peer (inbound): agentId=%s clientId=%s addr=%s:%d",
			remote.AgentID, remote.ClientID, remoteAddr, remote.HTTPPort))
	}
	// Registrar mapeamento agentID → peer.ID para transfer streams.
	if registry != nil {
		if ok, existing, conflict := registry.RegisterStrict(remote.AgentID, s.Conn().RemotePeer()); conflict {
			if onTrace != nil {
				onTrace(fmt.Sprintf("libp2p peer rejeitado por conflito de identidade: agentId=%s peerAtual=%s peerRegistrado=%s",
					strings.TrimSpace(remote.AgentID), s.Conn().RemotePeer(), existing))
			}
			if gater != nil {
				gater.BlockPeer(s.Conn().RemotePeer())
			}
			_ = s.Reset()
			return
		} else if !ok {
			return
		}
	}
	peerHost := strings.TrimSpace(remote.Host)
	if peerHost == "" {
		peerHost = remoteAddr
	}
	if onPeer != nil {
		onPeer(p2pDiscoveredPeer{
			AgentID:      strings.TrimSpace(remote.AgentID),
			ClientID:     strings.TrimSpace(remote.ClientID),
			Host:         peerHost,
			Address:      remoteAddr,
			Port:         remote.HTTPPort,
			Source:       p2pDiscoveryLibP2P,
			ConnectedVia: p2pDiscoveryLibP2P,
		})
	}
}

// pickDiscoveryProvider returns the libp2p provider, injecting coord e transfer
// para que o host registre os protocolos de transporte de artifacts.
func pickDiscoveryProvider(cfg P2PConfig, coord *Coordinator, transfer *TransferServer) p2pDiscoveryProvider {
	registry := newLibp2pPeerRegistry()
	return &p2pLibP2PProvider{
		bootstrapPeers: cfg.BootstrapConfig.BootstrapPeers,
		tcpOnly:        cfg.TCPOnlyEnabled(),
		coord:          coord,
		transfer:       transfer,
		registry:       registry,
	}
}

// ── ConnectionGater ─────────────────────────────────────────────────────────

// peerBlocklistTTL define por quanto tempo um peer permanece bloqueado após
// um conflito de identidade. Correção A5: a blocklist era append-only e
// permanente — um peer legítimo cuja identidade mudou (dados limpos, chave
// corrompida) ficava bloqueado para sempre, partindo a malha. Com TTL, a
// rotação legítima se recupera sozinha; ataques continuam bloqueados pela
// janela.
const peerBlocklistTTL = 15 * time.Minute

// p2pConnectionGater implementa connmgr.ConnectionGater com uma blocklist de
// peers marcados como maliciosos/defeituosos, com expiração automática (A5).
// Todos os métodos são thread-safe.
type p2pConnectionGater struct {
	mu        sync.RWMutex
	blocklist map[peer.ID]time.Time // expiração do bloqueio
}

func newP2PConnectionGater() *p2pConnectionGater {
	return &p2pConnectionGater{blocklist: make(map[peer.ID]time.Time)}
}

// BlockPeer adiciona um peer à blocklist; futuras conexões dele serão
// recusadas até expirar (peerBlocklistTTL).
func (g *p2pConnectionGater) BlockPeer(id peer.ID) {
	g.mu.Lock()
	g.blocklist[id] = time.Now().Add(peerBlocklistTTL)
	g.mu.Unlock()
}

// UnblockPeer remove um peer da blocklist (ex.: registro consistente aceito
// após expiração da janela de conflito).
func (g *p2pConnectionGater) UnblockPeer(id peer.ID) {
	g.mu.Lock()
	delete(g.blocklist, id)
	g.mu.Unlock()
}

// isBlocked verifica (e limpa) entradas expiradas. Thread-safe.
func (g *p2pConnectionGater) isBlocked(id peer.ID) bool {
	g.mu.RLock()
	until, blocked := g.blocklist[id]
	g.mu.RUnlock()
	if !blocked {
		return false
	}
	if time.Now().Before(until) {
		return true
	}
	// Expirado: remove (re-checa sob lock de escrita para não apagar
	// renovação feita por outra goroutine entre a leitura e o delete).
	g.mu.Lock()
	if cur, ok := g.blocklist[id]; ok && time.Now().After(cur) {
		delete(g.blocklist, id)
	}
	g.mu.Unlock()
	return false
}

func (g *p2pConnectionGater) InterceptPeerDial(id peer.ID) bool {
	return !g.isBlocked(id)
}

func (g *p2pConnectionGater) InterceptAddrDial(_ peer.ID, addr multiaddr.Multiaddr) bool {
	// Bloqueia dial para endereços link-local (APIPA 169.254.x.x) como
	// defesa em profundidade — esses endereços não são roteáveis entre
	// máquinas e causariam timeouts.
	ipStr := extractIPFromMultiaddr(addr.String())
	if ipStr != "" {
		ip := net.ParseIP(ipStr)
		if ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
			return false
		}
	}
	return true
}

func (g *p2pConnectionGater) InterceptAccept(_ network.ConnMultiaddrs) bool {
	return true
}

func (g *p2pConnectionGater) InterceptSecured(_ network.Direction, id peer.ID, _ network.ConnMultiaddrs) bool {
	return !g.isBlocked(id)
}

func (g *p2pConnectionGater) InterceptUpgraded(_ network.Conn) (bool, control.DisconnectReason) {
	return true, 0
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

// cleanPeerstoreAddrs remove do peerstore do host todos os endereços
// link-local (APIPA) para o peer informado, evitando que tentativas de
// dial futuras percam tempo com endereços não roteáveis.
func cleanPeerstoreAddrs(h host.Host, pid peer.ID) {
	ps := h.Peerstore()
	addrs := ps.Addrs(pid)
	var keep []multiaddr.Multiaddr
	for _, addr := range addrs {
		ipStr := extractIPFromMultiaddr(addr.String())
		if ipStr == "" {
			keep = append(keep, addr)
			continue
		}
		ip := net.ParseIP(ipStr)
		if ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
			continue
		}
		keep = append(keep, addr)
	}
	if len(keep) < len(addrs) {
		ps.ClearAddrs(pid)
		for _, addr := range keep {
			ps.AddAddr(pid, addr, peerstore.PermanentAddrTTL)
		}
	}
}
