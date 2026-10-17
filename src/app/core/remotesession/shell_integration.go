//go:build windows

package remotesession

import (
	"bytes"
	"strings"

	"discovery/app/core/terminal"
)

// ── Integração de shell: o shell reporta o próprio cwd ──
//
// Até aqui a completação usava o diretório do PROCESSO do agent: depois de um
// cd no terminal remoto o Tab continuava listando a pasta errada. A decisão do
// usuário foi mudar o protocolo: o SHELL reporta o cwd pelo prompt usando a
// sequência OSC 633 (ESC ] 633 ; P ; Cwd=<path> BEL), a mesma ideia usada pelo
// VS Code.
//
// Este arquivo concentra (1) os builders do init injetado no start do console e
// (2) o scanner que REMOVE a sequência do fluxo de saída (o viewer não pode
// renderizar o OSC como lixo) e devolve os cwd reportados, inclusive quando a
// sequência chega fragmentada entre chunks.
//
// Limitações conhecidas:
//   - o usuário pode sobrescrever o prompt (PowerShell) ou PROMPT_COMMAND /
//     precmd_functions (bash/zsh) DEPOIS do init — nesse caso o shell deixa de
//     reportar e voltamos ao fallback heurístico;
//   - cmd.exe não tem hook de prompt: usa sempre o fallback (cd/Set-Location
//     digitado + cwd do processo);
//   - WSL reporta caminhos POSIX (/home/...), que não são resolvíveis pelo
//     filesystem Windows usado na completação — ver terminal_cwd.go.

// osc633Prefix é o começo da sequência (antes do corpo P;Cwd=...).
const osc633Prefix = "\x1b]633;"

// osc633CwdMarker é o parâmetro que carrega o diretório corrente.
const osc633CwdMarker = "P;Cwd="

// shellIntegrationClearScreen limpa a tela logo após a injeção: o comando
// injetado é digitado no ConPTY e ecoado pelo shell. Limpar a tela (a sessão
// acabou de abrir, não há nada útil para perder) evita deixar o script inteiro
// no scrollback do usuário — o equivalente a limpar a linha que o hello faz.
const shellIntegrationClearScreen = "\x1b[H\x1b[2J"

// buildPowerShellIntegrationInit monta o init de PowerShell/pwsh.
//
// Guarda o prompt ORIGINAL em uma global ($function:prompt) e redefine
// global:prompt para escrever o OSC 633 e então chamar o original — a aparência
// do prompt não muda. O guard ($global:__dshPromptOriginal) evita empilhar
// wrappers se o init for reenviado.
func buildPowerShellIntegrationInit() string {
	return strings.Join([]string{
		`$global:__dshOsc633 = $true;`,
		`if (-not $global:__dshPromptOriginal) { $global:__dshPromptOriginal = $function:prompt };`,
		`function global:prompt { $dshCwd = (Get-Location).Path; if ($dshCwd) { $dshE = [char]27; [Console]::Write($dshE + ']633;P;Cwd=' + $dshCwd + [char]7) }; if ($global:__dshPromptOriginal) { & $global:__dshPromptOriginal } else { 'PS ' + (Get-Location).Path + '> ' } };`,
		`Clear-Host`,
	}, " ")
}

// buildBashIntegrationInit monta o init de bash: registra uma função em
// PROMPT_COMMAND que emite o OSC 633 antes de cada prompt.
func buildBashIntegrationInit() string {
	return `__dsh_osc633_cwd() { printf '\033]633;P;Cwd=%s\a' "$PWD"; }; ` +
		`case ";$PROMPT_COMMAND;" in *";__dsh_osc633_cwd;"*) ;; *) ` +
		`PROMPT_COMMAND="__dsh_osc633_cwd\${PROMPT_COMMAND:+;\$PROMPT_COMMAND}" ;; esac; ` +
		`printf '` + shellIntegrationClearScreen + `'`
}

// buildZshIntegrationInit monta o init de zsh: registra a função em
// precmd_functions (executada antes de cada prompt).
func buildZshIntegrationInit() string {
	return `__dsh_osc633_cwd() { printf '\033]633;P;Cwd=%s\a' "$PWD"; }; ` +
		`typeset -ga precmd_functions; ` +
		`(( \${precmd_functions[(I)__dsh_osc633_cwd]} )) || precmd_functions+=(__dsh_osc633_cwd); ` +
		`printf '` + shellIntegrationClearScreen + `'`
}

// buildPosixIntegrationInit monta um init que funciona em bash E zsh (WSL não
// informa qual dos dois é o shell de login). Detecta pelo $ZSH_VERSION.
func buildPosixIntegrationInit() string {
	return `__dsh_osc633_cwd() { printf '\033]633;P;Cwd=%s\a' "$PWD"; }; ` +
		`if [ -n "$ZSH_VERSION" ]; then typeset -ga precmd_functions; ` +
		`(( \${precmd_functions[(I)__dsh_osc633_cwd]} )) || precmd_functions+=(__dsh_osc633_cwd); ` +
		`else case ";$PROMPT_COMMAND;" in *";__dsh_osc633_cwd;"*) ;; *) ` +
		`PROMPT_COMMAND="__dsh_osc633_cwd\${PROMPT_COMMAND:+;\$PROMPT_COMMAND}" ;; esac; fi; ` +
		`printf '` + shellIntegrationClearScreen + `'`
}

