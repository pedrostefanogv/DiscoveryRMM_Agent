//go:build !windows

package platform

// Stubs não-Windows: o ciclo de vida do serviço é específico do SCM do
// Windows. Mantidos para que o pacote compile em qualquer plataforma.

// PreshutdownTimeoutFor não tem efeito fora do Windows.
func PreshutdownTimeoutFor(graceSeconds int) uint32 { return 0 }

// ConfigureServiceLifecycle não tem efeito fora do Windows.
func ConfigureServiceLifecycle(name string, preshutdownTimeoutMs uint32) error { return nil }
