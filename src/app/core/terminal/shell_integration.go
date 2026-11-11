//go:build windows

package terminal

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// ── Integração de shell (OSC 633) no command line do spawn ──
//
// O shell PowerShell/pwsh reporta o próprio cwd pela sequência OSC 633
// (ESC ] 633 ; P ; Cwd=<path> BEL) — a mesma ideia do VS Code. O agente remove
// a sequência do fluxo de saída (remotesession/osc633Scanner) e usa o path
// para a completação (Tab) do terminal remoto.
//
// O init vai EMBUTIDO no command line do spawn via -EncodedCommand
// (UTF-16LE + base64) em vez de ser DIGITADO no ConPTY. Motivo (2026-11-11):
// digitar o init no stdin ecoava o script inteiro no viewer (com highlighting
// do PSReadLine) e o Clear-Host do fim da linha — que "escondia" o eco — era
// cancelável: um Ctrl+C do operador (reação natural ao texto estranho
// aparecendo) abortava a linha ANTES do Clear-Host e o script ficava visível
// para sempre. Com -EncodedCommand o init nunca é ecoado, roda antes do
// primeiro prompt (o hook já vale para o prompt inicial) e não depende de
// timing com o carregamento do profile.

// powerShellIntegrationScript devolve o init de integração do PowerShell/pwsh.
//
// Guarda o prompt ORIGINAL em uma global ($function:prompt) e redefine
// global:prompt para escrever o OSC 633 e então chamar o original — a aparência
// do prompt não muda. O guard ($global:__dshPromptOriginal) evita empilhar
// wrappers se o init rodar duas vezes. O Clear-Host do fim preserva o UX
// anterior (terminal abre limpo, sem o output do profile).
func powerShellIntegrationScript() string {
	return strings.Join([]string{
		"$global:__dshOsc633 = $true;",
		"if (-not $global:__dshPromptOriginal) { $global:__dshPromptOriginal = $function:prompt };",
		"function global:prompt { $dshCwd = (Get-Location).Path; if ($dshCwd) { $dshE = [char]27; [Console]::Write($dshE + ']633;P;Cwd=' + $dshCwd + [char]7) }; if ($global:__dshPromptOriginal) { & $global:__dshPromptOriginal } else { 'PS ' + (Get-Location).Path + '> ' } };",
		"Clear-Host",
	}, " ")
}

// powerShellStartupArgs monta os argumentos de spawn do PowerShell/pwsh com o
// init de integração via -EncodedCommand (UTF-16LE + base64). O base64 não tem
// espaço/tab/aspas: o quoting do buildCommandLine nunca se aplica e a command
// line não pode quebrar em argumentos a mais.
func powerShellStartupArgs() []string {
	units := utf16.Encode([]rune(powerShellIntegrationScript()))
	raw := make([]byte, 0, len(units)*2)
	var buf [2]byte
	for _, u := range units {
		binary.LittleEndian.PutUint16(buf[:], u)
		raw = append(raw, buf[:]...)
	}
	return []string{"-NoLogo", "-NoExit", "-EncodedCommand", base64.StdEncoding.EncodeToString(raw)}
}
