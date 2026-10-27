//go:build windows

package terminal

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// ShellKind define o tipo de shell.
type ShellKind string

const (
	ShellCmd        ShellKind = "cmd"
	ShellPowerShell ShellKind = "powershell"
	ShellBash       ShellKind = "bash"
	ShellWSL        ShellKind = "wsl"
	ShellZsh        ShellKind = "zsh"
)

// zshKnownPaths são instalações conhecidas de zsh fora do PATH (Git for
// Windows, MSYS2 e Cygwin). O zsh só é oferecido quando um destes arquivos —
// ou um zsh do PATH — existe de fato.
var zshKnownPaths = []string{
	`C:\Program Files\Git\usr\bin\zsh.exe`,
	`C:\Program Files\Git\bin\zsh.exe`,
	`C:\msys64\usr\bin\zsh.exe`,
	`C:\cygwin64\bin\zsh.exe`,
}

// zshResolver agrupa as operações de descoberta do zsh para que os testes
// injetem PATH/FS falsos sem tocar o host.
type zshResolver struct {
	lookPath func(string) (string, error)
	stat     func(string) (os.FileInfo, error)
}

// defaultZshResolver é a implementação real (PATH + locais conhecidos).
var defaultZshResolver = zshResolver{lookPath: exec.LookPath, stat: os.Stat}

// find devolve o caminho do zsh quando ele existe no PATH ou nos locais
// conhecidos. Procura primeiro no PATH (zsh.exe e zsh) e só depois nos caminhos
// fixos, aceitando apenas arquivo (nunca diretório).
func (r zshResolver) find() (string, bool) {
	for _, nome := range []string{"zsh.exe", "zsh"} {
		if p, err := r.lookPath(nome); err == nil && strings.TrimSpace(p) != "" {
			return p, true
		}
	}
	for _, caminho := range zshKnownPaths {
		info, err := r.stat(caminho)
		if err == nil && info != nil && !info.IsDir() {
			return caminho, true
		}
	}
	return "", false
}

// ZshExecutable devolve o caminho do zsh instalado ("" + false se ausente).
func ZshExecutable() (string, bool) {
	return defaultZshResolver.find()
}

// IsZshAvailable informa se o shell zsh pode ser oferecido ao viewer.
func IsZshAvailable() bool {
	_, ok := ZshExecutable()
	return ok
}

// AvailableShellKinds devolve os shells oferecidos ao viewer: os básicos
// (powershell/cmd), zsh quando o executável existe e wsl:<distro> por distro.
// Sem zsh instalado ele não aparece na lista.
func AvailableShellKinds() []string {
	return availableShellKinds(IsZshAvailable, IsWSLAvailable)
}

// availableShellKinds é a versão injetável de AvailableShellKinds (testes).
func availableShellKinds(zshDisponivel func() bool, wsl func() (bool, []string)) []string {
	shells := []string{string(ShellPowerShell), string(ShellCmd)}
	if zshDisponivel != nil && zshDisponivel() {
		shells = append(shells, string(ShellZsh))
	}
	if wsl != nil {
		if available, distros := wsl(); available {
			for _, distro := range distros {
				shells = append(shells, "wsl:"+distro)
			}
		}
	}
	return shells
}

// ResolveShell resolve o shell efetivo para uso, garantindo que o binário
// exista. Em máquinas sem PowerShell (ex.: Windows PE, Core/Server minimal),
// o pedido de powershell é rebaixado para cmd.exe. Retorna o ShellKind e o
// caminho do executável resolvido (ou "" se nenhum shell estiver disponível).
func ResolveShell(shell ShellKind) (ShellKind, string) {
	return resolveShellWith(shell, defaultZshResolver)
}

// resolveShellWith é a versão injetável de ResolveShell: o resolvedor de zsh
// permite testar a descoberta (PATH/FS) sem depender do host.
func resolveShellWith(shell ShellKind, zsh zshResolver) (ShellKind, string) {
	switch shell {
	case ShellPowerShell:
		// PowerShell 7+ (pwsh) quando instalado; o Windows PowerShell 5.1
		// (powershell.exe) fica como fallback. O binário resolvido vai no
		// term.ready (shellPath) para diagnóstico remoto.
		for _, name := range []string{"pwsh.exe", "powershell.exe"} {
			if p, err := exec.LookPath(name); err == nil {
				return ShellPowerShell, p
			}
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
	case ShellZsh:
		// Sem executável o zsh NÃO é oferecido: devolve caminho vazio (mesma
		// política do WSL) em vez de rebaixar para cmd.
		if p, ok := zsh.find(); ok {
			return ShellZsh, p
		}
		return ShellZsh, ""
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

// Timings do probe de WSL. O wsl.exe pode travar (serviço LxssManager preso,
// disco lento): sem timeout, AvailableShellKinds nunca retornava e o term.ready
// nunca era publicado — o terminal ficava "conectando" para sempre.
const (
	wslProbeTimeout = 1500 * time.Millisecond
	wslCacheTTL     = 60 * time.Second
)

var wslCache struct {
	mu        sync.Mutex
	at        time.Time
	available bool
	distros   []string
}

// IsWSLAvailable verifica se o WSL está instalado e retorna as distribuições
// disponíveis. Cache de 60 s + timeout de 1,5 s (ver comentário acima).
func IsWSLAvailable() (bool, []string) {
	wslCache.mu.Lock()
	if time.Since(wslCache.at) < wslCacheTTL {
		available := wslCache.available
		distros := append([]string(nil), wslCache.distros...)
		wslCache.mu.Unlock()
		return available, distros
	}
	wslCache.mu.Unlock()

	available, distros := probeWSLAvailable()

	wslCache.mu.Lock()
	wslCache.at = time.Now()
	wslCache.available = available
	wslCache.distros = append([]string(nil), distros...)
	wslCache.mu.Unlock()

	return available, distros
}

// probeWSLAvailable faz a consulta real (sem cache).
func probeWSLAvailable() (available bool, distros []string) {
	// Verifica se wsl.exe existe
	if _, err := exec.LookPath("wsl.exe"); err != nil {
		return false, nil
	}

	// Tenta listar distribuições: wsl.exe -l -q (com timeout).
	ctx, cancel := context.WithTimeout(context.Background(), wslProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wsl.exe", "-l", "-q").Output()
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
