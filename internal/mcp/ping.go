package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// PingResult is the structured response returned by the ping_host tool.
type PingResult struct {
	Success    bool   `json:"success"`
	DurationMs int    `json:"durationMs"`
	Output     string `json:"output"`
}

// FlushDNSResult is the structured response returned by the flush_dns tool.
type FlushDNSResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
}

var privateNets []*net.IPNet
var privateNetsOnce sync.Once

func initPrivateNets() {
	cidrs := []string{
		"127.0.0.0/8",    // loopback IPv4
		"::1/128",        // loopback IPv6
		"10.0.0.0/8",     // private IPv4
		"172.16.0.0/12",  // private IPv4
		"192.168.0.0/16", // private IPv4
		"169.254.0.0/16", // link-local IPv4
		"fe80::/10",      // link-local IPv6
		"fc00::/7",       // unique local IPv6
	}
	for _, cidr := range cidrs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		privateNets = append(privateNets, n)
	}
}

func isAllowedIP(ip net.IP) bool {
	privateNetsOnce.Do(initPrivateNets)
	if ip == nil {
		return false
	}
	for _, n := range privateNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateLocalHostOrIP rejects non-hostname input and ensures resolved IPs belong to a
// local/private range (RFC1918, loopback, link-local, ULA).
func ValidateLocalHostOrIP(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return errors.New("host nao pode ser vazio")
	}
	if strings.ContainsAny(host, "\n\r\t") {
		return errors.New("host invalido")
	}

	// If it's an IP literal, validate directly.
	if ip := net.ParseIP(host); ip != nil {
		if !isAllowedIP(ip) {
			return fmt.Errorf("ip nao permitido: %s", host)
		}
		return nil
	}

	// If it contains a port, reject (we only support bare hostname/IP).
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		// IPv6 literals can contain colons but are valid IPs (handled above).
		return errors.New("host nao pode conter porta")
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("falha ao resolver host: %w", err)
	}
	if len(ips) == 0 {
		return errors.New("nao foi possivel resolver nenhum IP")
	}
	for _, ip := range ips {
		if isAllowedIP(ip) {
			return nil
		}
	}
	return fmt.Errorf("nenhum IP resolvido pertence a rede privada")
}

func buildPingCommand(host string, count int, timeoutSeconds int) (*exec.Cmd, error) {
	if count <= 0 {
		count = 1
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 5
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// -n: count, -w: timeout in ms
		cmd = exec.Command("ping", "-n", fmt.Sprint(count), "-w", fmt.Sprint(timeoutSeconds*1000), host)
	} else {
		// -c: count, -W: timeout in seconds (per packet) on Linux/BSD.
		cmd = exec.Command("ping", "-c", fmt.Sprint(count), "-W", fmt.Sprint(timeoutSeconds), host)
	}
	return cmd, nil
}

// PingHost executes a ping to the given host (IP or hostname) and returns structured output.
// It only allows hosts/IPs on private/local networks.
func PingHost(ctx context.Context, host string, count int, timeoutSeconds int) (PingResult, error) {
	if err := ValidateLocalHostOrIP(host); err != nil {
		return PingResult{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds+2)*time.Second)
	defer cancel()

	cmd, err := buildPingCommand(host, count, timeoutSeconds)
	if err != nil {
		return PingResult{}, err
	}
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)

	start := time.Now()
	out, err := cmd.CombinedOutput()
	duration := time.Since(start)

	result := PingResult{
		Success:    err == nil,
		DurationMs: int(duration.Milliseconds()),
		Output:     strings.TrimSpace(string(out)),
	}
	return result, nil
}

func buildFlushDNSCommand() (*exec.Cmd, error) {
	if runtime.GOOS == "windows" {
		return exec.Command("ipconfig", "/flushdns"), nil
	}
	if runtime.GOOS == "darwin" {
		// macOS: reload mDNSResponder
		return exec.Command("killall", "-HUP", "mDNSResponder"), nil
	}
	// Linux: prefer resolvectl, fall back to systemd-resolve
	if _, err := exec.LookPath("resolvectl"); err == nil {
		return exec.Command("resolvectl", "flush-caches"), nil
	}
	if _, err := exec.LookPath("systemd-resolve"); err == nil {
		return exec.Command("systemd-resolve", "--flush-caches"), nil
	}
	return nil, errors.New("nenhum comando de flush DNS disponivel neste sistema")
}

// FlushDNS attempts to clear the system DNS cache.
func FlushDNS(ctx context.Context) (FlushDNSResult, error) {
	cmd, err := buildFlushDNSCommand()
	if err != nil {
		return FlushDNSResult{}, err
	}
	// Limit runtime so it doesn't hang indefinitely.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	out, err := cmd.CombinedOutput()
	return FlushDNSResult{
		Success: err == nil,
		Output:  strings.TrimSpace(string(out)),
	}, err
}
