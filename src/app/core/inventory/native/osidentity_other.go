//go:build !windows

package native

// DetectOSIdentity não tem implementação fora do Windows: devolve "unknown" com
// IsServerKnown=false para que o chamador OMITA os campos de identidade em vez
// de afirmar que a máquina não é servidor.
func DetectOSIdentity() OSIdentity {
	return OSIdentity{ProductType: ProductTypeUnknown}
}
