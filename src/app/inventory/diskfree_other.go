//go:build !windows

package inventory

// freeDiskBytes não é implementado fora do Windows: devolve espaço
// desconhecido (0) e a checagem de espaço é ignorada.
func freeDiskBytes(string) (uint64, error) {
	return 0, nil
}
