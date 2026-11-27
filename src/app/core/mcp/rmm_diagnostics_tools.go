package mcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func boolPtr(v bool) *bool {
	return &v
}

func readOnlyAnnotations() *ToolAnnotations {
	return &ToolAnnotations{
		ReadOnlyHint:    boolPtr(true),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(true),
	}
}

func registerRmmDiagnosticsTools(reg *Registry) {
	// 1. vpn_and_wifi
	reg.Register(Tool{
		Name: "vpn_and_wifi",
		Description: "Diagnóstico somente leitura de conexões VPN (perfis rasdial/VPN nativas e adaptadores VPN) " +
			"e Wi-Fi (SSID, sinal, canal, BSSID, gateway e DNS) no Windows. Use quando o usuário relatar falha de VPN, queda de Wi-Fi, sinal fraco ou DNS errado.",
		Annotations: readOnlyAnnotations(),
		Params: []ToolParam{
			{
				Name:        "scope",
				Type:        "string",
				Description: "Escopo da coleta: 'all' (padrão), 'vpn' ou 'wifi'.",
				Required:    false,
				Enum:        []string{"all", "vpn", "wifi"},
				Default:     "all",
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			scope := strings.ToLower(optionalStringArg(args, "scope"))
			if scope == "" {
				scope = "all"
			}
			if scope != "all" && scope != "vpn" && scope != "wifi" {
				return nil, fmt.Errorf("scope invalido: use all, vpn ou wifi")
			}

			script := fmt.Sprintf(`
$scope = '%s'
$out = [ordered]@{ scope = $scope }
if ($scope -eq 'all' -or $scope -eq 'vpn') {
  $vpnConns = @(Get-VpnConnection -ErrorAction SilentlyContinue | Select-Object Name, ServerAddress, ConnectionStatus, TunnelType, SplitTunneling)
  $vpnAdapters = @(Get-NetAdapter -ErrorAction SilentlyContinue | Where-Object { $_.InterfaceDescription -match 'VPN|WireGuard|OpenVPN|Cisco|AnyConnect|Fortinet|GlobalProtect|TAP|TUN' -or $_.Name -match 'VPN|WireGuard|OpenVPN' } | Select-Object Name, InterfaceDescription, Status, LinkSpeed)
  $out.vpn = [ordered]@{ connections = $vpnConns; adapters = $vpnAdapters }
}
if ($scope -eq 'all' -or $scope -eq 'wifi') {
  $wlan = (netsh wlan show interfaces 2>&1) -join "`+"`n"+`"
  $dns = @(Get-DnsClientServerAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.ServerAddresses.Count -gt 0 } | Select-Object InterfaceAlias, ServerAddresses)
  $out.wifi = [ordered]@{ wlanInterfaces = $wlan; dnsServers = $dns }
}
$out | ConvertTo-Json -Depth 5 -Compress
`, scope)
			return runPowerShell(ctx, script, 25*time.Second), nil
		},
	})

	// 2. startup_and_browser_health
	reg.Register(Tool{
		Name: "startup_and_browser_health",
		Description: "Diagnóstico somente leitura de programas na inicialização do Windows (Startup/Run) e " +
			"saúde básica de navegadores (tamanho de cache/perfis Chrome, Edge e Firefox). Use quando houver boot lento, navegador travando ou disco cheio por cache.",
		Annotations: readOnlyAnnotations(),
		Params: []ToolParam{
			{
				Name:        "includeBrowsers",
				Type:        "boolean",
				Description: "Se true (padrão), mede tamanho aproximado dos perfis/caches de Chrome, Edge e Firefox.",
				Required:    false,
				Default:     true,
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			includeBrowsers := true
			if _, ok := args["includeBrowsers"]; ok {
				includeBrowsers = optionalBoolArg(args, "includeBrowsers")
			}
			flag := "$true"
			if !includeBrowsers {
				flag = "$false"
			}
			script := fmt.Sprintf(`
$includeBrowsers = %s
$startup = @(Get-CimInstance Win32_StartupCommand -ErrorAction SilentlyContinue | Select-Object -First 40 Name, Command, Location, User)
$out = [ordered]@{ startupItems = $startup }
if ($includeBrowsers) {
  $paths = @(
    @{ browser = 'Chrome'; path = "$env:LOCALAPPDATA\Google\Chrome\User Data" },
    @{ browser = 'Edge'; path = "$env:LOCALAPPDATA\Microsoft\Edge\User Data" },
    @{ browser = 'Firefox'; path = "$env:APPDATA\Mozilla\Firefox\Profiles" }
  )
  $browsers = @()
  foreach ($p in $paths) {
    if (Test-Path $p.path) {
      $bytes = (Get-ChildItem -Path $p.path -Recurse -File -ErrorAction SilentlyContinue | Select-Object -First 2000 | Measure-Object -Property Length -Sum).Sum
      $browsers += [ordered]@{ browser = $p.browser; exists = $true; sampledSizeMB = [math]::Round(($bytes / 1MB), 1) }
    } else {
      $browsers += [ordered]@{ browser = $p.browser; exists = $false; sampledSizeMB = 0 }
    }
  }
  $out.browsers = $browsers
}
$out | ConvertTo-Json -Depth 4 -Compress
`, flag)
			return runPowerShell(ctx, script, 30*time.Second), nil
		},
	})

	// 3. network_http_probe
	reg.Register(Tool{
		Name: "network_http_probe",
		Description: "Sonda HTTP/HTTPS somente leitura para testar disponibilidade de URL externa pública, " +
			"status code, TTFB e validade do certificado TLS. Bloqueia IPs privados/loopback/link-local (anti-SSRF). Use para validar acesso a sites/portais públicos.",
		Annotations: &ToolAnnotations{
			ReadOnlyHint:    boolPtr(true),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(true),
			OpenWorldHint:   boolPtr(true),
		},
		Params: []ToolParam{
			{
				Name:        "url",
				Type:        "string",
				Description: "URL HTTP ou HTTPS pública a testar (ex.: 'https://portal.office.com').",
				Required:    true,
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			rawURL, err := requiredStringArg(args, "url")
			if err != nil {
				return nil, err
			}
			parsed, err := url.Parse(strings.TrimSpace(rawURL))
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
				return nil, fmt.Errorf("url invalida: informe http:// ou https:// com hostname valido")
			}

			host := parsed.Hostname()
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil || len(ips) == 0 {
				return nil, fmt.Errorf("falha ao resolver DNS de %s: %v", host, err)
			}
			for _, addr := range ips {
				ip := addr.IP
				if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
					return nil, fmt.Errorf("destino bloqueado por politica anti-SSRF (IP privado ou reservado)")
				}
			}

			probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()

			dialer := &net.Dialer{Timeout: 8 * time.Second}
			transport := &http.Transport{
				DialContext: func(dCtx context.Context, network, address string) (net.Conn, error) {
					_, port, splitErr := net.SplitHostPort(address)
					if splitErr != nil {
						return nil, splitErr
					}
					return dialer.DialContext(dCtx, network, net.JoinHostPort(ips[0].IP.String(), port))
				},
			}
			client := &http.Client{
				Transport: transport,
				Timeout:   15 * time.Second,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}

			req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, parsed.String(), nil)
			if err != nil {
				return nil, err
			}
			start := time.Now()
			resp, err := client.Do(req)
			ttfbMs := time.Since(start).Milliseconds()
			if err != nil {
				return map[string]any{
					"ok":     false,
					"url":    parsed.String(),
					"ttfbMs": ttfbMs,
					"error":  err.Error(),
				}, nil
			}
			defer resp.Body.Close()

			res := map[string]any{
				"ok":         resp.StatusCode >= 200 && resp.StatusCode < 400,
				"url":        parsed.String(),
				"statusCode": resp.StatusCode,
				"ttfbMs":     ttfbMs,
				"resolvedIp": ips[0].IP.String(),
			}
			if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
				cert := resp.TLS.PeerCertificates[0]
				daysLeft := int(time.Until(cert.NotAfter).Hours() / 24)
				res["tls"] = map[string]any{
					"version":     tls.VersionName(resp.TLS.Version),
					"subject":     cert.Subject.CommonName,
					"expiresAt":   cert.NotAfter.UTC().Format(time.RFC3339),
					"daysToExpir": daysLeft,
				}
			}
			return res, nil
		},
	})

	// 4. office_diagnostics
	reg.Register(Tool{
		Name: "office_diagnostics",
		Description: "Diagnóstico somente leitura do Microsoft 365 / Office: versão instalada, tamanho de arquivos " +
			"OST/PST do Outlook, cache do Microsoft Teams e status básico do OneDrive. Use para lentidão no Outlook, caixa cheia, Teams travando ou falha no OneDrive.",
		Annotations: readOnlyAnnotations(),
		Params:      []ToolParam{},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			script := `
$officeReg = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Office\ClickToRun\Configuration' -ErrorAction SilentlyContinue
$ostFiles = @(Get-ChildItem "$env:LOCALAPPDATA\Microsoft\Outlook" -Filter '*.ost' -ErrorAction SilentlyContinue | Select-Object Name, @{N='sizeMB';E={[math]::Round($_.Length/1MB,1)}}, LastWriteTime)
$pstFiles = @(Get-ChildItem "$env:USERPROFILE\Documents\Outlook Files" -Filter '*.pst' -ErrorAction SilentlyContinue | Select-Object Name, @{N='sizeMB';E={[math]::Round($_.Length/1MB,1)}}, LastWriteTime)
$teamsRunning = @(Get-Process -Name 'ms-teams','Teams' -ErrorAction SilentlyContinue | Select-Object Name, @{N='memMB';E={[math]::Round($_.WorkingSet64/1MB,1)}})
$oneDriveRunning = @(Get-Process -Name 'OneDrive' -ErrorAction SilentlyContinue | Select-Object Name, @{N='memMB';E={[math]::Round($_.WorkingSet64/1MB,1)}})
[ordered]@{
  officeVersion = if ($officeReg) { $officeReg.VersionToReport } else { $null }
  officeChannel = if ($officeReg) { $officeReg.CDNBaseUrl } else { $null }
  outlookOst = $ostFiles
  outlookPst = $pstFiles
  teamsProcesses = $teamsRunning
  oneDriveProcesses = $oneDriveRunning
} | ConvertTo-Json -Depth 4 -Compress
`
			return runPowerShell(ctx, script, 25*time.Second), nil
		},
	})

	// 5. hardware_peripherals
	reg.Register(Tool{
		Name: "hardware_peripherals",
		Description: "Diagnóstico somente leitura de periféricos e energia: saúde/carga da bateria (notebooks), " +
			"monitores conectados, dispositivos de áudio/USB/Bluetooth com erro no PnP. Use para bateria descarregando, monitor, webcam/áudio ou USB com falha.",
		Annotations: readOnlyAnnotations(),
		Params:      []ToolParam{},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			script := `
$battery = @(Get-CimInstance Win32_Battery -ErrorAction SilentlyContinue | Select-Object Name, EstimatedChargeRemaining, BatteryStatus, DesignCapacity, FullChargeCapacity)
$monitors = @(Get-CimInstance Win32_DesktopMonitor -ErrorAction SilentlyContinue | Select-Object Name, ScreenWidth, ScreenHeight, Availability)
$pnpErrors = @(Get-CimInstance Win32_PnPEntity -ErrorAction SilentlyContinue | Where-Object { $_.ConfigManagerErrorCode -ne 0 } | Select-Object -First 25 Name, PNPClass, ConfigManagerErrorCode)
[ordered]@{
  batteries = $battery
  monitors = $monitors
  pnpDevicesWithError = $pnpErrors
} | ConvertTo-Json -Depth 4 -Compress
`
			return runPowerShell(ctx, script, 25*time.Second), nil
		},
	})
}
