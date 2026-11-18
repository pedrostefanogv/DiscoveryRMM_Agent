package netproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ipv4UnspecifiedPrefix = netip.MustParsePrefix("0.0.0.0/8")
	ipv4Broadcast         = netip.MustParseAddr("255.255.255.255")
	awsIMDSv2IPv6         = netip.MustParseAddr("fd00:ec2::254")
)

// IPResolver abstrai a resolucao DNS para permitir injecao em testes (anti-TOCTOU).
type IPResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Endpoint representa um destino explicito (IP + porta), como 127.0.0.1:3389 para RDP.
type Endpoint struct {
	IP   netip.Addr
	Port int
}

// Allowlist controla quais hosts/portas podem ser acessados via proxy/tunel.
// Vazia por padrao = bloqueio total (fail-closed).
type Allowlist struct {
	mu                sync.RWMutex
	prefixes          []netip.Prefix
	ports             map[int]bool
	explicitEndpoints map[Endpoint]struct{}
	enabled           bool
	allowLoopback     bool
	resolver          IPResolver
	dialTimeout       time.Duration
}

// NewAllowlist cria uma allowlist vazia (bloqueio total, allowLoopback=false).
func NewAllowlist() *Allowlist {
	return &Allowlist{
		ports:             make(map[int]bool),
		explicitEndpoints: make(map[Endpoint]struct{}),
		enabled:           true,
		allowLoopback:     false,
		resolver:          net.DefaultResolver,
		dialTimeout:       10 * time.Second,
	}
}

// AddCIDR adiciona uma rede permitida (ex: 192.168.0.0/16).
func (a *Allowlist) AddCIDR(cidr string) error {
	p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return err
	}
	masked := p.Masked()
	// Normaliza ::ffff:a.b.c.d/n se for prefixo IPv4-mapped de /96..128
	if masked.Addr().Is4In6() && masked.Bits() >= 96 {
		unmapped := masked.Addr().Unmap()
		masked = netip.PrefixFrom(unmapped, masked.Bits()-96).Masked()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prefixes = append(a.prefixes, masked)
	return nil
}

// AddIP adiciona um IP unico permitido (ex: 192.168.1.10).
func (a *Allowlist) AddIP(ipStr string) error {
	addr, err := netip.ParseAddr(strings.TrimSpace(ipStr))
	if err != nil {
		return err
	}
	addr = addr.Unmap()
	p := netip.PrefixFrom(addr, addr.BitLen())
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prefixes = append(a.prefixes, p)
	return nil
}

// AddEntry aceita CIDR (192.168.1.0/24) ou IP avulso (192.168.1.10).
func (a *Allowlist) AddEntry(cidrOrIP string) error {
	s := strings.TrimSpace(cidrOrIP)
	if s == "" {
		return fmt.Errorf("entrada de allowlist vazia")
	}
	if strings.Contains(s, "/") {
		return a.AddCIDR(s)
	}
	return a.AddIP(s)
}

// AddPort adiciona uma porta permitida (1..65535).
func (a *Allowlist) AddPort(port int) {
	if port < 1 || port > 65535 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ports[port] = true
}

// SetEnabled ativa/desativa a checagem de CIDRs/portas (protecoes SSRF permanecem ativas).
func (a *Allowlist) SetEnabled(enabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.enabled = enabled
}

// SetAllowLoopback define se enderecos de loopback (127.0.0.0/8, ::1, localhost)
// sao permitidos quando casarem com a allowlist. Default: false.
func (a *Allowlist) SetAllowLoopback(allow bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.allowLoopback = allow
}

// AllowLoopback retorna se loopback esta habilitado por politica.
func (a *Allowlist) AllowLoopback() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.allowLoopback
}

// SetResolver define o resolver DNS utilizado por ResolveAndValidate e DialAllowed.
func (a *Allowlist) SetResolver(r IPResolver) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r == nil {
		a.resolver = net.DefaultResolver
		return
	}
	a.resolver = r
}

// SetDialTimeout define o timeout de conexao TCP utilizado por DialAllowed.
func (a *Allowlist) SetDialTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dialTimeout = d
}

