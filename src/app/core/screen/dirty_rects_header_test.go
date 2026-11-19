package screen

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
	"testing"
)

// TestEncodeDirtyRectsHeaderBateComImagemRecortada cobre o defeito corrigido na
// revisao de 2026-11-19: um tile que passa da borda direita/inferior do frame
// tem o tamanho REDUZIDO no recorte, mas o header anunciava o tamanho ORIGINAL.
// O viewer posiciona o tile por (x,y) e desenha o bitmap no tamanho natural,
// entao o descasamento aparecia como tile esticado/deslocado nas bordas.
func TestEncodeDirtyRectsHeaderBateComImagemRecortada(t *testing.T) {
	// Frame pequeno para forcar varios tiles de borda.
	frame := benchFrameBGRA(100, 60, 77)
	rects := []DirtyRect{
		{X: 80, Y: 0, Width: 32, Height: 32},  // passa da DIREITA (100-80=20)
		{X: 0, Y: 40, Width: 32, Height: 32},  // passa de BAIXO (60-40=20)
		{X: 90, Y: 50, Width: 32, Height: 32}, // passa de ambos (10x10)
	}

	out, err := EncodeDirtyRects(frame, rects, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 4 {
		t.Fatalf("saida curta: %d", len(out))
	}
	n := int(binary.BigEndian.Uint16(out[0:2]))
	if n != len(rects) {
		t.Fatalf("numRects=%d, esperado %d", n, len(rects))
	}

	off := 4
	for i := 0; i < n; i++ {
		if off+12 > len(out) {
			t.Fatalf("tile %d: header truncado", i)
		}
		x := int(binary.BigEndian.Uint16(out[off : off+2]))
		y := int(binary.BigEndian.Uint16(out[off+2 : off+4]))
		w := int(binary.BigEndian.Uint16(out[off+4 : off+6]))
		h := int(binary.BigEndian.Uint16(out[off+6 : off+8]))
		size := int(binary.BigEndian.Uint32(out[off+8 : off+12]))
		off += 12
		if off+size > len(out) {
			t.Fatalf("tile %d: payload truncado (size=%d)", i, size)
		}
		jpegBytes := out[off : off+size]
		off += size

		// O JPEG tem de ser decodificavel e ter EXATAMENTE o tamanho do header.
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(jpegBytes))
		if err != nil {
			t.Fatalf("tile %d: JPEG invalido: %v", i, err)
		}
		if cfg.Width != w || cfg.Height != h {
			t.Errorf("tile %d: header diz %dx%d, JPEG tem %dx%d", i, w, h, cfg.Width, cfg.Height)
		}
		// E o tile precisa caber dentro do frame a partir de (x,y).
		if x+w > frame.Width || y+h > frame.Height {
			t.Errorf("tile %d: (%d,%d) %dx%d ultrapassa o frame %dx%d", i, x, y, w, h, frame.Width, frame.Height)
		}
	}
}

// TestEncodeDirtyRectsTilesDentroDoFrame cobre o caso normal: retangulos
// totalmente internos mantem as dimensoes exatas (a correcao nao pode encolher
// tiles validos).
func TestEncodeDirtyRectsTilesDentroDoFrame(t *testing.T) {
	frame := benchFrameBGRA(640, 480, 88)
	rects := []DirtyRect{{X: 100, Y: 100, Width: 240, Height: 135}}
	out, err := EncodeDirtyRects(frame, rects, 60)
	if err != nil {
		t.Fatal(err)
	}
	off := 4
	w := int(binary.BigEndian.Uint16(out[off+4 : off+6]))
	h := int(binary.BigEndian.Uint16(out[off+6 : off+8]))
	if w != 240 || h != 135 {
		t.Fatalf("tile interno virou %dx%d, esperado 240x135", w, h)
	}
}
