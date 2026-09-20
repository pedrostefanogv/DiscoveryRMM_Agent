//go:build !windows

package platform

// Samplers de poder da máquina: stub para plataformas não-Windows
// (o acesso remoto é exclusivo do Windows; retorna desconhecido).

// SampleCPUPercent retorna -1 (não implementado fora do Windows).
func SampleCPUPercent() float64 { return -1 }

// SampleMemoryPercent retorna -1 (não implementado fora do Windows).
func SampleMemoryPercent() float64 { return -1 }
