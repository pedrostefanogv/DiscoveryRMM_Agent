//go:build !windows

package platform

// CPUSampler é um stub para plataformas não-Windows.
// O throttling de CPU não é suportado nestas plataformas; Sample() sempre retorna -1.
type CPUSampler struct{}

// NewCPUSampler cria um sampler (stub).
func NewCPUSampler() *CPUSampler { return &CPUSampler{} }

// Sample retorna -1 (não implementado).
func (s *CPUSampler) Sample() float64 { return -1 }
