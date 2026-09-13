package terminal

import (
	"regexp"
	"strings"
)

// ── Validação do campo shell de sessões remotas (correção A14) ──────────────
//
// O campo "shell" do payload de sessão remota vem do servidor sem validação e
// era convertido direto em ShellKind, chegando a resolveShellCommand →
// wsl.exe -d <distro>. Como a command line é montada por join de args, um
// valor como "wsl:Ubuntu --exec cmd /c whoami" virava
//
//	"wsl.exe" -d Ubuntu --exec cmd /c whoami
//
// — RCE genérico no host para qualquer ator capaz de emitir comando de
// sessão (backend comprometido, viewer comprometido). Estas validações
// restringem o que chega ao CreateProcessW.

// wslDistroPattern restringe o nome de distro WSL: apenas [A-Za-z0-9 ._-],
// começando com alfanumérico, tamanho ≤ 64. Não aceita metacaracteres de
// shell/linha de comando (--, ;, &, |, <, >, $, aspas, barras).
var wslDistroPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)

// IsValidWSLDistro valida um nome de distribuição WSL recebido de fonte
// externa (servidor). Charset restrito + rejeição de sequências que virariam
// flags de wsl.exe mesmo dentro do charset: "--" e " -" (token iniciado por
// hífen após espaço), ex.: "Ubuntu --exec cmd" contém só chars válidos mas
// é uma tentativa de injeção.
func IsValidWSLDistro(distro string) bool {
	distro = strings.TrimSpace(distro)
	if distro == "" {
		return false
	}
	if !wslDistroPattern.MatchString(distro) {
		return false
	}
	if strings.Contains(distro, "--") || strings.Contains(distro, " -") {
		return false
	}
	return true
}

// ValidateSessionShellKind valida o campo shell recebido do servidor para
// sessões remotas e retorna um ShellKind seguro:
//   - "" / "powershell" → ShellPowerShell (default);
//   - "cmd" | "bash" | "wsl" → mantidos;
//   - "wsl:<distro>" aceito somente com distro no charset permitido —
//     caso contrário cai no WSL default;
//   - qualquer outro valor cai no default (powershell).
func ValidateSessionShellKind(raw string) ShellKind {
	s := strings.TrimSpace(raw)
	switch s {
	case "", string(ShellPowerShell):
		return ShellPowerShell
	case string(ShellCmd):
		return ShellCmd
	case string(ShellBash), string(ShellWSL):
		return ShellKind(s)
	}
	if strings.HasPrefix(s, string(ShellWSL)+":") {
		distro := strings.TrimSpace(s[len(ShellWSL)+1:])
		if IsValidWSLDistro(distro) {
			return ShellKind(string(ShellWSL) + ":" + distro)
		}
		return ShellWSL
	}
	return ShellPowerShell
}
