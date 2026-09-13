package terminal

import "testing"

// TestIsValidWSLDistro cobre o charset restrito da correção A14.
func TestIsValidWSLDistro(t *testing.T) {
	validNames := []string{"Ubuntu", "Ubuntu-22.04", "Debian 12", "openSUSE-Leap_15.5"}
	for _, d := range validNames {
		if !IsValidWSLDistro(d) {
			t.Fatalf("IsValidWSLDistro(%q) = false, want true", d)
		}
	}

	invalid := []string{
		"",                     // vazio
		"   ",                  // só espaços
		"Ubuntu --exec cmd",    // flag dupla
		"Ubuntu; rm -rf /",     // metacaractere ;
		"Ubuntu && calc",       // &&
		"Ubuntu | whoami",      // pipe
		"Ubuntu > out.txt",     // redirect
		"Ubuntu$IFS",           // $
		"Ubuntu\\evil",         // backslash
		"Ubuntu/x",             // barra
		"Ubuntu\"quoted",       // aspas
		"-Ubuntu",              // começa com hífen (parece flag)
		"--Ubuntu",             // começa com -- (flag)
	}
	for _, d := range invalid {
		if IsValidWSLDistro(d) {
			t.Fatalf("IsValidWSLDistro(%q) = true, want false", d)
		}
	}
}

// TestValidateSessionShellKind cobre o clamp do campo shell recebido do
// servidor para os ShellKind seguros (A14).
func TestValidateSessionShellKind(t *testing.T) {
	cases := []struct {
		in   string
		want ShellKind
	}{
		{"", ShellPowerShell},
		{"powershell", ShellPowerShell},
		{"powershell ", ShellPowerShell},
		{"cmd", ShellCmd},
		{"bash", ShellBash},
		{"wsl", ShellWSL},
		{"wsl:Ubuntu", ShellKind("wsl:Ubuntu")},
		{"wsl:Ubuntu-22.04", ShellKind("wsl:Ubuntu-22.04")},
		{"wsl:Ubuntu --exec cmd /c whoami", ShellWSL}, // injeção → fallback default
		{"wsl:--system --exec calc", ShellWSL},        // injeção → fallback default
		{"cmd /c whoami", ShellPowerShell},            // shell desconhecido → default
		{"pwsh -e evil", ShellPowerShell},             // shell desconhecido → default
		{"wsl:", ShellWSL},                            // distro vazia → default
	}
	for _, c := range cases {
		if got := ValidateSessionShellKind(c.in); got != c.want {
			t.Fatalf("ValidateSessionShellKind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
