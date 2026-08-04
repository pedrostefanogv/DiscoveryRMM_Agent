//go:build windows

package terminal

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// ShellKind define o tipo de shell.
type ShellKind string

const (
	ShellCmd        ShellKind = "cmd"
	ShellPowerShell ShellKind = "powershell"
	ShellBash       ShellKind = "bash"
	ShellWSL        ShellKind = "wsl"
)

// IsWSLAvailable verifica se o WSL está instalado e retorna as distribuições disponíveis.
func IsWSLAvailable() (available bool, distros []string) {
	// Verifica se wsl.exe existe
	if _, err := exec.LookPath("wsl.exe"); err != nil {
		return false, nil
	}

	// Tenta listar distribuições: wsl.exe -l -q
	cmd := exec.Command("wsl.exe", "-l", "-q")
	out, err := cmd.Output()
	if err != nil {
		return false, nil
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Ignorar mensagens informativas do WSL
		if strings.HasPrefix(line, "Windows Subsystem") {
			continue
		}
		distros = append(distros, line)
	}

	return len(distros) > 0, distros
}

// WSLDistroToShellKind converte um nome de distribuição WSL para ShellKind.
func WSLDistroToShellKind(distro string) ShellKind {
	return ShellKind(fmt.Sprintf("wsl:%s", distro))
}

// ShellKindToWSLDistro extrai o nome da distribuição de um ShellKind WSL.
func ShellKindToWSLDistro(kind ShellKind) string {
	s := string(kind)
	if strings.HasPrefix(s, "wsl:") {
		return s[4:]
	}
	return ""
}

// IsWSL retorna true se o ShellKind representa um shell WSL.
func IsWSL(kind ShellKind) bool {
	return strings.HasPrefix(string(kind), "wsl")
}

// Ensure io import
var _ io.Reader
