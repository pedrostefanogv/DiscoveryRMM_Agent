// Package status encapsula a lógica de formatação do snapshot de saúde
// do agente (Status tab), separado do App.
package status

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// DebugConfig é uma visão mínima da configuração de debug usada pelo status.
type DebugConfig struct {
	Server       string
	NatsWsServer string
	NatsServer   string
	ApiServer    string
	Scheme       string
}

// FirstServerCandidate retorna o primeiro candidato de servidor disponível.
func FirstServerCandidate(agentServer string, cfg DebugConfig) string {
	if server := strings.TrimSpace(agentServer); server != "" {
		return server
	}
	for _, candidate := range []string{cfg.Server, cfg.NatsWsServer, cfg.NatsServer, cfg.ApiServer} {
		if server := strings.TrimSpace(candidate); server != "" {
			return server
		}
	}
	return ""
}

// ResolveConnectionType resolve o tipo de conexão a partir do transporte e config.
func ResolveConnectionType(transport string, cfg DebugConfig) string {
	if mapped := MapTransportConnectionType(transport); mapped != "" {
		return mapped
	}

	if strings.TrimSpace(cfg.NatsWsServer) != "" && strings.TrimSpace(cfg.NatsServer) == "" {
		return "wss"
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Scheme), "nats") || strings.TrimSpace(cfg.NatsServer) != "" {
		return "nats"
	}
	if strings.TrimSpace(cfg.NatsWsServer) != "" {
		return "wss"
	}

	return "-"
}

// MapTransportConnectionType mapeia o tipo de transporte para um rótulo.
func MapTransportConnectionType(transport string) string {
	normalized := strings.ToLower(strings.TrimSpace(transport))
	switch normalized {
	case "":
		return ""
	case "nats":
		return "nats"
	case "nats-ws", "nats-wss", "ws", "wss":
		return "wss"
	default:
		if strings.Contains(normalized, "ws") {
			return "wss"
		}
		if strings.Contains(normalized, "nats") {
			return "nats"
		}
		return normalized
	}
}

// NormalizeServer normaliza um servidor para exibição.
func NormalizeServer(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil && strings.TrimSpace(parsed.Host) != "" {
			value = strings.TrimSpace(parsed.Host)
		}
	} else if strings.ContainsAny(value, "/?") {
		if parsed, err := url.Parse("//" + value); err == nil && strings.TrimSpace(parsed.Host) != "" {
			value = strings.TrimSpace(parsed.Host)
		}
	}

	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	} else if host, ok := splitHostAndNumericPort(value); ok {
		value = host
	}

	value = strings.TrimSpace(strings.Trim(value, "[]"))
	if value == "" {
		return strings.TrimSpace(raw)
	}
	return value
}

func splitHostAndNumericPort(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	idx := strings.LastIndex(trimmed, ":")
	if idx <= 0 || idx >= len(trimmed)-1 {
		return "", false
	}

	hostPart := strings.TrimSpace(trimmed[:idx])
	portPart := strings.TrimSpace(trimmed[idx+1:])
	if hostPart == "" || portPart == "" {
		return "", false
	}
	if strings.Contains(hostPart, ":") {
		return "", false
	}
	if _, err := strconv.Atoi(portPart); err != nil {
		return "", false
	}

	return hostPart, true
}