// AddExplicitEndpoint registra um par exato (IP, porta) permitido explicitamente
// (ex.: 127.0.0.1:3389 para sessoes RDP). IPs SSRF incondicionais (metadata,
// unspecified, multicast, broadcast) continuam proibidos.
func (a *Allowlist) AddExplicitEndpoint(ip netip.Addr, port int) error {
	if !ip.IsValid() {
		return fmt.Errorf("IP invalido")
	}
	ip = ip.Unmap()
	if isUnconditionalSSRF(ip) {
		return fmt.Errorf("IP bloqueado por politica SSRF: %s", ip)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("porta invalida: %d", port)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.explicitEndpoints[Endpoint{IP: ip, Port: port}] = struct{}{}
	return nil
}

// AllowRDPEndpoint permite exclusivamente 127.0.0.1:<port> (default 3389) para sessoes RDP.
func (a *Allowlist) AllowRDPEndpoint(port int) error {
	if port <= 0 {
		port = 3389
	}
	return a.AddExplicitEndpoint(netip.MustParseAddr("127.0.0.1"), port)
}

// IsEmpty retorna true se nao ha CIDRs, portas ou endpoints explicitos configurados.
func (a *Allowlist) IsEmpty() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.prefixes) == 0 && len(a.ports) == 0 && len(a.explicitEndpoints) == 0
}

// isUnconditionalSSRF bloqueia enderecos que nunca devem ser alcancados via proxy/tunel,
// mesmo com allowLoopback=true ou enabled=false.
func isUnconditionalSSRF(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return true
	}
	if addr.IsUnspecified() {
		return true
	}
	if addr.Is4() && ipv4UnspecifiedPrefix.Contains(addr) {
		return true
	}
	if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() {
		return true
	}
	if addr.IsMulticast() {
		return true
	}
	if addr == ipv4Broadcast || addr == awsIMDSv2IPv6 {
		return true
	}
	return false
}

// isSSRFBlocked aplica isUnconditionalSSRF mais o bloqueio default de loopback.
func isSSRFBlocked(addr netip.Addr, allowLoopback bool) bool {
	addr = addr.Unmap()
	if isUnconditionalSSRF(addr) {
		return true
	}
	if !allowLoopback && addr.IsLoopback() {
		return true
	}
	return false
}

func isLocalhostName(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

func cleanTargetHost(host string) (string, error) {
	h := strings.TrimSpace(host)
	if h == "" {
		return "", fmt.Errorf("host vazio")
	}
	if strings.Contains(h, "%") {
		return "", fmt.Errorf("zone ID IPv6 nao permitido no host")
	}
	if strings.HasPrefix(h, "[") || strings.HasSuffix(h, "]") {
		if !(strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]")) {
			return "", fmt.Errorf("colchetes IPv6 malformados em %q", host)
		}
		h = h[1 : len(h)-1]
	}
	if h == "" {
		return "", fmt.Errorf("host vazio")
	}
	return h, nil
}

type allowlistSnapshot struct {
	prefixes          []netip.Prefix
	ports             map[int]bool
	explicitEndpoints map[Endpoint]struct{}
	enabled           bool
	allowLoopback     bool
	resolver          IPResolver
	dialTimeout       time.Duration
}

func (a *Allowlist) snapshot() allowlistSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()

	prefixes := make([]netip.Prefix, len(a.prefixes))
	copy(prefixes, a.prefixes)

	ports := make(map[int]bool, len(a.ports))
	for k, v := range a.ports {
		ports[k] = v
	}

	explicit := make(map[Endpoint]struct{}, len(a.explicitEndpoints))
	for k := range a.explicitEndpoints {
		explicit[k] = struct{}{}
	}

	res := a.resolver
	if res == nil {
		res = net.DefaultResolver
	}
	timeout := a.dialTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return allowlistSnapshot{
		prefixes:          prefixes,
		ports:             ports,
		explicitEndpoints: explicit,
		enabled:           a.enabled,
		allowLoopback:     a.allowLoopback,
		resolver:          res,
		dialTimeout:       timeout,
	}
}

