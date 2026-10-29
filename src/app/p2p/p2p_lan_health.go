package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// p2pLANHealthPath é a única rota exposta na interface de LAN.
//
// O listener principal do TransferServer continua loopback-only (hardening C4:
// /p2p/config/onboard e /p2p/artifact/* nunca saem da máquina). Porém o probe
// de descoberta (p2p_lan_probe.go) e o anúncio de peerId/addrs dependem de um
// /p2p/health alcançável pelo IP de LAN do peer — sem ele a descoberta por
// subnet é impossível (achado D2, 2026-10-29). Esta rota não carrega segredo
// algum: devolve apenas {ok, agentId, httpPort, libp2pPeerId, libp2pAddrs},
// que é exatamente o que o mDNS já anuncia na sub-rede.
const p2pLANHealthPath = "/p2p/health"

// p2pLANHealthServer serve /p2p/health em um listener por interface IPv4
// roteável. É deliberadamente mínimo: nenhuma rota além de /p2p/health.
type p2pLANHealthServer struct {
	coord *Coordinator

	mu      sync.Mutex
	servers []*http.Server
	urls    []string
	ips     []string
}

// boundIPs devolve os IPs em que este servidor está escutando.
func (s *p2pLANHealthServer) boundIPs() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.ips))
	copy(out, s.ips)
	return out
}

// refreshLANHealthServer reconcilia os listeners com os IPs ATUAIS da máquina.
// Sem isso, um agente cujo IP muda (DHCP/VPN/wifi->cabo) continuaria
// anunciando health num endereço que não existe mais, e o probe de LAN não o
// encontraria até o próximo restart.
func (c *Coordinator) refreshLANHealthServer() {
	if c == nil {
		return
	}
	c.mu.RLock()
	srv := c.lanHealth
	c.mu.RUnlock()
	if srv == nil {
		return
	}
	bound := srv.boundIPs()
	want := p2pLANHealthCandidateIPs()
	if sameIPSet(bound, want) {
		return
	}
	if c.deps != nil {
		c.deps.Log("[p2p][lan-health] IPs da interface mudaram; reabrando health de LAN (antes=" +
			strings.Join(bound, ",") + " agora=" + strings.Join(want, ",") + ")")
	}
	c.startLANHealthServer()
}

// sameIPSet compara dois conjuntos de IPs independentemente da ordem.
func sameIPSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, ip := range a {
		seen[ip] = struct{}{}
	}
	for _, ip := range b {
		if _, ok := seen[ip]; !ok {
			return false
		}
	}
	return true
}

// startLANHealthServer sobe o health de LAN usando a mesma porta do
// TransferServer (o número é o mesmo; muda apenas o endereço de bind).
func (c *Coordinator) startLANHealthServer() {
	if c == nil || c.transferServer == nil || c.deps == nil {
		return
	}
	port := 0
	if parsed, err := parsePortFromURL(c.transferServer.BaseURL()); err == nil {
		port = parsed
	}
	if port <= 0 {
		c.deps.Log("[p2p][lan-health] porta do servidor local indisponivel; health de LAN nao iniciado")
		return
	}
	// Idempotência: se já existir um servidor (start chamado 2x), fecha antes de
	// criar outro para não vazar listeners.
	c.mu.Lock()
	previous := c.lanHealth
	c.lanHealth = nil
	c.mu.Unlock()
	if previous != nil {
		previous.Close()
	}

	srv, err := c.startLANHealthServerOn(p2pLANHealthCandidateIPs(), port)
	if err != nil {
		c.deps.Log("[p2p][lan-health] falha ao iniciar: " + err.Error())
		return
	}
	if srv == nil || len(srv.urls) == 0 {
		c.deps.Log("[p2p][lan-health] nenhum endereco de LAN disponivel para anunciar health")
		return
	}
	c.mu.Lock()
	c.lanHealth = srv
	c.mu.Unlock()
	c.deps.Log("[p2p][lan-health] health publico em " + strings.Join(srv.urls, ", "))
}

// startLANHealthServerOn sobe o servidor para a lista de IPs informada.
// port == 0 abre porta efêmera (usado em teste).
func (c *Coordinator) startLANHealthServerOn(ips []string, port int) (*p2pLANHealthServer, error) {
	if c == nil || c.transferServer == nil {
		return nil, fmt.Errorf("transfer server indisponivel")
	}
	srv := &p2pLANHealthServer{coord: c}
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		if err != nil {
			// Porta ocupada/interface sem suporte: segue nas demais.
			if c.deps != nil {
				c.deps.Log(fmt.Sprintf("[p2p][lan-health] bind %s:%d falhou: %v", ip, port, err))
			}
			continue
		}
		mux := http.NewServeMux()
		mux.HandleFunc(p2pLANHealthPath, srv.handleHealth)
		httpSrv := &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       30 * time.Second,
		}
		srv.mu.Lock()
		srv.servers = append(srv.servers, httpSrv)
		srv.urls = append(srv.urls, "http://"+ln.Addr().String()+p2pLANHealthPath)
		srv.ips = append(srv.ips, ip)
		srv.mu.Unlock()
		go func() { _ = httpSrv.Serve(ln) }()
	}
	return srv, nil
}

// Close encerra todos os listeners de health de LAN. Idempotente.
func (s *p2pLANHealthServer) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	servers := s.servers
	s.servers = nil
	s.urls = nil
	s.ips = nil
	s.mu.Unlock()
	for _, srv := range servers {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
}

func (s *p2pLANHealthServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	health := p2pHealthResponse{}
	if s != nil && s.coord != nil {
		health = s.coord.transferServer.buildHealthResponse()
	}
	_, _ = w.Write([]byte(healthJSON(health)))
}

// healthJSON serializa o health response.
func healthJSON(h p2pHealthResponse) string {
	b, err := json.Marshal(h)
	if err != nil {
		return "{\"ok\":false}"
	}
	return string(b)
}

// p2pLANHealthCandidateIPs devolve os IPv4 roteáveis do host, sem loopback e
// sem link-local (APIPA), priorizando o IP de saída para a internet.
func p2pLANHealthCandidateIPs() []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 4)
	add := func(ip string) {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			return
		}
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.To4() == nil || parsed.IsLoopback() || parsed.IsLinkLocalUnicast() {
			return
		}
		if _, ok := seen[ip]; ok {
			return
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}
	if primary := strings.TrimSpace(DetectLocalAddressForPeers()); primary != "" {
		add(primary)
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, addrErr := iface.Addrs()
		if addrErr != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			add(ipNet.IP.String())
		}
	}
	return out
}
