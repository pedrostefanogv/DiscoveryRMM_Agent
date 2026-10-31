package agentconn

import (
	"net"
	"strings"
)

// cloudflareIPv4Prefixes são os prefixos IPv4 oficiais do Cloudflare
// (https://www.cloudflare.com/ips-v4). Um host de API que resolve para um IP
// desses prefixos está atrás do proxy Cloudflare: o NATS nativo bruto
// (nats://host:4222) NUNCA conecta (o Cloudflare só encaminha HTTP/HTTPS),
// e o dial custa um i/o timeout inteiro (3-8s) em toda sessão/reconexão.
var cloudflareIPv4Prefixes = []string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
}

// cloudflareIPv6Prefix é o prefixo IPv6 oficial do Cloudflare (ips-v6).
var cloudflareIPv6Prefix = mustCIDR("2606:4700::/32")

// isLikelyCloudflareHost resolve o host e verifica se algum IP pertence ao
// Cloudflare. Falha de DNS resolve para false (não pula o fallback — o dial
// pode até funcionar em LAN).
func isLikelyCloudflareHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	// O host pode ser um domínio do Cloudflare — não dá para saber sem DNS.
	// Deixa o DNS decidir: o custo é uma resolução, cacheada pelo sistema.
	if net.ParseIP(host) == nil {
		ips, err := net.LookupIP(host)
		if err != nil {
			return false
		}
		if len(ips) == 0 {
			return false
		}
		for _, ip := range ips {
			if isCloudflareIP(ip) {
				return true
			}
		}
		return false
	}
	return isCloudflareIP(net.ParseIP(host))
}

// mustCIDR converte um prefixo literal (constante) para *net.IPNet; erro
// impossível para os literais fixos deste arquivo — panica na inicialização
// se alguém digitar um prefixo inválido (falha rápida, melhor que silêncio).
func mustCIDR(cidr string) *net.IPNet {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		panic("agentconn: prefixo Cloudflare inválido: " + cidr)
	}
	return network
}

// cloudflareIPv4Networks pré-computa os prefixos (o pacote é importado por
// cada sessão de heartbeat — evitar re-parse por chamada).
var cloudflareIPv4Networks = func() []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(cloudflareIPv4Prefixes))
	for _, prefix := range cloudflareIPv4Prefixes {
		networks = append(networks, mustCIDR(prefix))
	}
	return networks
}()

func isCloudflareIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		for _, network := range cloudflareIPv4Networks {
			if network.Contains(ip4) {
				return true
			}
		}
		return false
	}
	if cloudflareIPv6Prefix != nil {
		return cloudflareIPv6Prefix.Contains(ip)
	}
	return false
}
