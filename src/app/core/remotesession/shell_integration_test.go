//go:build windows

package remotesession

import (
	"strings"
	"testing"

	"discovery/app/core/terminal"
)

// ── Builders do init de integração ──

func TestBuildShellIntegrationInit_PowerShell(t *testing.T) {
	init := buildPowerShellIntegrationInit()
	if init == "" {
		t.Fatal("init de PowerShell nao pode ser vazio")
	}
	for _, frag := range []string{
		"$global:__dshPromptOriginal",
		"$function:prompt",
		"function global:prompt",
		"]633;P;Cwd=",
		"Get-Location",
		"Clear-Host",
	} {
		if !strings.Contains(init, frag) {
			t.Fatalf("init de PowerShell nao contem %q: %s", frag, init)
		}
	}
	// Enviado como UMA linha no stdin do ConPTY (o Enter vai separado).
	if strings.ContainsAny(init, "\r\n") {
		t.Fatalf("init de PowerShell deveria ser uma unica linha: %q", init)
	}
}

func TestBuildShellIntegrationInit_Bash(t *testing.T) {
	init := buildBashIntegrationInit()
	for _, frag := range []string{
		"__dsh_osc633_cwd",
		"PROMPT_COMMAND",
		"$PWD",
		"printf",
		"]633;P;Cwd=%s",
		shellIntegrationClearScreen,
	} {
		if !strings.Contains(init, frag) {
			t.Fatalf("init de bash nao contem %q: %s", frag, init)
		}
	}
	if strings.ContainsAny(init, "\r\n") {
		t.Fatalf("init de bash deveria ser uma unica linha: %q", init)
	}
}

func TestBuildShellIntegrationInit_Zsh(t *testing.T) {
	init := buildZshIntegrationInit()
	for _, frag := range []string{
		"__dsh_osc633_cwd",
		"precmd_functions",
		"$PWD",
		"printf",
		"]633;P;Cwd=%s",
		shellIntegrationClearScreen,
	} {
		if !strings.Contains(init, frag) {
			t.Fatalf("init de zsh nao contem %q: %s", frag, init)
		}
	}
}

func TestBuildShellIntegrationInit_Dispatcher(t *testing.T) {
	cases := []struct {
		name    string
		kind    terminal.ShellKind
		backend string
		want    bool
		frag    string
	}{
		{"powershell conpty", terminal.ShellPowerShell, "conpty", true, "__dshPromptOriginal"},
		{"pwsh conpty", terminal.ShellKind("pwsh"), "conpty", true, "__dshPromptOriginal"},
		{"cmd sem hook", terminal.ShellCmd, "conpty", false, ""},
		// bash nativo NÃO recebe init: o spawner do ConPTY resolve "bash" para
		// powershell.exe; injetar POSIX ali quebraria a sessão.
		{"bash nativo sem init", terminal.ShellBash, "conpty", false, ""},
		{"zsh nativo conpty", terminal.ShellKind("zsh"), "conpty", true, "precmd_functions"},
		{"wsl conpty", terminal.ShellWSL, "conpty", true, "ZSH_VERSION"},
		{"wsl distro", terminal.ShellKind("wsl:Ubuntu"), "conpty", true, "ZSH_VERSION"},
		{"legacy sem VT", terminal.ShellPowerShell, "legacy", false, ""},
		{"desconhecido", terminal.ShellKind("fish"), "conpty", false, ""},
	}
	for _, c := range cases {
		got := buildShellIntegrationInit(c.kind, c.backend)
		if c.want {
			if got == "" {
				t.Fatalf("%s: init vazio, want nao-vazio", c.name)
			}
			if c.frag != "" && !strings.Contains(got, c.frag) {
				t.Fatalf("%s: init nao contem %q: %s", c.name, c.frag, got)
			}
			continue
		}
		if got != "" {
			t.Fatalf("%s: init = %q, want vazio", c.name, got)
		}
	}
}

// ── Scanner/removedor do OSC 633 ──

// osc633CwdSeq monta a sequência completa com terminador BEL.
func osc633CwdSeq(path string) string {
	return "\x1b]633;P;Cwd=" + path + "\x07"
}

// scanChunks alimenta o scanner com os chunks e junta o resultado.
func scanChunks(chunks ...string) (string, []string) {
	s := &osc633Scanner{}
	var clean strings.Builder
	var cwds []string
	for _, c := range chunks {
		out, found := s.Scan(c)
		clean.WriteString(out)
		cwds = append(cwds, found...)
	}
	return clean.String(), cwds
}

