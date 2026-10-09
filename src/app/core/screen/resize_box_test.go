package screen

import "testing"

func boxTestFrame(w, h int, set func(x, y int) (byte, byte, byte, byte)) *Frame {
	data := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			b, g, r, a := set(x, y)
			o := (y*w + x) * 4
			data[o], data[o+1], data[o+2], data[o+3] = b, g, r, a
		}
	}
	return &Frame{Data: data, Width: w, Height: h, Stride: w * 4}
}

// O filtro de área usa TODOS os pixels de origem: 0,255,255,0 em 4x1 -> 127,127.
// A amostragem bilinear daria 0,255 (descarta metade dos pixels).
func TestResizeBGRABoxAveragesEverySourcePixel(t *testing.T) {
	src := boxTestFrame(4, 1, func(x, _ int) (byte, byte, byte, byte) {
		var v byte
		if x == 1 || x == 2 {
			v = 255
		}
		return v, v, v, 255
	})
	out := ResizeBGRABox(src, 0.5)
	if out == nil || out.Width != 2 || out.Height != 1 {
		t.Fatalf("dimensoes = %v, want 2x1", out)
	}
	for x := 0; x < 2; x++ {
		if got := out.Data[x*4]; got != 128 {
			t.Fatalf("pixel %d = %d, want 128 (media de area, arredondada)", x, got)
		}
	}
}

// Padrão quadriculado 1px: com média de área o resultado é o cinza médio, sem
// o serrilhado que a amostragem pontual produz.
func TestResizeBGRABoxKillsCheckerboardAliasing(t *testing.T) {
	src := boxTestFrame(2, 2, func(x, y int) (byte, byte, byte, byte) {
		var v byte
		if (x+y)%2 == 0 {
			v = 255
		}
		return v, v, v, 255
	})
	out := ResizeBGRABox(src, 0.5)
	if out.Width != 1 || out.Height != 1 {
		t.Fatalf("dimensoes = %dx%d, want 1x1", out.Width, out.Height)
	}
	if got := out.Data[0]; got < 126 || got > 129 {
		t.Fatalf("quadriculado = %d, want ~127", got)
	}
}

func TestResizeBGRABoxKeepsUniformColor(t *testing.T) {
	src := boxTestFrame(5, 3, func(_, _ int) (byte, byte, byte, byte) { return 10, 20, 30, 255 })
	out := ResizeBGRABox(src, 0.5)
	if out.Width != 2 || out.Height != 1 {
		t.Fatalf("dimensoes = %dx%d, want 2x1", out.Width, out.Height)
	}
	for i := 0; i+3 < len(out.Data); i += 4 {
		if out.Data[i] != 10 || out.Data[i+1] != 20 || out.Data[i+2] != 30 || out.Data[i+3] != 255 {
			t.Fatalf("cor uniforme deformada no offset %d: %v", i, out.Data[i:i+4])
		}
	}
}

func TestResizeBGRABoxPreservesOriginAndIgnoresUpscale(t *testing.T) {
	src := boxTestFrame(4, 4, func(_, _ int) (byte, byte, byte, byte) { return 1, 2, 3, 255 })
	src.OriginX, src.OriginY = 100, 50
	out := ResizeBGRABox(src, 0.5)
	if out.OriginX != 100 || out.OriginY != 50 {
		t.Fatalf("origem perdida: %d,%d", out.OriginX, out.OriginY)
	}
	if got := ResizeBGRABox(src, 2.0); got != src {
		t.Fatal("scaleFactor >= 1 deveria devolver o proprio frame")
	}
}
