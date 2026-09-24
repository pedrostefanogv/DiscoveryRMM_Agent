//go:build !windows

package winget

// fromAppPathsRegistry é no-op fora do Windows: o winget só existe no Windows.
func fromAppPathsRegistry() string { return "" }
