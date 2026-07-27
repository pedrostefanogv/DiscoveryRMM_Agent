//go:build !h264
// +build !h264

package screen

import "errors"

var errH264NotAvailable = errors.New("H.264 encoder not available (build tag h264 not set)")

func NewH264Encoder() (Encoder, error) {
	return nil, errH264NotAvailable
}
