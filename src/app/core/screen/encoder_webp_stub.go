//go:build !cgo
// +build !cgo

package screen

import (
	"bytes"
	"errors"
	"image"
)

var errWebPNotAvailable = errors.New("WebP encoder not available (cgo disabled)")

func NewWebPEncoder() Encoder {
	return nil
}

func encodeRGBAWebP(_ *bytes.Buffer, _ *image.RGBA, _ int) error {
	return errWebPNotAvailable
}
