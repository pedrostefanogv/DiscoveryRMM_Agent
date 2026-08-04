//go:build !windows

package platform

import "os"

// OpenFileSequential é um stub para plataformas não-Windows; usa os.Open comum.
func OpenFileSequential(path string) (*os.File, error) {
	return os.Open(path)
}
