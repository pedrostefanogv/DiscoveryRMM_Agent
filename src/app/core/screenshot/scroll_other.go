//go:build !windows

package screenshot

import "errors"

// ScrollDown não tem equivalente fora do Windows (depende de SendInput).
func ScrollDown(notches int) error {
	return errors.New("rolagem do mouse suportada apenas no Windows")
}
