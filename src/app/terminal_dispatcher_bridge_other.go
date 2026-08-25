//go:build !windows

package app

// TerminalRunDispatcher é um no-op em plataformas sem o modo dispatcher
// (o dispatcher do terminal é uma implementação Windows/baseada em ConPTY).
// Mantém o main.go compilável em linux/darwin.
func TerminalRunDispatcher() {
	// no-op — modo dispatcher indisponível fora do Windows.
}