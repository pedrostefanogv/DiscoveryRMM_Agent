//go:build windows

package app

import (
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"discovery/app/core/selfupdate"
)

// agentUninstallRegistryKey é gravado pelo instalador NSIS
// (wails.writeUninstaller) e apagado no uninstall.
const agentUninstallRegistryKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Discovery.RMM`

// readUninstallerFromRegistry lê QuietUninstallString (preferido, já traz /S) e
// UninstallString. Sempre na view 64-bit: o agente é x64 e o instalador grava
// com SetRegView 64.
func readUninstallerFromRegistry() string {
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		agentUninstallRegistryKey,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer key.Close()

	for _, valueName := range []string{"QuietUninstallString", "UninstallString"} {
		raw, _, err := key.GetStringValue(valueName)
		if err != nil {
			continue
		}
		if path := parseUninstallerExecutable(raw); path != "" {
			return path
		}
	}
	return ""
}

// launchAgentUninstallerDetached lança o uninstaller NSIS silencioso como
// processo independente (CREATE_BREAKAWAY_FROM_JOB + DETACHED_PROCESS), fora da
// árvore do agente. Requer processo elevado — o handler já valida isso.
//
// O uninstaller executa "sc stop DiscoveryAgent" e só depois os taskkills;
// como o serviço (nosso processo) já encerrou nesse ponto, o /T do NSIS não
// alcança o próprio uninstaller.
func launchAgentUninstallerDetached(uninstallerPath string) (uint32, error) {
	pi, err := selfupdate.LaunchDetachedCreateProcess(uninstallerPath, "/S")
	if err != nil {
		return 0, fmt.Errorf("falha ao lançar uninstaller %s: %w", uninstallerPath, err)
	}

	pid := pi.ProcessId
	// Os handles não são necessários (o processo continua vivo e independente);
	// fechá-los evita vazamento no processo do agente.
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return pid, nil
}
