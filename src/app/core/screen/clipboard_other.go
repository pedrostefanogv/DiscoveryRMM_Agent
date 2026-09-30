//go:build !windows

package screen

import (
	"errors"
	"image"
)

// SetClipboardImage não tem equivalente fora do Windows.
func SetClipboardImage(img image.Image) error {
	return errors.New("clipboard de imagem suportado apenas no Windows")
}
