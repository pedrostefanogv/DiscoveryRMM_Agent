//go:build windows

package screenshot

import (
	"os"

	"discovery/app/core/screen"
)

// selfPID é o PID do processo do agente (usado para marcar IsSelf).
func selfPID() int { return os.Getpid() }

// toneMapIfHDR aplica tone mapping quando o capturador devolveu scRGB float
// (monitor HDR/Advanced Color). Fora do caminho DXGI o frame já é SDR e a
// função é no-op.
func toneMapIfHDR(f *screen.Frame) *screen.Frame {
	if f == nil || f.ColorSpace == 0 {
		return f
	}
	isScRGB := f.ColorSpace == screen.DXGI_COLOR_SPACE_RGB_FULL_G10_NONE_P709 ||
		f.ColorSpace == screen.DXGI_COLOR_SPACE_RGB_FULL_G10_NONE_P709_FIXED_POINT
	if !isScRGB || f.Stride != f.Width*8 {
		return f
	}
	return screen.ToneMapHDRToSDR(f)
}