// buildShellIntegrationInit escolhe o init por shell/backend. Devolve "" quando
// não há hook (cmd) ou quando o backend não tem VT (legacy): injetar ESC/OSC em
// console legado imprimiria lixo. Nesse caso o fallback de terminal_cwd.go
// assume.
func buildShellIntegrationInit(shellKind terminal.ShellKind, backend string) string {
	if backend != "conpty" {
		return ""
	}
	k := strings.ToLower(strings.TrimSpace(string(shellKind)))
	switch {
	case k == "powershell" || k == "pwsh":
		return buildPowerShellIntegrationInit()
	case strings.HasPrefix(k, "wsl"):
		// WSL roda bash ou zsh (o builder POSIX detecta $ZSH_VERSION).
		return buildPosixIntegrationInit()
	case k == "zsh":
		// zsh nativo suportado pelo spawner do ConPTY (shell_zsh_test.go).
		return buildZshIntegrationInit()
	}
	// cmd (sem hook) e bash nativo não recebem init: o spawner resolve "bash"
	// para powershell.exe (não há bash.exe no Windows) — injetar sintaxe POSIX
	// ali quebraria a sessão. O fallback heurístico de terminal_cwd.go cobre.
	return ""
}

// ── Parser/removedor do OSC 633 ──

// osc633Scanner remove as sequências OSC 633 do output e devolve os cwd
// reportados. Mantém em pending o começo de uma sequência que ficou partida
// entre chunks (ESC, ESC], ESC]63..., cabeçalho completo sem terminador).
// NÃO é seguro para uso concorrente — cada console tem o seu, alimentado pelo
// callback de output (uma goroutine de leitura).
type osc633Scanner struct {
	pending []byte
}

// Scan processa um chunk e devolve o texto LIMPO (sem OSC 633) e os cwd
// encontrados, na ordem. Sequências OSC de outros tipos (título, etc.) são
// preservadas intactas.
func (s *osc633Scanner) Scan(chunk string) (string, []string) {
	if chunk == "" && len(s.pending) == 0 {
		return "", nil
	}
	data := make([]byte, 0, len(s.pending)+len(chunk))
	data = append(data, s.pending...)
	data = append(data, chunk...)
	s.pending = nil

	var out strings.Builder
	out.Grow(len(data))
	var cwds []string

	pos := 0
	for pos < len(data) {
		idx := bytes.Index(data[pos:], []byte(osc633Prefix[:2]))
		if idx < 0 {
			keep := partialOSCStartLen(data[pos:])
			out.Write(data[pos : len(data)-keep])
			s.pending = append(s.pending[:0], data[len(data)-keep:]...)
			return out.String(), cwds
		}
		idx += pos
		out.Write(data[pos:idx])

		rest := data[idx:]
		// Começo de OSC que pode ser (ou vir a ser) 633: guarda para o próximo
		// chunk em vez de emitir meio cabeçalho ao viewer.
		if len(rest) < len(osc633Prefix) && strings.HasPrefix(osc633Prefix, string(rest)) {
			s.pending = append(s.pending[:0], rest...)
			return out.String(), cwds
		}
		if !bytes.HasPrefix(rest, []byte(osc633Prefix)) {
			// OSC de outro tipo (ex.: título): preserva ESC] e segue.
			out.Write(rest[:2])
			pos = idx + 2
			continue
		}

		bodyStart := idx + len(osc633Prefix)
		bodyEnd, termLen := findOSC633Terminator(data, bodyStart)
		if bodyEnd < 0 {
			s.pending = append(s.pending[:0], data[idx:]...)
			return out.String(), cwds
		}
		if cwd, ok := parseOSC633Cwd(string(data[bodyStart:bodyEnd])); ok {
			cwds = append(cwds, cwd)
		}
		pos = bodyEnd + termLen
	}
	return out.String(), cwds
}

// partialOSCStartLen devolve quantos bytes do fim são um possível começo de
// sequência ESC ] partido entre chunks.
func partialOSCStartLen(b []byte) int {
	if len(b) >= 2 && b[len(b)-2] == 0x1b && b[len(b)-1] == ']' {
		return 2
	}
	if len(b) >= 1 && b[len(b)-1] == 0x1b {
		return 1
	}
	return 0
}

// findOSC633Terminator procura BEL (0x07) ou ST (ESC barra invertida).
// Devolve (-1, 0) quando o terminador ainda não chegou (fragmentado).
func findOSC633Terminator(data []byte, start int) (int, int) {
	for i := start; i < len(data); i++ {
		switch data[i] {
		case 0x07:
			return i, 1
		case 0x1b:
			if i+1 >= len(data) {
				return -1, 0
			}
			if data[i+1] == '\\' {
				return i, 2
			}
		}
	}
	return -1, 0
}

// parseOSC633Cwd extrai o path do corpo P;Cwd=<path>. Aceita aspas em volta do
// path (alguns prompts as colocam) e caminhos com espaço/acento.
func parseOSC633Cwd(body string) (string, bool) {
	if len(body) < len(osc633CwdMarker) || !strings.EqualFold(body[:len(osc633CwdMarker)], osc633CwdMarker) {
		return "", false
	}
	cwd := strings.TrimSpace(body[len(osc633CwdMarker):])
	if len(cwd) >= 2 {
		if (cwd[0] == '"' && cwd[len(cwd)-1] == '"') || (cwd[0] == '\'' && cwd[len(cwd)-1] == '\'') {
			cwd = strings.TrimSpace(cwd[1 : len(cwd)-1])
		}
	}
	if cwd == "" {
		return "", false
	}
	return cwd, true
}
