//go:build windows

package app

import (
	"fmt"
	"net"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// RunNativePreflightChecks executa as pré-checas NATIVAS (sem PSADT).
func (a *App) RunNativePreflightChecks() NativePreflightResult {
	result := NativePreflightResult{CheckedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	a.Logs.Append("[notify-debug] executando preflight checks nativos...")

	result.OSName = "Windows"
	result.OSVersion = windowsVersionNative()
	result.Architecture = runtime.GOARCH

	result.IsAdmin = isAdminNative()
	result.RebootPending = preflightRebootPending()
	result.NetworkAvailable = preflightNetworkOnline()

	result.Success = true
	a.Logs.Append(fmt.Sprintf("[notify-debug] preflight [OK] admin=%t reboot=%t online=%t", result.IsAdmin, result.RebootPending, result.NetworkAvailable))
	return result
}

// isAdminNative verifica o token do processo (elevação).
func isAdminNative() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// windowsVersionNative lê a versão do Windows do registro.
func windowsVersionNative() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, "SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion", registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	display, _, _ := key.GetStringValue("DisplayVersion")
	build, _, _ := key.GetStringValue("CurrentBuildNumber")
	parts := []string{}
	if strings.TrimSpace(build) != "" {
		parts = append(parts, strings.TrimSpace(build))
	}
	if strings.TrimSpace(display) != "" {
		parts = append(parts, strings.TrimSpace(display))
	}
	return strings.Join(parts, " ")
}

// preflightRebootPending: mesma checagem do executor nativo (CBS/WU/
// PendingFileRenameOperations), reimplementada aqui porque os helpers do
// executor são internos ao pacote automation.
func preflightRebootPending() bool {
	keys := []string{
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\WindowsUpdate\\Auto Update\\RebootRequired",
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Component Based Servicing\\RebootPending",
	}
	for _, path := range keys {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
		if err == nil {
			key.Close()
			return true
		}
	}
	sm, err := registry.OpenKey(registry.LOCAL_MACHINE, "SYSTEM\\CurrentControlSet\\Control\\Session Manager", registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer sm.Close()
	vals, err := sm.ReadValueNames(-1)
	if err != nil {
		return false
	}
	for _, name := range vals {
		if strings.EqualFold(name, "PendingFileRenameOperations") {
			return true
		}
	}
	return false
}

// preflightNetworkOnline sonda DNS públicos.
func preflightNetworkOnline() bool {
	probes := []string{"1.1.1.1:53", "8.8.8.8:53"}
	for _, addr := range probes {
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}