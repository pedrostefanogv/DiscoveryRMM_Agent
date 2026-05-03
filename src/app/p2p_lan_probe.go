package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const (
	p2pLANProbeRequestTimeout = 650 * time.Millisecond
	p2pLANProbeWorkers        = 48
	p2pLANProbePreferredPorts = 4
)

type p2pHealthResponse struct {
	OK           bool     `json:"ok"`
	AgentID      string   `json:"agentId"`
	HTTPPort     int      `json:"httpPort,omitempty"`
	LibP2PPeerID string   `json:"libp2pPeerId,omitempty"`
	LibP2PAddrs  []string `json:"libp2pAddrs,omitempty"`
}

type p2pLANProbeTarget struct {
	Host string
	Port int
}

func (s *p2pTransferServer) buildHealthResponse() p2pHealthResponse {
	if s == nil {
		return p2pHealthResponse{}
	}
	s.mu.RLock()
	agentID := strings.TrimSpace(s.agentID)
	baseURL := strings.TrimSpace(s.baseURL)
	app := s.app
	s.mu.RUnlock()

	out := p2pHealthResponse{
		OK:      true,
		AgentID: agentID,
	}
	if port, err := parsePortFromURL(baseURL); err == nil {
		out.HTTPPort = port
	}
	if app == nil || app.p2pCoord == nil {
		return out
	}
	h, _ := app.p2pCoord.libp2pHostAndRegistry()
	if h == nil {
		return out
	}
	out.LibP2PPeerID = strings.TrimSpace(h.ID().String())
	out.LibP2PAddrs = buildP2PAdvertiseAddrs(h)
	return out
}

func buildP2PAdvertiseAddrs(h host.Host) []string {
	if h == nil {
		return nil
	}
	peerID := strings.TrimSpace(h.ID().String())
	if peerID == "" {
		return nil
	}
	fallbackIPs := extractRoutableIPv4Addrs(h)
	if len(fallbackIPs) == 0 {
		if ip := strings.TrimSpace(detectLocalAddressForPeers()); ip != "" {
			fallbackIPs = append(fallbackIPs, ip)
		}
	}

	seen := make(map[string]struct{})
	out := make([]string, 0, len(h.Addrs()))
	for _, addr := range h.Addrs() {
		raw := strings.TrimSpace(addr.String())
		if raw == "" {
			continue
		}
		ip := strings.TrimSpace(extractIPFromMultiaddr(raw))
		parsedIP := net.ParseIP(ip)
		switch {
		case parsedIP == nil:
			continue
		case parsedIP.IsLoopback() || parsedIP.IsLinkLocalUnicast():
			continue
		case ip == "0.0.0.0" || ip == "::":
			for _, fallbackIP := range fallbackIPs {
				candidate := appendP2PPeerIDMultiaddr(replaceWildcardMultiaddrIP(raw, fallbackIP), peerID)
				if candidate == "" {
					continue
				}
				if _, ok := seen[candidate]; ok {
					continue
				}
				seen[candidate] = struct{}{}
				out = append(out, candidate)
			}
		default:
			if parsedIP.To4() == nil {
				continue
			}
			candidate := appendP2PPeerIDMultiaddr(raw, peerID)
			if candidate == "" {
				continue
			}
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
	}
	sort.Strings(out)
	return out
}

func appendP2PPeerIDMultiaddr(addr, peerID string) string {
	addr = strings.TrimSpace(addr)
	peerID = strings.TrimSpace(peerID)
	if addr == "" || peerID == "" {
		return ""
	}
	if strings.Contains(addr, "/p2p/") {
		return addr
	}
	return strings.TrimRight(addr, "/") + "/p2p/" + peerID
}

func replaceWildcardMultiaddrIP(addr, replacement string) string {
	parts := strings.Split(addr, "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "ip4" && parts[i+1] == "0.0.0.0" {
			parts[i+1] = replacement
			return strings.Join(parts, "/")
		}
	}
	return addr
}

