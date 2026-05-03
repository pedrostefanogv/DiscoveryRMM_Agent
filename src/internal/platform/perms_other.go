//go:build !windows

package platform

// EnsureWorldAccess is a no-op on non-Windows platforms.
func EnsureWorldAccess(path string) error {
	return nil
}
