//go:build !windows

package inventory

func collectWindowsMemoryNative() (float64, float64, float64, bool) {
	return -1, -1, -1, false
}
