//go:build windows

package terminal

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

// decodeEncodedCommand decodifica o valor de um -EncodedCommand (UTF-16LE +
// base64) de volta para texto.
func decodeEncodedCommand(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 do -EncodedCommand invalido: %v", err)
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(raw[i:]))
	}
	return string(utf16.Decode(units))
}

func TestPowerShellStartupArgs_Formato(t *testing.T) {
	args := powerShellStartupArgs()
	if len(args) != 4 {
		t.Fatalf("args = %v (len %d), want 4", args, len(args))
	}
	if args[0] != "-NoLogo" || args[1] != "-NoExit" || args[2] != "-EncodedCommand" {
		t.Fatalf("flags inesperadas: %v", args[:3])
	}
	// Base64 sem espaço/tab/aspas: o quoting do buildCommandLine nunca se
	// aplica (um argumento quebrado ao meio descarregaria o script no shell).
	if strings.ContainsAny(args[3], " \t\"") {
		t.Fatalf("base64 do -EncodedCommand tem caractere de quoting: %q", args[3])
	}
}

func TestPowerShellStartupArgs_ConteudoDoScript(t *testing.T) {
	script := decodeEncodedCommand(t, powerShellStartupArgs()[3])
	if script == "" {
		t.Fatal("script do -EncodedCommand vazio")
	}
	for _, frag := range []string{
		"$global:__dshOsc633",
		"$global:__dshPromptOriginal",
		"$function:prompt",
		"function global:prompt",
		"]633;P;Cwd=",
		"Get-Location",
		"Clear-Host",
	} {
		if !strings.Contains(script, frag) {
			t.Fatalf("script da integracao nao contem %q: %s", frag, script)
		}
	}
	// O init nunca é "digitado", mas segue sendo UMA linha lógica: sem CR/LF
	// (a command line não pode conter quebras).
	if strings.ContainsAny(script, "\r\n") {
		t.Fatalf("script deveria ser uma unica linha: %q", script)
	}
}

func TestResolveShellCommand_PowerShellComEncodedCommand(t *testing.T) {
	exe, args := resolveShellCommand(ShellPowerShell)
	base := strings.ToLower(exe)
	if !strings.HasSuffix(base, "pwsh.exe") && !strings.HasSuffix(base, "powershell.exe") {
		t.Fatalf("exe inesperado: %q", exe)
	}
	// Último par de flags: -EncodedCommand <b64> (init embutido no spawn).
	if len(args) < 4 || args[len(args)-2] != "-EncodedCommand" {
		t.Fatalf("args sem -EncodedCommand no fim: %v", args)
	}
	script := decodeEncodedCommand(t, args[len(args)-1])
	if !strings.Contains(script, "]633;P;Cwd=") {
		t.Fatalf("script do spawn sem hook OSC 633: %s", script)
	}
}
