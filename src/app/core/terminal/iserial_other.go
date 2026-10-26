//go:build !windows

package terminal

// NewShellInteractive cria um shell interativo. Em plataformas não-Windows
// não há ConPTY; usamos o shell legado baseado em pipes (/bin/sh, /bin/bash).
func NewShellInteractive(shell ShellKind, cols, rows int, onOutput func(string)) (IShell, error) {
	key := string(shell)
	if key == "" || key == "cmd" || key == "powershell" || key == "wsl" {
		key = "sh" // shells do Windows não existem aqui; fallback para /bin/sh
	}
	s, err := NewShell(key, onOutput)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// NewShellInteractiveRunAs é a variante run-as (fora do Windows o run-as é
// sempre herdado — não existe token de sessão de console).
func NewShellInteractiveRunAs(shell ShellKind, cols, rows int, onOutput func(string), runAs RunAs) (IShell, string, error) {
	s, err := NewShellInteractive(shell, cols, rows, onOutput)
	return s, "inherit", err
}

// Compila: garante que o Shell implementa IShell.
var _ IShell = (*Shell)(nil)

// ShellBackendName retorna o nome legível do backend de shell em uso.
// Em não-Windows usa sempre o shell legado (pty).
func ShellBackendName(s IShell) string {
	if s == nil {
		return "none"
	}
	return "pty"
}
