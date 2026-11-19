package screen

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestEncodeDirtyRectsSobSaturacao estressa o caminho de tile repetidamente com
// o MESMO scratch, que e o padrao de producao (uma sessao de tela reaproveita o
// scratch a cada frame). O objetivo e provar que a reutilizacao nao corrompe
// nenhum resultado ao longo de muitas iteracoes — o bug classico de buffer
// reaproveitado aparece so depois de varias voltas.
func TestEncodeDirtyRectsSobSaturacao(t *testing.T) {
	frame := benchFrameBGRA(320, 240, 123)
	rects := []DirtyRect{
		{X: 0, Y: 0, Width: 64, Height: 64},
		{X: 128, Y: 64, Width: 64, Height: 64},
		{X: 250, Y: 200, Width: 64, Height: 64}, // recortado nas bordas
	}

	// Referencia sem scratch.
	ref, err := EncodeDirtyRects(frame, rects, 55)
	if err != nil {
		t.Fatal(err)
	}

	sc := &TileScratch{}
	for i := 0; i < 500; i++ {
		got, err := EncodeDirtyRectsScratch(frame, rects, 55, sc)
		if err != nil {
			t.Fatalf("iteracao %d: %v", i, err)
		}
		if !bytes.Equal(got, ref) {
			t.Fatalf("iteracao %d: bytes divergem do resultado sem scratch (%d vs %d)", i, len(got), len(ref))
		}
	}
}

// TestEncodeDirtyRectsHeaderConsistenteEmLote valida que TODOS os headers de um
// lote grande batem com o JPEG correspondente (nao so o primeiro).
func TestEncodeDirtyRectsHeaderConsistenteEmLote(t *testing.T) {
	frame := benchFrameBGRA(200, 150, 321)
	var rects []DirtyRect
	for y := 0; y < 150; y += 30 {
		for x := 0; x < 200; x += 40 {
			rects = append(rects, DirtyRect{X: x, Y: y, Width: 48, Height: 40})
		}
	}
	out, err := EncodeDirtyRects(frame, rects, 50)
	if err != nil {
		t.Fatal(err)
	}
	n := int(binary.BigEndian.Uint16(out[0:2]))
	off := 4
	for i := 0; i < n; i++ {
		w := int(binary.BigEndian.Uint16(out[off+4 : off+6]))
		h := int(binary.BigEndian.Uint16(out[off+6 : off+8]))
		size := int(binary.BigEndian.Uint32(out[off+8 : off+12]))
		off += 12 + size
		if w <= 0 || h <= 0 {
			t.Fatalf("tile %d com dimensao invalida %dx%d", i, w, h)
		}
	}
	if off != len(out) {
		t.Fatalf("consumiu %d bytes de %d: lote inconsistente", off, len(out))
	}
}
