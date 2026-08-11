//go:build windows

package screen

import (
	"math"
	"testing"
)

func TestHalfToFloat(t *testing.T) {
	cases := []struct {
		h    uint16
		want float32
	}{
		{0x0000, 0},            // +0
		{0x3C00, 1.0},          // 1.0
		{0x4000, 2.0},          // 2.0
		{0x3800, 0.5},          // 0.5
		{0x0001, 0.0000000596}, // subnormal mínimo
		{0x7BFF, 65504},        // maior half normal
		{0x8000, 0},            // -0
		{0xBC00, -1.0},         // -1.0
	}
	for _, c := range cases {
		got := halfToFloat(c.h)
		if math.Abs(float64(got-c.want)) > 0.001 {
			t.Errorf("halfToFloat(0x%04X) = %v, want %v", c.h, got, c.want)
		}
	}
}

func TestToneMapLinear(t *testing.T) {
	cases := []struct {
		in   float32
		want float32
	}{
		{0, 0},
		{0.5, 0.5},   // SDR preservado
		{1.0, 1.0},   // branco SDR
		{2.0, 1.667}, // destaque comprimido (1 + 1/(1+1/2))
		{4.0, 2.2},   // destaque mais comprimido
	}
	for _, c := range cases {
		got := toneMapLinear(c.in)
		if math.Abs(float64(got-c.want)) > 0.01 {
			t.Errorf("toneMapLinear(%v) = %v, want ~%v", c.in, got, c.want)
		}
	}
	// Monotonicidade: valores maiores nunca produzem saída menor
	prev := float32(0)
	for i := 0; i <= 100; i++ {
		v := toneMapLinear(float32(i) / 10)
		if v < prev {
			t.Errorf("toneMapLinear não é monotônico em %v", float32(i)/10)
		}
		prev = v
	}
}

func TestLinearToSRGBByte(t *testing.T) {
	cases := []struct {
		in   float32
		want byte
	}{
		{0, 0},
		{1.0, 255},
		{0.5, 187}, // ~0.5 linear → ~0.735 sRGB → 187
		{0.25, 137},
	}
	for _, c := range cases {
		got := linearToSRGBByte(c.in)
		if got != c.want {
			t.Errorf("linearToSRGBByte(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestToneMapHDRToSDR(t *testing.T) {
	// Cria um frame scRGB 2x1: pixel 0 = preto, pixel 1 = branco SDR (1.0)
	w, h := 2, 1
	data := make([]byte, w*h*8)
	// halfToFloat(1.0) = 0x3C00
	one := uint16(0x3C00)
	// Pixel 0: preto (0,0,0)
	// Pixel 1: branco (1.0, 1.0, 1.0)
	off := 8
	putHalf := func(off int, v uint16) {
		data[off] = byte(v)
		data[off+1] = byte(v >> 8)
	}
	putHalf(off, one)
	putHalf(off+2, one)
	putHalf(off+4, one)
	putHalf(off+6, one) // alpha

	frame := &Frame{Data: data, Width: w, Height: h, Stride: w * 8, ColorSpace: DXGI_COLOR_SPACE_RGB_FULL_G10_NONE_P709}
	out := ToneMapHDRToSDR(frame)

	if out.Width != w || out.Height != h || out.Stride != w*4 {
		t.Fatalf("dimensões erradas: %dx%d stride=%d", out.Width, out.Height, out.Stride)
	}
	// Pixel 0 (preto) → BGRA (0,0,0,255)
	if out.Data[0] != 0 || out.Data[1] != 0 || out.Data[2] != 0 || out.Data[3] != 255 {
		t.Errorf("pixel preto = %v, want [0 0 0 255]", out.Data[0:4])
	}
	// Pixel 1 (branco) → BGRA (255,255,255,255)
	if out.Data[4] != 255 || out.Data[5] != 255 || out.Data[6] != 255 || out.Data[7] != 255 {
		t.Errorf("pixel branco = %v, want [255 255 255 255]", out.Data[4:8])
	}
	// O frame original não deve ser alterado
	if frame.Data[0] != 0 || frame.Data[8] != byte(one) {
		t.Errorf("frame original foi alterado")
	}
}

func TestIsHDRColorSpace(t *testing.T) {
	if !isHDRColorSpace(DXGI_COLOR_SPACE_RGB_FULL_G10_NONE_P709) {
		t.Error("scRGB deveria ser HDR")
	}
	if !isHDRColorSpace(DXGI_COLOR_SPACE_RGB_FULL_G2084_NONE_P2020) {
		t.Error("HDR10 deveria ser HDR")
	}
	if isHDRColorSpace(DXGI_COLOR_SPACE_RGB_FULL_G22_NONE_P709) {
		t.Error("SDR não deveria ser HDR")
	}
}

func TestSwapRB_BGRA(t *testing.T) {
	// RGBA (R,G,B,A) → BGRA (B,G,R,A). Verde (#00FF00) permanece intacto;
	// vermelho (#FF0000) e azul (#0000FF) trocam entre si — reproduz o bug.
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"vermelho pra azul", []byte{0xFF, 0x00, 0x00, 0xFF}, []byte{0x00, 0x00, 0xFF, 0xFF}},
		{"azul pra vermelho", []byte{0x00, 0x00, 0xFF, 0xFF}, []byte{0xFF, 0x00, 0x00, 0xFF}},
		{"verde permanece", []byte{0x00, 0xFF, 0x00, 0xFF}, []byte{0x00, 0xFF, 0x00, 0xFF}},
		{"grayscale invariante", []byte{0x80, 0x80, 0x80, 0xAA}, []byte{0x80, 0x80, 0x80, 0xAA}},
		{"multi pixel", []byte{0xFF, 0x00, 0x00, 0xFF, 0x00, 0xFF, 0x00, 0xFF},
			[]byte{0x00, 0x00, 0xFF, 0xFF, 0x00, 0xFF, 0x00, 0xFF}},
	}
	for _, c := range cases {
		buf := append([]byte(nil), c.in...)
		swapRB_BGRA(buf)
		if string(buf) != string(c.want) {
			t.Errorf("%s: swapRB_BGRA(%v) = %v, want %v", c.name, c.in, buf, c.want)
		}
		// Idempotência: aplicar de novo deve restaurar o original.
		swapRB_BGRA(buf)
		if string(buf) != string(c.in) {
			t.Errorf("%s: segunda chamada não é idempotente: %v", c.name, buf)
		}
	}

	// Buffer vazio e buffer com tamanho não-múltiplo de 4 não devem causar
	// panic (path de fallback byte-a-byte).
	for _, edge := range [][]byte{nil, {}, {0xFF}, {0xFF, 0x00}} {
		swapRB_BGRA(edge) // só garante que não panica
	}
}
