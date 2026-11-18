package remotesession

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"discovery/app/core/netproxy"
)

// RemoteSessionKind espelha DiscoveryRMM_API (Discovery.Core.Enums.RemoteSessionKind),
// preservando All=4 e Processes=5 ja persistidos no Postgres e adicionando Rdp=6.
// No wire NATS, o campo "kind" e sempre string canonica em minusculas.
type RemoteSessionKind int

const (
	RemoteSessionKindScreen    RemoteSessionKind = 0
	RemoteSessionKindTerminal  RemoteSessionKind = 1
	RemoteSessionKindFiles     RemoteSessionKind = 2
	RemoteSessionKindProxy     RemoteSessionKind = 3
	RemoteSessionKindAll       RemoteSessionKind = 4
	RemoteSessionKindProcesses RemoteSessionKind = 5
	RemoteSessionKindRdp       RemoteSessionKind = 6
)

const (
	KindScreen    = "screen"
	KindTerminal  = "terminal"
	KindFiles     = "files"
	KindProcesses = "processes"
	KindProxy     = "proxy"
	KindRdp       = "rdp"
	KindAll       = "all"
)

// RemoteSessionRights espelha a mascara de permissoes/restricoes da sessao remota.
type RemoteSessionRights uint32

const (
	RightsNone         RemoteSessionRights = 0
	RightsViewOnly     RemoteSessionRights = 1
	RightsNoTerminal   RemoteSessionRights = 2
	RightsNoFiles      RemoteSessionRights = 4
	RightsLimitedInput RemoteSessionRights = 8
	RightsNoDesktop    RemoteSessionRights = 16
	RightsNoEvents     RemoteSessionRights = 32
	RightsNoProxy      RemoteSessionRights = 64
	RightsNoRdp        RemoteSessionRights = 128
)

// Has retorna true se a flag estiver presente na mascara.
func (r RemoteSessionRights) Has(flag RemoteSessionRights) bool {
	return r&flag != 0
}

// ParseRemoteSessionKind normaliza kind em string canonica ou inteiro (0..6).
func ParseRemoteSessionKind(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(v))
	case int:
		return remoteSessionKindIntToString(RemoteSessionKind(v))
	case int64:
		return remoteSessionKindIntToString(RemoteSessionKind(v))
	case float64:
		return remoteSessionKindIntToString(RemoteSessionKind(int(v)))
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return remoteSessionKindIntToString(RemoteSessionKind(n))
		}
		return strings.ToLower(strings.TrimSpace(v.String()))
	default:
		return ""
	}
}

func remoteSessionKindIntToString(k RemoteSessionKind) string {
	switch k {
	case RemoteSessionKindScreen:
		return KindScreen
	case RemoteSessionKindTerminal:
		return KindTerminal
	case RemoteSessionKindFiles:
		return KindFiles
	case RemoteSessionKindProxy:
		return KindProxy
	case RemoteSessionKindAll:
		return KindAll
	case RemoteSessionKindProcesses:
		return KindProcesses
	case RemoteSessionKindRdp:
		return KindRdp
	default:
		return ""
	}
}

// ParseRemoteSessionRights aceita numero, string separada por virgula ou slice de flags.
// Quando omitido/nil, retorna RightsNone (0).
func ParseRemoteSessionRights(raw any) RemoteSessionRights {
	if raw == nil {
		return RightsNone
	}
	switch v := raw.(type) {
	case int:
		return RemoteSessionRights(v)
	case int32:
		return RemoteSessionRights(v)
	case int64:
		return RemoteSessionRights(v)
	case uint32:
		return RemoteSessionRights(v)
	case float64:
		return RemoteSessionRights(uint32(v))
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return RemoteSessionRights(uint32(n))
		}
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return RightsNone
		}
		if n, err := strconv.ParseUint(s, 10, 32); err == nil {
			return RemoteSessionRights(uint32(n))
		}
		var out RemoteSessionRights
		for _, part := range strings.Split(s, ",") {
			out |= parseRightName(part)
		}
		return out
	case []string:
		var out RemoteSessionRights
		for _, item := range v {
			out |= ParseRemoteSessionRights(item)
		}
		return out
	case []any:
		var out RemoteSessionRights
		for _, item := range v {
			out |= ParseRemoteSessionRights(item)
		}
		return out
	}
	return RightsNone
}

func parseRightName(name string) RemoteSessionRights {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "none":
		return RightsNone
	case "viewonly", "view_only":
		return RightsViewOnly
	case "noterminal", "no_terminal":
		return RightsNoTerminal
	case "nofiles", "no_files":
		return RightsNoFiles
	case "limitedinput", "limited_input":
		return RightsLimitedInput
	case "nodesktop", "no_desktop":
		return RightsNoDesktop
	case "noevents", "no_events":
		return RightsNoEvents
	case "noproxy", "no_proxy":
		return RightsNoProxy
	case "nordp", "no_rdp":
		return RightsNoRdp
	default:
		return RightsNone
	}
}

// DefaultProxyMaxResponseBytes espelha RemoteAccessProxyOptions.MaxResponseBytes (10 MiB).
const DefaultProxyMaxResponseBytes int64 = 10 * 1024 * 1024