func localRoutableIPv4Addrs() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP == nil {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			key := ip.String()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func buildLANProbeHostsFromIPs(selfIPs []string) []string {
	seen := make(map[string]struct{})
	selfSet := make(map[string]struct{}, len(selfIPs))
	for _, raw := range selfIPs {
		if ip := net.ParseIP(strings.TrimSpace(raw)).To4(); ip != nil {
			selfSet[ip.String()] = struct{}{}
		}
	}

	out := make([]string, 0, len(selfIPs)*254)
	for _, raw := range selfIPs {
		ip := net.ParseIP(strings.TrimSpace(raw)).To4()
		if ip == nil {
			continue
		}
		base := ip.Mask(net.CIDRMask(24, 32)).To4()
		if base == nil {
			continue
		}
		for host := 1; host <= 254; host++ {
			candidate := net.IPv4(base[0], base[1], base[2], byte(host)).String()
			if _, ok := selfSet[candidate]; ok {
				continue
			}
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
	}
	sort.Strings(out)
	return out
}

func buildLANProbePorts(cfg P2PConfig, selfPort int) []int {
	cfg = normalizeP2PConfig(cfg)
	seen := make(map[int]struct{})
	out := make([]int, 0, p2pLANProbePreferredPorts+1)
	add := func(port int) {
		if port <= 0 {
			return
		}
		if _, ok := seen[port]; ok {
			return
		}
		seen[port] = struct{}{}
		out = append(out, port)
	}
	add(selfPort)
	for port := cfg.HTTPListenPortRangeStart; port <= cfg.HTTPListenPortRangeEnd && len(out) < p2pLANProbePreferredPorts+1; port++ {
		add(port)
	}
	return out
}

func (c *p2pCoordinator) runLANDiscoveryProbe(ctx context.Context, source string) (int, error) {
	if c == nil || c.app == nil {
		return 0, fmt.Errorf("coordinator P2P indisponivel")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h, registry := c.libp2pHostAndRegistry()
	if h == nil || registry == nil {
		return 0, fmt.Errorf("host libp2p nao disponivel")
	}
	selfIPs := localRoutableIPv4Addrs()
	hosts := buildLANProbeHostsFromIPs(selfIPs)
	if len(hosts) == 0 {
		return 0, fmt.Errorf("nenhuma faixa IPv4 local elegivel para probe")
	}
	selfPort, _ := parsePortFromURL(c.transferServer.BaseURL())
	ports := buildLANProbePorts(c.app.GetP2PConfig(), selfPort)
	if len(ports) == 0 {
		return 0, fmt.Errorf("nenhuma porta elegivel para probe LAN")
	}

	client := &http.Client{Timeout: p2pLANProbeRequestTimeout}
	selfAgentID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
	targets := make(chan p2pLANProbeTarget, len(hosts))
	workerCount := p2pLANProbeWorkers
	if total := len(hosts) * len(ports); total > 0 && total < workerCount {
		workerCount = total
	}
	if workerCount < 1 {
		workerCount = 1
	}

	var wg sync.WaitGroup
	var found atomic.Int32
	var hits atomic.Int32
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range targets {
				if ctx.Err() != nil {
					return
				}
				hit, inserted := c.probeLANPeer(ctx, client, h, registry, selfAgentID, source, target)
				if hit {
					hits.Add(1)
				}
				if inserted {
					found.Add(1)
				}
			}
		}()
	}

	for _, host := range hosts {
		for _, port := range ports {
			select {
			case <-ctx.Done():
				close(targets)
				wg.Wait()
				return int(found.Load()), ctx.Err()
			case targets <- p2pLANProbeTarget{Host: host, Port: port}:
			}
		}
	}
	close(targets)
	wg.Wait()

	newPeers := int(found.Load())
	if probeHits := int(hits.Load()); probeHits > 0 || newPeers > 0 {
		c.app.logs.append(fmt.Sprintf("[p2p][lan-probe] source=%s hosts=%d ports=%d hits=%d novos=%d",
			strings.TrimSpace(source), len(hosts), len(ports), probeHits, newPeers))
	}
	return newPeers, nil
}

func (c *p2pCoordinator) probeLANPeer(ctx context.Context, client *http.Client, h host.Host, registry *libp2pPeerRegistry, selfAgentID, source string, target p2pLANProbeTarget) (bool, bool) {
	probeURL := fmt.Sprintf("http://%s:%d/p2p/health", strings.TrimSpace(target.Host), target.Port)
	reqCtx, cancel := context.WithTimeout(ctx, p2pLANProbeRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, probeURL, nil)
	if err != nil {
		return false, false
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}

	var health p2pHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return false, false
	}
	health.AgentID = strings.TrimSpace(health.AgentID)
	if !health.OK || health.AgentID == "" || strings.EqualFold(health.AgentID, selfAgentID) {
		return false, false
	}

	connectedVia := "lan-probe"
	if err := connectLANProbePeer(ctx, h, registry, health); err == nil {
		connectedVia = p2pDiscoveryLibP2P
	}

	peerPort := target.Port
	if health.HTTPPort > 0 {
		peerPort = health.HTTPPort
	}
	peerView := p2pDiscoveredPeer{
		AgentID:      health.AgentID,
		Host:         strings.TrimSpace(target.Host),
		Address:      strings.TrimSpace(target.Host),
		Port:         peerPort,
		Source:       "lan-probe",
		ConnectedVia: connectedVia,
	}
	inserted := c.upsertPeer(peerView)
	if inserted {
		go c.refreshSinglePeer(ctx, peerView)
		c.app.logs.append(fmt.Sprintf("[p2p][lan-probe] peer confirmado: agentId=%s addr=%s:%d source=%s",
			peerView.AgentID, peerView.Address, peerView.Port, strings.TrimSpace(source)))
	}
	return true, inserted
}

