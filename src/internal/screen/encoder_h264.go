//go:build h264
// +build h264

package screen

import (
	"fmt"
)

// h264Encoder comprime frames via Media Foundation H.264 (cgo, Windows).
// NOTA: implementacao requer bindings COM para IMFTransform + IMFSample.
// Sera completada com bindings syscall direto na Fase 5.
type h264Encoder struct{}

func NewH264Encoder() (Encoder, error) {
	return &h264Encoder{}, nil
}

func (e *h264Encoder) Encode(frame *Frame, quality int) ([]byte, error) {
	return nil, fmt.Errorf("H.264 Media Foundation: bindings COM pendentes (Fase 5)")
}

func (e *h264Encoder) Name() string { return "h264" }

var _ Encoder = (*h264Encoder)(nil)
