//go:build !windows

package processutil

// SetEfficiencyMode is a no-op outside Windows.
func SetEfficiencyMode(enable bool) (bool, error) {
	return false, nil
}

// TrimCurrentProcessWorkingSet is a no-op outside Windows.
func TrimCurrentProcessWorkingSet() error {
	return nil
}
