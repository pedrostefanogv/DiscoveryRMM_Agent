//go:build windows

package app

import "discovery/app/core/terminal"

// TerminalRunDispatcher entra em modo dispatcher do terminal (subprocesso que
// executa o ConPTY isolado do processo GUI). Chamado pelo main.go quando o
// binário é lançado com --terminal-dispatcher (flag interna usada pelo
// NewShellInteractive). Bloqueia até o shell encerrar.
func TerminalRunDispatcher() {
	terminal.RunDispatcher()
}