func connectLANProbePeer(ctx context.Context, h host.Host, registry *libp2pPeerRegistry, health p2pHealthResponse) error {
	if h == nil || registry == nil {
		return fmt.Errorf("libp2p indisponivel")
	}
	addrInfos, err := addrInfosFromHealthResponse(health)
	if err != nil {
		return err
	}
	var lastErr error
	for _, info := range addrInfos {
		connectCtx, cancel := context.WithTimeout(ctx, p2pLibP2PHandshakeTimeout)
		err = h.Connect(connectCtx, info)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		if ok, existing, conflict := registry.RegisterStrict(strings.TrimSpace(health.AgentID), info.ID); conflict {
			_ = h.Network().ClosePeer(info.ID)
			return fmt.Errorf("conflito de identidade agentId=%s peerAtual=%s peerRegistrado=%s", strings.TrimSpace(health.AgentID), info.ID, existing)
		} else if !ok {
			return nil
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("nenhum multiaddr libp2p conectavel")
}

func addrInfosFromHealthResponse(health p2pHealthResponse) ([]peer.AddrInfo, error) {
	if len(health.LibP2PAddrs) == 0 {
		return nil, fmt.Errorf("peer %s sem multiaddrs libp2p anunciados", strings.TrimSpace(health.AgentID))
	}
	infosByID := make(map[peer.ID]*peer.AddrInfo)
	for _, raw := range health.LibP2PAddrs {
		ma, err := multiaddr.NewMultiaddr(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		pi, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			continue
		}
		entry, ok := infosByID[pi.ID]
		if !ok {
			clone := *pi
			infosByID[pi.ID] = &clone
			continue
		}
		entry.Addrs = append(entry.Addrs, pi.Addrs...)
	}
	if len(infosByID) == 0 {
		return nil, fmt.Errorf("peer %s sem multiaddr libp2p valido", strings.TrimSpace(health.AgentID))
	}
	out := make([]peer.AddrInfo, 0, len(infosByID))
	for _, info := range infosByID {
		out = append(out, *info)
	}
	return out, nil
}