func TestOSC633Scanner_RemoveNoMeioDoTexto(t *testing.T) {
	path := "C:\\Users\\Pédro Lourenço\\Documents"
	clean, cwds := scanChunks("PS C:\\> " + osc633CwdSeq(path) + "PS C:\\Users\\Pédro Lourenço\\Documents> ")
	if clean != "PS C:\\> PS C:\\Users\\Pédro Lourenço\\Documents> " {
		t.Fatalf("texto limpo inesperado: %q", clean)
	}
	if len(cwds) != 1 || cwds[0] != path {
		t.Fatalf("cwds = %#v, want path %q", cwds, path)
	}
}

func TestOSC633Scanner_MultiplosNaMesmaChunk(t *testing.T) {
	clean, cwds := scanChunks("a" + osc633CwdSeq("C:\\um") + "b" + osc633CwdSeq("C:\\dois") + "c")
	if clean != "abc" {
		t.Fatalf("clean = %q, want abc", clean)
	}
	if len(cwds) != 2 || cwds[0] != "C:\\um" || cwds[1] != "C:\\dois" {
		t.Fatalf("cwds = %#v", cwds)
	}
}

func TestOSC633Scanner_FragmentadoByteAByte(t *testing.T) {
	path := "C:\\Program Files\\Ação"
	texto := "antes-" + osc633CwdSeq(path) + "-depois"
	var chunks []string
	for i := 0; i < len(texto); i++ {
		chunks = append(chunks, texto[i:i+1])
	}
	clean, cwds := scanChunks(chunks...)
	if clean != "antes--depois" {
		t.Fatalf("clean fragmentado = %q, want antes--depois", clean)
	}
	if len(cwds) != 1 || cwds[0] != path {
		t.Fatalf("cwds fragmentado = %#v, want %q", cwds, path)
	}
}

func TestOSC633Scanner_FragmentadoEmTodosOsPontos(t *testing.T) {
	path := "C:\\pasta com espaço"
	texto := "x" + osc633CwdSeq(path) + "y"
	for corte := 1; corte < len(texto); corte++ {
		clean, cwds := scanChunks(texto[:corte], texto[corte:])
		if clean != "xy" {
			t.Fatalf("corte %d: clean = %q, want xy", corte, clean)
		}
		if len(cwds) != 1 || cwds[0] != path {
			t.Fatalf("corte %d: cwds = %#v", corte, cwds)
		}
	}
}

func TestOSC633Scanner_TerminadorST(t *testing.T) {
	clean, cwds := scanChunks("a\x1b]633;P;Cwd=/home/user\x1b\\b")
	if clean != "ab" {
		t.Fatalf("clean = %q, want ab", clean)
	}
	if len(cwds) != 1 || cwds[0] != "/home/user" {
		t.Fatalf("cwds = %#v", cwds)
	}
}

func TestOSC633Scanner_PreservaOSCDeOutroTipo(t *testing.T) {
	title := "\x1b]0;meu titulo\x07"
	clean, cwds := scanChunks("a" + title + "b")
	if clean != "a"+title+"b" {
		t.Fatalf("OSC de titulo deveria ser preservado: %q", clean)
	}
	if len(cwds) != 0 {
		t.Fatalf("cwds = %#v, want vazio", cwds)
	}
}

func TestOSC633Scanner_PathComAspas(t *testing.T) {
	_, cwds := scanChunks("\x1b]633;P;Cwd=\"C:\\com espaço\"\x07")
	if len(cwds) != 1 || cwds[0] != "C:\\com espaço" {
		t.Fatalf("cwds = %#v", cwds)
	}
}

func TestOSC633Scanner_SequenciaIncompletaNaoVaza(t *testing.T) {
	s := &osc633Scanner{}
	out, cwds := s.Scan("antes\x1b]633;P;Cwd=C:\\par")
	if out != "antes" {
		t.Fatalf("prefixo incompleto vazou: %q", out)
	}
	if len(cwds) != 0 {
		t.Fatalf("cwds = %#v, want vazio", cwds)
	}
	out2, cwds2 := s.Scan("cial\x07depois")
	if out2 != "depois" {
		t.Fatalf("resto = %q, want depois", out2)
	}
	if len(cwds2) != 1 || cwds2[0] != "C:\\parcial" {
		t.Fatalf("cwds = %#v", cwds2)
	}
}
