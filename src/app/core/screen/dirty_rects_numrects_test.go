package screen

import (
	"encoding/binary"
	"testing"
)

// TestEncodeDirtyRectsNumRectsEfetivo cobre a correção do header: um rect
// TOTALMENTE fora do frame é descartado, e o numRects anunciado precisa
// refletir apenas os tiles efetivamente gravados. O viewer itera numRects vezes;
// contagem inflada lia lixo além do payload.
func TestEncodeDirtyRectsNumRectsEfetivo(t *testing.T) {
	frame := benchFrameBGRA(100, 60, 55)
	rects := []DirtyRect{
		{X: 0, Y: 0, Width: 30, Height: 30},   // válido
		{X: 200, Y: 0, Width: 30, Height: 30}, // totalmente fora (X > largura)
		{X: 0, Y: 200, Width: 30, Height: 30}, // totalmente fora (Y > altura)
		{X: 60, Y: 20, Width: 30, Height: 30}, // recortado na borda, válido
	}
	out, err := EncodeDirtyRects(frame, rects, 60)
	if err != nil {
		t.Fatal(err)
	}
	n := int(binary.BigEndian.Uint16(out[0:2]))
	if n != 2 {
		t.Fatalf("numRects=%d, esperado 2 (um válido integral + um recortado)", n)
	}

	// Percorre o lote: tem de terminar EXATAMENTE no fim do payload.
	off := 4
	for i := 0; i < n; i++ {
		size := int(binary.BigEndian.Uint32(out[off+8 : off+12]))
		off += 12 + size
	}
	if off != len(out) {
		t.Fatalf("lote consumiu %d de %d bytes: header e payload divergem", off, len(out))
	}
}

// TestEncodeDirtyRectsNumRectsZero documenta o caso sem tiles graváveis: o
// payload é só o header, com numRects=0. O viewer trata como frame sem tiles
// (o decoder rejeita e descarta — nada a desenhar mesmo).
func TestEncodeDirtyRectsNumRectsZero(t *testing.T) {
	frame := benchFrameBGRA(100, 60, 66)
	out, err := EncodeDirtyRects(frame, []DirtyRect{{X: 500, Y: 500, Width: 10, Height: 10}}, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("esperado só o header (4 bytes), veio %d", len(out))
	}
	if n := int(binary.BigEndian.Uint16(out[0:2])); n != 0 {
		t.Fatalf("numRects=%d, esperado 0", n)
	}
}
