//go:build !windows

package winregistry

// ActiveUserContext stub para plataformas nao-Windows.
func ActiveUserContext() *UserContext {
	return nil
}
