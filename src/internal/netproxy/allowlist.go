package netproxy

import (
	"net"
	"strings"
	"sync"
)

// Allowlist controla quais hosts/portas podem ser acessados via proxy.
// Vazia por padrao = bloqueio total.
type Allowlist struct {
	mu       sync.RWMutex
	cidrs    []*net.IPNet
	ports    map[int]bool
	enabled  bool
}

// NewAllowlist cria uma allowlist vazia (bloqueio total).
func NewAllowlist() *Allowlist {
	return &Allowlist{
		ports:   make(map[int]bool),
		enabled: true,
	}
}

// AddCIDR adiciona uma rede permitida (ex: 192.168.0.0/16).
func (a *Allowlist) AddCIDR(cidr string) error {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cidrs = append(a.cidrs, ipnet)
	return nil
}

// AddPort adiciona uma porta permitida.
func (a *Allowlist) AddPort(port int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ports[port] = true
}

// SetEnabled ativa/desativa a allowlist.
func (a *Allowlist) SetEnabled(enabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.enabled = enabled
}

// IsAllowed verifica se um host:port e permitido.
func (a *Allowlist) IsAllowed(host string, port int) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if !a.enabled {
		return true // allowlist desativada = permite tudo
	}

	// Se allowlist vazia e ativa = bloqueio total
	if len(a.cidrs) == 0 && len(a.ports) == 0 {
		return false
	}

	// Verifica portas
	if len(a.ports) > 0 && !a.ports[port] {
		return false
	}

	// Verifica CIDRs
	if len(a.cidrs) == 0 {
		return true
	}

	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		// Tenta resolver hostname
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return false
		}
		ip = ips[0]
	}

	for _, cidr := range a.cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}

	return false
}

// IsEmpty retorna true se a allowlist esta vazia.
func (a *Allowlist) IsEmpty() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.cidrs) == 0 && len(a.ports) == 0
}

// Ensure imports
var _ = net.IP{}
