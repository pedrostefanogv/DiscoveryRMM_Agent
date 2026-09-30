//go:build windows

package screen

import (
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

func TestDIBBytesHeaderAndBottomUp(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	// Linha de cima (y=0) vermelha, linha de baixo (y=1) azul.
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{R: 255, A: 255})
	img.Set(0, 1, color.RGBA{B: 255, A: 255})
	img.Set(1, 1, color.RGBA{B: 255, A: 255})

	data, err := dibBytes(img)
	if err != nil {
		t.Fatalf("dibBytes falhou: %v", err)
	}
	if len(data) != 40+2*2*4 {
		t.Fatalf("tamanho = %d, want %d", len(data), 40+16)
	}
	if got := binary.LittleEndian.Uint32(data[0:]); got != 40 {
		t.Fatalf("biSize = %d", got)
	}
	if got := binary.LittleEndian.Uint32(data[4:]); got != 2 {
		t.Fatalf("biWidth = %d", got)
	}
	if got := binary.LittleEndian.Uint32(data[8:]); got != 2 {
		t.Fatalf("biHeight = %d (deve ser positivo = bottom-up)", got)
	}
	if got := binary.LittleEndian.Uint16(data[14:]); got != 32 {
		t.Fatalf("biBitCount = %d", got)
	}
	// Bottom-up: o primeiro pixel do DIB é a linha de BAIXO (azul em BGRA).
	first := data[40:44]
	if first[0] != 255 || first[2] != 0 {
		t.Fatalf("primeiro pixel deveria ser azul em BGRA, got %v", first)
	}
	last := data[40+3*4 : 40+4*4]
	if last[2] != 255 || last[0] != 0 {
		t.Fatalf("ultimo pixel deveria ser vermelho em BGRA, got %v", last)
	}
}
