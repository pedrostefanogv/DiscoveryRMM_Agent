package mcp

import (
	"context"
	"fmt"
	"net"
	"time"
)

// adaptersConfigScript le a configuracao de IP, mascara, gateway e DNS dos
// adaptadores. Script estatico (sem entrada do usuario).
const adaptersConfigScript = `$ErrorActionPreference = 'Stop'
$configs = @(Get-NetIPConfiguration | ForEach-Object {
  [PSCustomObject]@{
    interfaceAlias        = $_.InterfaceAlias
    interfaceIndex        = $_.InterfaceIndex
    interfaceDescription  = $_.InterfaceDescription
    ipv4Address           = @($_.IPv4Address | ForEach-Object { $_.IPAddress })
    ipv4PrefixLength      = @($_.IPv4Address | ForEach-Object { $_.PrefixLength })
    ipv4Gateway           = @($_.IPv4DefaultGateway | ForEach-Object { $_.NextHop })
    dnsServers            = @($_.DNSServer | ForEach-Object { $_.ServerAddresses })
  }
})
$configs | ConvertTo-Json -Compress -Depth 4`

// registerNetworkDiagnosticsTool registra a tool "network_diagnostics", que
// consolida ping_host e flush_dns e adiciona test_connection, resolve,
// connections e config.
func registerNetworkDiagnosticsTool(reg *Registry) {
	reg.Register(Tool{
		Name: "network_diagnostics",
		Description: "Diagnostico de rede (familia action-based). action: " +
			"ping (host, count, timeoutSeconds; apenas rede privada) | " +
			"flush_dns (limpa o cache DNS) | " +
			"test_connection (host e port; teste TCP, apenas rede privada/local) | " +
			"resolve (host; DNS) | " +
			"connections (conexoes TCP/UDP ativas com PID e estado) | " +
			"config (adaptadores: IP, mascara, gateway e DNS).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: ping, flush_dns, test_connection, resolve, connections, config", Required: true},
			{Name: "host", Type: "string", Description: "Host ou IP (ping, test_connection, resolve)", Required: false},
			{Name: "count", Type: "integer", Description: "Numero de pacotes ping (padrao 1)", Required: false},
			{Name: "timeoutSeconds", Type: "integer", Description: "Timeout em segundos (ping: padrao 5; test_connection: padrao 3)", Required: false},
			{Name: "port", Type: "integer", Description: "Porta TCP do teste (test_connection)", Required: false},
			{Name: "limit", Type: "integer", Description: "Limite de conexoes retornadas (padrao 200)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "ping", "flush_dns", "test_connection", "resolve", "connections", "config")
			if err != nil {
				return nil, err
			}
			switch action {
			case "ping":
				host, err := requiredStringArg(args, "host")
				if err != nil {
					return nil, err
				}
				return PingHost(ctx, host, optionalIntArg(args, "count"), optionalIntArg(args, "timeoutSeconds"))
			case "flush_dns":
				return FlushDNS(ctx)
			case "test_connection":
				host, err := requiredStringArg(args, "host")
				if err != nil {
					return nil, err
				}
				port := optionalIntArg(args, "port")
				if port <= 0 || port > 65535 {
					return nil, fmt.Errorf("port invalida: informe uma porta TCP entre 1 e 65535")
				}
				// Mesma politica do ping: apenas rede privada/local. Sem isso a tool
				// viraria um scanner TCP para qualquer host da internet.
				if err := ValidateLocalHostOrIP(host); err != nil {
					return nil, err
				}
				return testTCPConnection(ctx, host, port, optionalIntArg(args, "timeoutSeconds"))
			case "resolve":
				host, err := requiredStringArg(args, "host")
				if err != nil {
					return nil, err
				}
				addresses, err := net.LookupHost(host)
				if err != nil {
					return nil, fmt.Errorf("falha ao resolver %q: %w", host, err)
				}
				return map[string]any{"host": host, "addresses": addresses}, nil
			case "connections":
				return connectionsResult(optionalIntArg(args, "limit")), nil
			case "config":
				return runPowerShell(ctx, adaptersConfigScript, 90*time.Second), nil
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}

// connectionsResult agrega TCP+UDP respeitando o limite (padrao 200).
func connectionsResult(limit int) map[string]any {
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}
	conns := connectionsNative(limit)
	return map[string]any{
		"count":       len(conns),
		"limit":       limit,
		"connections": conns,
	}
}

// testTCPConnection tenta abrir uma conexao TCP com timeout e devolve o
// resultado estruturado (nunca erra por host inacessivel — reachable=false).
func testTCPConnection(ctx context.Context, host string, port, timeoutSeconds int) (map[string]any, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 3
	}
	if timeoutSeconds > 30 {
		timeoutSeconds = 30
	}
	dialCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	addr := net.JoinHostPort(host, fmt.Sprint(port))
	start := time.Now()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", addr)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return map[string]any{
			"host":       host,
			"port":       port,
			"reachable":  false,
			"error":      err.Error(),
			"durationMs": elapsed,
		}, nil
	}
	_ = conn.Close()
	return map[string]any{
		"host":       host,
		"port":       port,
		"reachable":  true,
		"durationMs": elapsed,
	}, nil
}
