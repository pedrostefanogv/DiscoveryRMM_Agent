//go:build !windows

package tpmvault

// newPlatformVault fora do Windows: o cofre não é suportado.
func newPlatformVault(opts Options) Vault {
	return newUnavailableVault(opts, ErrUnavailable.Error())
}