func (s allowlistSnapshot) validateIP(ip netip.Addr, port int) error {
	ip = ip.Unmap()
	if isUnconditionalSSRF(ip) {
		return fmt.Errorf("SSRF bloqueado: %s:%d", ip, port)
	}
	if _, ok := s.explicitEndpoints[Endpoint{IP: ip, Port: port}]; ok {
		return nil
	}
	if !s.allowLoopback && ip.IsLoopback() {
		return fmt.Errorf("SSRF bloqueado (loopback): %s:%d", ip, port)
	}
	if !s.enabled {
		return nil
	}
	if len(s.prefixes) == 0 {
		return fmt.Errorf("allowlist vazia: acesso bloqueado para %s:%d", ip, port)
	}
	if len(s.ports) > 0 && !s.ports[port] {
		return fmt.Errorf("porta %d nao permitida pela allowlist", port)
	}
	matched := false
	for _, p := range s.prefixes {
		if p.Contains(ip) {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("IP %s fora da allowlist", ip)
	}
	return nil
}

// ResolveAndValidate resolve o host UMA unica vez (se nao for literal IP) e
// valida TODOS os IPs retornados contra SSRF, endpoints explicitos e allowlist.
// Falha de forma fail-closed se QUALQUER IP resolvido violar a politica.
func (a *Allowlist) ResolveAndValidate(ctx context.Context, host string, port int) ([]netip.Addr, error) {
	if a == nil {
		return nil, fmt.Errorf("allowlist nil")
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("porta invalida: %d", port)
	}
	cleanHost, err := cleanTargetHost(host)
	if err != nil {
		return nil, err
	}

	snap := a.snapshot()

	var candidates []netip.Addr
	if addr, parseErr := netip.ParseAddr(cleanHost); parseErr == nil {
		candidates = []netip.Addr{addr.Unmap()}
	} else {
		if !snap.allowLoopback && isLocalhostName(cleanHost) {
			return nil, fmt.Errorf("SSRF bloqueado (localhost): %s:%d", cleanHost, port)
		}
		if ctx == nil {
			ctx = context.Background()
		}
		ips, lookupErr := snap.resolver.LookupIPAddr(ctx, cleanHost)
		if lookupErr != nil || len(ips) == 0 {
			if lookupErr != nil {
				return nil, fmt.Errorf("resolver %s: %w", cleanHost, lookupErr)
			}
			return nil, fmt.Errorf("resolver %s: nenhum IP retornado", cleanHost)
		}
		candidates = make([]netip.Addr, 0, len(ips))
		for _, ipa := range ips {
			addr, ok := netip.AddrFromSlice(ipa.IP)
			if !ok {
				return nil, fmt.Errorf("IP resolvido invalido para %s", cleanHost)
			}
			candidates = append(candidates, addr.Unmap())
		}
	}

	for _, ip := range candidates {
		if err := snap.validateIP(ip, port); err != nil {
			return nil, err
		}
	}
	return candidates, nil
}

// IsAllowed verifica se host:port e permitido (validando TODOS os IPs resolvidos
// e bloqueando vetores SSRF).
func (a *Allowlist) IsAllowed(host string, port int) bool {
	if a == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.ResolveAndValidate(ctx, host, port)
	return err == nil
}

// ContainsIP verifica se um IP esta permitido pela politica de rede/SSRF
// (sem restricao de porta).
func (a *Allowlist) ContainsIP(ip net.IP) bool {
	if a == nil || ip == nil {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()

	snap := a.snapshot()
	if isSSRFBlocked(addr, snap.allowLoopback) {
		return false
	}
	if !snap.enabled {
		return true
	}
	if len(snap.prefixes) == 0 {
		return false
	}
	for _, p := range snap.prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// DialAllowed executa resolucao unica (anti-TOCTOU) + validacao de todos os IPs
// e disca diretamente para o(s) IP(s) validado(s) sem nova consulta DNS.
func (a *Allowlist) DialAllowed(ctx context.Context, network, addr string) (net.Conn, error) {
	if a == nil {
		return nil, fmt.Errorf("allowlist nil")
	}
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("rede nao suportada: %s", network)
	}

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("endereco invalido %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("porta invalida %q", portStr)
	}

	addrs, err := a.ResolveAndValidate(ctx, host, port)
	if err != nil {
		return nil, err
	}

	snap := a.snapshot()
	dialer := net.Dialer{Timeout: snap.dialTimeout}
	var errs []error
	for _, ip := range addrs {
		if network == "tcp4" && !ip.Is4() {
			continue
		}
		if network == "tcp6" && !ip.Is6() {
			continue
		}
		target := net.JoinHostPort(ip.String(), portStr)
		conn, dialErr := dialer.DialContext(ctx, network, target)
		if dialErr == nil {
			return conn, nil
		}
		errs = append(errs, dialErr)
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("nenhum IP compativel com %s para %s", network, addr)
	}
	return nil, errors.Join(errs...)
}

// DialTCPAllowed e um atalho para tunelamento TCP/RDP reutilizando DialAllowed.
func DialTCPAllowed(ctx context.Context, allowlist *Allowlist, host string, port int) (net.Conn, error) {
	if allowlist == nil {
		return nil, fmt.Errorf("allowlist nil")
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("porta invalida: %d", port)
	}
	return allowlist.DialAllowed(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}
