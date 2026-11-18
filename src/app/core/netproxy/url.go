package netproxy

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

func parseAndValidateURL(rawURL string, allowedSchemes ...string) (*url.URL, string, int, error) {
	if len(allowedSchemes) == 0 {
		allowedSchemes = []string{"http", "https"}
	}

	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, "", 0, fmt.Errorf("URL vazia")
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			return nil, "", 0, fmt.Errorf("URL contem caracteres de controle ou espacos invalidos")
		}
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, "", 0, fmt.Errorf("URL invalida: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	schemeAllowed := false
	for _, s := range allowedSchemes {
		if scheme == strings.ToLower(s) {
			schemeAllowed = true
			break
		}
	}
	if !schemeAllowed {
		return nil, "", 0, fmt.Errorf("esquema nao permitido: %q", u.Scheme)
	}

	if u.Opaque != "" {
		return nil, "", 0, fmt.Errorf("URL opaca nao permitida")
	}
	if u.User != nil || strings.Contains(u.Host, "@") {
		return nil, "", 0, fmt.Errorf("userinfo na URL nao permitido")
	}
	if strings.Contains(u.Host, "\\") {
		return nil, "", 0, fmt.Errorf("barra invertida no host nao permitida")
	}
	if u.Host == "" {
		return nil, "", 0, fmt.Errorf("host ausente na URL")
	}
	if strings.Contains(u.Host, "%") {
		return nil, "", 0, fmt.Errorf("zone ID ou escape %% no host nao permitido")
	}

	hostname := u.Hostname()
	if hostname == "" {
		return nil, "", 0, fmt.Errorf("hostname ausente na URL")
	}
	if strings.Contains(hostname, "%") {
		return nil, "", 0, fmt.Errorf("zone ID IPv6 nao permitido")
	}

	var port int
	if strings.HasPrefix(u.Host, "[") {
		if strings.Contains(u.Host, "]:") {
			splitHost, portStr, splitErr := net.SplitHostPort(u.Host)
			if splitErr != nil || splitHost == "" || portStr == "" {
				return nil, "", 0, fmt.Errorf("host/porta IPv6 invalido em %q", u.Host)
			}
			p, convErr := strconv.Atoi(portStr)
			if convErr != nil || p < 1 || p > 65535 {
				return nil, "", 0, fmt.Errorf("porta invalida: %q", portStr)
			}
			port = p
		} else {
			if !strings.HasSuffix(u.Host, "]") {
				return nil, "", 0, fmt.Errorf("host IPv6 malformado em %q", u.Host)
			}
			if _, addrErr := netip.ParseAddr(hostname); addrErr != nil {
				return nil, "", 0, fmt.Errorf("endereco IPv6 invalido: %w", addrErr)
			}
			port = defaultPortForScheme(scheme)
		}
	} else if strings.Contains(u.Host, ":") {
		splitHost, portStr, splitErr := net.SplitHostPort(u.Host)
		if splitErr != nil || splitHost == "" || portStr == "" {
			return nil, "", 0, fmt.Errorf("host/porta invalido em %q", u.Host)
		}
		p, convErr := strconv.Atoi(portStr)
		if convErr != nil || p < 1 || p > 65535 {
			return nil, "", 0, fmt.Errorf("porta invalida: %q", portStr)
		}
		port = p
	} else {
		port = defaultPortForScheme(scheme)
	}

	u.Scheme = scheme
	return u, hostname, port, nil
}

func defaultPortForScheme(scheme string) int {
	switch strings.ToLower(scheme) {
	case "https", "wss":
		return 443
	default:
		return 80
	}
}

func validateHTTPMethod(method string) (string, error) {
	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" {
		return "GET", nil
	}
	switch m {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return m, nil
	default:
		return "", fmt.Errorf("metodo HTTP nao permitido: %q", m)
	}
}
