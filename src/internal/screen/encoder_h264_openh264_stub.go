//go:build !h264_openh264
// +build !h264_openh264

package screen

import "errors"

var errOpenH264NotAvailable = errors.New("OpenH264 encoder not available (build tag h264_openh264 not set)")

func NewOpenH264Encoder() (Encoder, error) {
	return nil, errOpenH264NotAvailable
}
