package screen

import (
	"testing"
)

// TestBenchCompareTileVsFullSize mede o TAMANHO codificado dos dois caminhos
// para decidir a Fase 4 do plano de otimização: o modo tile economiza banda em
// dano PEQUENO, mas custa CPU. Este teste imprime os números reais.
func TestBenchCompareTileVsFullSize(t *testing.T) {
	frame := benchFrameBGRA(1920, 1080, 21)
	enc := NewJPEGEncoder()

	full, err := enc.Encode(frame, 60)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("frame completo: %d bytes", len(full))

	cases := []struct {
		name  string
		rects []DirtyRect
	}{
		{"1 tile 240x135", []DirtyRect{{X: 100, Y: 100, Width: 240, Height: 135}}},
		{"4 tiles", []DirtyRect{
			{X: 0, Y: 0, Width: 240, Height: 135},
			{X: 300, Y: 0, Width: 240, Height: 135},
			{X: 0, Y: 300, Width: 240, Height: 135},
			{X: 300, Y: 300, Width: 240, Height: 135},
		}},
		{"64 tiles (bench)", func() []DirtyRect {
			var r []DirtyRect
			for rr := 0; rr < 8; rr++ {
				for c := 0; c < 8; c++ {
					r = append(r, DirtyRect{X: c * 240, Y: rr * 135, Width: 240, Height: 135})
				}
			}
			return r
		}()},
	}
	for _, tc := range cases {
		tiles, err := EncodeDirtyRects(frame, tc.rects, 60)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%-18s tiles=%2d -> %8d bytes (full=%d, economia=%.1f%%)",
			tc.name, len(tc.rects), len(tiles), len(full),
			(1-float64(len(tiles))/float64(len(full)))*100)
	}
}
