//go:build !cgo
// +build !cgo

package screen

import "errors"

var errWebPNotAvailable = errors.New("WebP encoder not available (cgo disabled)")

func NewWebPEncoder() Encoder {
	return nil
}
