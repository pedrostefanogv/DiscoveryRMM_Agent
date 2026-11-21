//go:build cgo

package screen

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestWebPEncoderEncodeAndScratchReuse(t *testing.T) {
	enc := NewWebPEncoder()
	if enc.Name() != "webp" {
		t.Fatalf("Name() = %q, want webp", enc.Name())
	}

	const w, h = 64, 48
	data := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := (y*w + x) * 4
			data[off] = 200   // B
			data[off+1] = 100 // G
			data[off+2] = 50  // R
			data[off+3] = 0   // A=0 na entrada deve virar opaco no encode
		}
	}
	origCopy := append([]byte(nil), data...)
	frame := &Frame{Data: data, Width: w, Height: h, Stride: w * 4}

	out1, err := enc.Encode(frame, 80)
	if err != nil {
		t.Fatalf("Encode 1 falhou: %v", err)
	}
	if len(out1) < 12 || !bytes.Equal(out1[:4], []byte("RIFF")) || !bytes.Equal(out1[8:12], []byte("WEBP")) {
		t.Fatalf("saída não é WebP válido (len=%d)", len(out1))
	}
	if !bytes.Equal(frame.Data, origCopy) {
		t.Fatal("Encode mutou frame.Data in-place")
	}

	// Segunda chamada com dimensões maiores para exercitar realocação do scratch.
	const w2, h2 = 96, 64
	frame2 := &Frame{Data: make([]byte, w2*h2*4), Width: w2, Height: h2, Stride: w2 * 4}
	out2, err := enc.Encode(frame2, 70)
	if err != nil {
		t.Fatalf("Encode 2 falhou: %v", err)
	}
	if len(out2) < 12 || !bytes.Equal(out2[:4], []byte("RIFF")) {
		t.Fatalf("saída 2 não é WebP válido (len=%d)", len(out2))
	}
}

func TestEncodeDirtyRectsWebPTilesAndCopyFrameRects(t *testing.T) {
	const w, h = 128, 96
	srcData := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := (y*w + x) * 4
			srcData[off] = byte(x)
			srcData[off+1] = byte(y)
			srcData[off+2] = 180
			srcData[off+3] = 255
		}
	}
	src := &Frame{Data: srcData, Width: w, Height: h, Stride: w * 4}
	dst := &Frame{Data: make([]byte, w*h*4), Width: w, Height: h, Stride: w * 4}
	rects := []DirtyRect{
		{X: 16, Y: 16, Width: 32, Height: 24},
		{X: 64, Y: 48, Width: 40, Height: 32},
	}

	CopyFrameRects(dst, src, rects)
	// Verifica pixel dentro do rect copiado e pixel fora (deve continuar zerado).
	inOff := (16*w + 16) * 4
	if dst.Data[inOff+2] != 180 {
		t.Fatalf("pixel dentro do rect não foi copiado: %v", dst.Data[inOff:inOff+4])
	}
	outOff := (0*w + 0) * 4
	if dst.Data[outOff+2] != 0 {
		t.Fatalf("pixel fora do rect não deveria ter sido copiado: %v", dst.Data[outOff:outOff+4])
	}

	var sc TileScratch
	payload, err := EncodeDirtyRectsCodecScratch(dst, rects, 75, "webp", &sc)
	if err != nil {
		t.Fatalf("EncodeDirtyRectsCodecScratch(webp) falhou: %v", err)
	}
	numRects := binary.BigEndian.Uint16(payload[0:2])
	if numRects != 2 {
		t.Fatalf("numRects = %d, want 2", numRects)
	}
	// Verifica que o primeiro tile começa com RIFF....WEBP.
	tileSize := binary.BigEndian.Uint32(payload[12:16])
	tileData := payload[16 : 16+tileSize]
	if len(tileData) < 12 || !bytes.Equal(tileData[:4], []byte("RIFF")) || !bytes.Equal(tileData[8:12], []byte("WEBP")) {
		t.Fatalf("tile não está em formato WebP válido (header=%q)", tileData[:min(12, len(tileData))])
	}
}
