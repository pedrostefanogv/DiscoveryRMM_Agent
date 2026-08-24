//go:build windows

package terminal

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"unicode/utf16"
)

// ShellKind define o tipo de shell.
type ShellKind string

const (
	ShellCmd        ShellKind = "cmd"
	ShellPowerShell ShellKind = "powershell"
	ShellBash       ShellKind = "bash"
	ShellWSL        ShellKind = "wsl"
)

// ResolveShell resolve o shell efetivo para uso, garantindo que o binário
// exista. Em máquinas sem PowerShell (ex.: Windows PE, Core/Server minimal),
// o pedido de powershell é rebaixado para cmd.exe. Retorna o ShellKind e o
// caminho do executável resolvido (ou "" se nenhum shell estiver disponível).
func ResolveShell(shell ShellKind) (ShellKind, string) {
	switch shell {
	case ShellPowerShell:
		if p, err := exec.LookPath("powershell.exe"); err == nil {
			return ShellPowerShell, p
		}
		// Sem PowerShell — rebaixa para cmd.
		if p, err := exec.LookPath("cmd.exe"); err == nil {
			return ShellCmd, p
		}
		return shell, ""
	case ShellWSL, ShellBash:
		if p, err := exec.LookPath("wsl.exe"); err == nil {
			return shell, p
		}
		return shell, ""
	case ShellCmd, "":
		if p, err := exec.LookPath("cmd.exe"); err == nil {
			return ShellCmd, p
		}
		return shell, ""
	default:
		// wsl:<distro> etc — mantém
		return shell, ""
	}
}

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

	// wsl.exe -l -q emite UTF-16LE (com BOM) no Windows. Converter direto para
	// string sem decodificar produz "U\u0000b\u0000u\u0000n\u0000t\u0000u\u0000"
	// (nome com blanks), como visto nos logs. Normalizamos para UTF-8.
	text := decodeWSLOutput(out)

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Ignorar mensagens informativas do WSL ("Windows Subsystem...")
		if strings.HasPrefix(line, "Windows Subsystem") || strings.HasPrefix(line, "Mensagens") || strings.Contains(line, "Subsystem") {
			continue
		}
		distros = append(distros, line)
	}

	return len(distros) > 0, distros
}

// decodeWSLOutput converte a saída do wsl.exe para UTF-8, lidando com UTF-16LE
// (com ou sem BOM) e com o caso em que a saída já chega em UTF-8.
func decodeWSLOutput(out []byte) string {
	trimmed := out
	// Remove BOM UTF-16LE (FF FE) e UTF-8 (EF BB BF)
	if len(trimmed) >= 2 && trimmed[0] == 0xFF && trimmed[1] == 0xFE {
		return decodeUTF16LE(trimmed[2:])
	}
	if len(trimmed) >= 2 && trimmed[0] == 0xFE && trimmed[1] == 0xFF {
		return decodeUTF16BE(trimmed[2:])
	}
	// Sem BOM UTF-16: detecta zeros a cada 2 bytes (padrão UTF-16LE típico)
	if hasZeroEveryOtherByte(trimmed) {
		return decodeUTF16LE(trimmed)
	}
	return string(trimmed)
}

func hasZeroEveryOtherByte(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	// Verifica uma amostra: bytes pares sendo 0 para texto ASCII/observável
	zeroOdd := 0
	zeroEven := 0
	sample := b
	if len(sample) > 64 {
		sample = sample[:64]
	}
	for i := 0; i < len(sample); i++ {
		if sample[i] == 0 {
			if i%2 == 1 {
				zeroOdd++
			} else {
				zeroEven++
			}
		}
	}
	// UTF-16LE de texto ASCII: todos os bytes ímpares são 0.
	return zeroOdd > 0 && zeroOdd > zeroEven
}

func decodeUTF16LE(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, len(b)/2)
	for i := 0; i < len(u); i++ {
		u[i] = uint16(b[i*2]) | uint16(b[i*2+1])<<8
	}
	return strings.TrimSpace(string(utf16.Decode(u)))
}

func decodeUTF16BE(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, len(b)/2)
	for i := 0; i < len(u); i++ {
		u[i] = uint16(b[i*2])<<8 | uint16(b[i*2+1])
	}
	return strings.TrimSpace(string(utf16.Decode(u)))
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
