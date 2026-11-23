//go:build !windows

package power

// Read não tem implementação fora do Windows: o agente atual só coleta
// energia nativamente no Windows. ok=false mantém a semântica de
// "desconhecido" usada por todos os consumidores.
func Read() (Status, bool) {
	return Status{}, false
}