// ProxyPolicy representa a politica efetiva de proxy/tunel extraida do payload de start.
type ProxyPolicy struct {
	Allowlist          []string
	AllowedPorts       []int
	MaxResponseBytes   int64
	AllowLoopback      bool
	InsecureSkipVerify bool
	RdpHost            string
	RdpPort            int // 0 => resolver via preflight Windows (fallback 3389)
}

// ParseProxyPolicy extrai a politica fail-closed de proxy/rdp do payload de start.
func ParseProxyPolicy(payload map[string]any, kind string) ProxyPolicy {
	policy := ProxyPolicy{
		MaxResponseBytes: DefaultProxyMaxResponseBytes,
		AllowLoopback:    false,
		RdpHost:          "127.0.0.1",
		RdpPort:          0,
	}
	if payload == nil {
		return policy
	}

	var proxyMap map[string]any
	if pm, ok := payload["proxy"].(map[string]any); ok {
		proxyMap = pm
	}
	if proxyMap != nil {
		policy.Allowlist = parseStringSlice(proxyMap["allowlist"])
		if len(policy.Allowlist) == 0 {
			policy.Allowlist = parseStringSlice(proxyMap["defaultAllowlist"])
		}
		policy.AllowedPorts = parsePortSlice(proxyMap["allowedPorts"])
		if mb := toInt64Policy(proxyMap["maxResponseBytes"], 0); mb > 0 {
			policy.MaxResponseBytes = mb
		}
		if lb, ok := proxyMap["allowLoopback"].(bool); ok {
			policy.AllowLoopback = lb
		}
		if skip, ok := proxyMap["insecureSkipVerify"].(bool); ok {
			policy.InsecureSkipVerify = skip
		}
		if rh, ok := proxyMap["rdpHost"].(string); ok && strings.TrimSpace(rh) != "" {
			policy.RdpHost = strings.TrimSpace(rh)
		}
		if rp := toIntPolicy(proxyMap["rdpPort"], 0); rp >= 1 && rp <= 65535 {
			policy.RdpPort = rp
		}
	}

	if rdpMap, ok := payload["rdp"].(map[string]any); ok {
		if rh, ok := rdpMap["host"].(string); ok && strings.TrimSpace(rh) != "" {
			policy.RdpHost = strings.TrimSpace(rh)
		}
		if rh, ok := rdpMap["rdpHost"].(string); ok && strings.TrimSpace(rh) != "" {
			policy.RdpHost = strings.TrimSpace(rh)
		}
		if rp := toIntPolicy(rdpMap["port"], 0); rp >= 1 && rp <= 65535 {
			policy.RdpPort = rp
		}
		if rp := toIntPolicy(rdpMap["rdpPort"], 0); rp >= 1 && rp <= 65535 {
			policy.RdpPort = rp
		}
	}

	if rh, ok := payload["rdpHost"].(string); ok && strings.TrimSpace(rh) != "" {
		policy.RdpHost = strings.TrimSpace(rh)
	}
	if rp := toIntPolicy(payload["rdpPort"], 0); rp >= 1 && rp <= 65535 {
		policy.RdpPort = rp
	}

	if kind == KindRdp && policy.RdpHost == "" {
		policy.RdpHost = "127.0.0.1"
	}

	return policy
}

// BuildProxyAllowlist constroi uma *netproxy.Allowlist fail-closed a partir da politica.
// Para sessoes "rdp", trava exclusivamente em 127.0.0.1:<resolvedRdpPort>.
func BuildProxyAllowlist(policy ProxyPolicy, kind string, resolvedRdpPort int) (*netproxy.Allowlist, error) {
	al := netproxy.NewAllowlist()
	if strings.EqualFold(strings.TrimSpace(kind), KindRdp) {
		port := policy.RdpPort
		if port <= 0 {
			port = resolvedRdpPort
		}
		if port <= 0 || port > 65535 {
			port = 3389
		}
		if err := al.AllowRDPEndpoint(port); err != nil {
			return nil, fmt.Errorf("configurar endpoint RDP: %w", err)
		}
		return al, nil
	}

	al.SetAllowLoopback(policy.AllowLoopback)
	for _, entry := range policy.Allowlist {
		if err := al.AddEntry(entry); err != nil {
			return nil, fmt.Errorf("allowlist invalida %q: %w", entry, err)
		}
	}
	for _, port := range policy.AllowedPorts {
		al.AddPort(port)
	}
	return al, nil
}

func parseStringSlice(raw any) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func parsePortSlice(raw any) []int {
	switch v := raw.(type) {
	case []int:
		var out []int
		for _, p := range v {
			if p >= 1 && p <= 65535 {
				out = append(out, p)
			}
		}
		return out
	case []any:
		var out []int
		for _, item := range v {
			if p := toIntPolicy(item, 0); p >= 1 && p <= 65535 {
				out = append(out, p)
			}
		}
		return out
	default:
		return nil
	}
}

func toIntPolicy(v any, defaultVal int) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case json.Number:
		if n, err := val.Int64(); err == nil {
			return int(n)
		}
	}
	return defaultVal
}

func toInt64Policy(v any, defaultVal int64) int64 {
	switch val := v.(type) {
	case int:
		return int64(val)
	case int64:
		return val
	case float64:
		return int64(val)
	case json.Number:
		if n, err := val.Int64(); err == nil {
			return n
		}
	}
	return defaultVal
}
