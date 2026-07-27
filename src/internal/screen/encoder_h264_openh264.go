//go:build h264_openh264
// +build h264_openh264

package screen

import (
	"fmt"
)

// openh264Encoder comprime frames via Cisco OpenH264 (cgo).
// Fallback quando Media Foundation nao esta disponivel.
type openh264Encoder struct{}

func NewOpenH264Encoder() (Encoder, error) {
	return &openh264Encoder{}, nil
}

func (e *openh264Encoder) Encode(frame *Frame, quality int) ([]byte, error) {
	return nil, fmt.Errorf("OpenH264: bindings cgo pendentes (Fase 5)")
}

func (e *openh264Encoder) Name() string { return "h264-openh264" }

var _ Encoder = (*openh264Encoder)(nil)
