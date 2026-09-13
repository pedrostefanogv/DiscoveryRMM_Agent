//go:build !windows

package platform

// EnsureSharedStagingAccess is a no-op on non-Windows platforms.
func EnsureSharedStagingAccess(path string) error {
	return nil
}

// EnsureWorldReadable is a no-op on non-Windows platforms.
func EnsureWorldReadable(filePath string) error {
	return nil
}
// IsElevated is a no-op on non-Windows platforms (não há modelo UAC fora
// do Windows; considera elevado para não bloquear fluxos).
func IsElevated() bool {
	return true
}

// HardenSecretFileACL is a no-op on non-Windows platforms (o modo 0o600 do
// os.WriteFile já restringe no POSIX).
func HardenSecretFileACL(path string) error {
	return nil
}
