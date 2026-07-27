//go:build !webp
// +build !webp

package screen

import "errors"

var errWebPNotAvailable = errors.New("WebP encoder not available (build tag webp not set)")

func NewWebPEncoder() Encoder {
	return nil
}
