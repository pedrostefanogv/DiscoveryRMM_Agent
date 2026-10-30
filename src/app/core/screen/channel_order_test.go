//go:build windows

package screen

import "testing"

// frameFromPixels monta um Frame BGRA a partir de pares (r,g,b) para testes.
func frameFromPixels(rgb [][3]byte, w int) *Frame {
	h := len(rgb) / w
	data := make([]byte, w*h*4)
	for i, c := range rgb {
		off := i * 4
		data[off] = c[2]   // B
		data[off+1] = c[1] // G
		data[off+2] = c[0] // R
		data[off+3] = 255
	}
	return &Frame{Data: data, Width: w, Height: h, Stride: w * 4}
}

func swapFrameRB(f *Frame) {
	out := make([]byte, len(f.Data))
	copy(out, f.Data)
	swapRB_BGRA(out)
	copy(f.Data, out)
}

// testPattern gera um padrão com contraste R↔B (o que a calibração precisa).
func testPattern(w, h int) [][3]byte {
	rgb := make([][3]byte, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			rgb = append(rgb, [3]byte{
				byte((x * 255) / w),   // R
				byte((y * 255) / h),   // G
				byte(255 - (x*255)/w), // B
			})
		}
	}
	return rgb
}

func TestProbeChannelOrderDetectsBGRA(t *testing.T) {
	const w, h = 128, 72
	pattern := testPattern(w, h)
	reference := frameFromPixels(pattern, w) // GDI: BGRA correto
	candidate := frameFromPixels(pattern, w) // capturer já entrega BGRA

	if got := probeChannelOrder(candidate, reference); got != ChannelOrderBGRA {
		t.Fatalf("ordem = %v, want BGRA", got)
	}
}

func TestProbeChannelOrderDetectsRGBA(t *testing.T) {
	const w, h = 128, 72
	pattern := testPattern(w, h)
	reference := frameFromPixels(pattern, w)
	candidate := frameFromPixels(pattern, w)
	swapFrameRB(candidate) // capturer entrega RGBA

	if got := probeChannelOrder(candidate, reference); got != ChannelOrderRGBA {
		t.Fatalf("ordem = %v, want RGBA", got)
	}
}

func TestProbeChannelOrderGrayscaleIsUnknown(t *testing.T) {
	const w, h = 128, 72
	gray := make([][3]byte, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := byte((x + y) % 256)
			gray = append(gray, [3]byte{v, v, v})
		}
	}
	reference := frameFromPixels(gray, w)
	candidate := frameFromPixels(gray, w)
	swapFrameRB(candidate)

	if got := probeChannelOrder(candidate, reference); got != ChannelOrderUnknown {
		t.Fatalf("ordem = %v, want Unknown (sem contraste R↔B)", got)
	}
}

func TestProbeChannelOrderUnrelatedContentIsUnknown(t *testing.T) {
	const w, h = 128, 72
	a := frameFromPixels(testPattern(w, h), w)
	// Conteúdo totalmente diferente: nenhuma hipótese casa.
	other := make([][3]byte, 0, w*h)
	for i := 0; i < w*h; i++ {
		other = append(other, [3]byte{byte(i % 7 * 30), byte(i % 5 * 40), byte(i % 3 * 60)})
	}
	b := frameFromPixels(other, w)

	if got := probeChannelOrder(a, b); got != ChannelOrderUnknown {
		t.Fatalf("ordem = %v, want Unknown (conteúdo divergente)", got)
	}
}

func TestChannelCalibratorAppliesResolvedOrder(t *testing.T) {
	// Já calibrado como RGBA: apply deve converter para BGRA.
	// verified=true: sem isso o apply tentaria calibrar (captura GDI) antes.
	cc := channelCalibrator{order: ChannelOrderRGBA, resolved: true, verified: true}
	f := frameFromPixels([][3]byte{{10, 20, 30}, {200, 100, 50}}, 2)
	original := make([]byte, len(f.Data))
	copy(original, f.Data)

	cc.apply(f)

	if f.Data[0] != original[2] || f.Data[2] != original[0] {
		t.Fatalf("apply não swapou R↔B: antes=%v depois=%v", original, f.Data)
	}
	// Idempotência não é esperada: aplicar de novo desfaria — garante que a
	// calibração resolve uma única vez (resolved=true impede nova tentativa).
	cc.apply(f)
	if f.Data[0] != original[0] {
		t.Fatal("apply aplicou swap duas vezes")
	}
}
