package screenshot

import (
	"bytes"
	"image"
	"testing"

	"discovery/app/core/screen"
)

// Downscale é o caminho usado por TODAS as capturas reduzidas (IA e ferramenta
// de captura). Ele precisa usar média de área: 4x1 com 0,255,255,0 reduzido à
// metade deve dar 127/127, não 0/255.
func TestDownscaleUsesAreaAveraging(t *testing.T) {
	data := make([]byte, 4*4)
	for x := 0; x < 4; x++ {
		var v byte
		if x == 1 || x == 2 {
			v = 255
		}
		o := x * 4
		data[o], data[o+1], data[o+2], data[o+3] = v, v, v, 255
	}
	frame := &screen.Frame{Data: data, Width: 4, Height: 1, Stride: 16}

	out := Downscale(frame, 2)
	if out.Width != 2 || out.Height != 1 {
		t.Fatalf("dimensoes = %dx%d, want 2x1", out.Width, out.Height)
	}
	for x := 0; x < 2; x++ {
		if got := out.Data[x*4]; got != 127 {
			t.Fatalf("pixel %d = %d, want 127 (media de area)", x, got)
		}
	}
}

// Sem redução o frame original é devolvido (nenhuma perda é introduzida).
func TestDownscaleNoOpWhenWithinLimit(t *testing.T) {
	frame := &screen.Frame{Data: make([]byte, 3*2*4), Width: 3, Height: 2, Stride: 12}
	if got := Downscale(frame, 3840); got != frame {
		t.Fatal("frame dentro do limite deveria ser devolvido sem copia")
	}
}

// Padrão de produto: a captura vai na resolução ORIGINAL. Mesmo acima do antigo
// teto de 3840 (ultrawide/5K), sem maxDimension explícito nada é reduzido.
func TestEncodeKeepsOriginalResolutionByDefault(t *testing.T) {
	frame := solidFrame(5000, 120, 12, 18, 24, 255)
	data, mime, err := EncodeFrameFormat(frame, 0, 0, FormatAuto)
	if err != nil {
		t.Fatalf("EncodeFrameFormat falhou: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("imagem nao decodifica (%s): %v", mime, err)
	}
	if b := img.Bounds(); b.Dx() != 5000 || b.Dy() != 120 {
		t.Fatalf("resolucao final = %dx%d, want 5000x120 (resolucao original)", b.Dx(), b.Dy())
	}

	// A redução continua disponível quando pedida explicitamente (custo de tokens).
	reduced, _, err := EncodeFrameFormat(frame, 0, 2000, FormatAuto)
	if err != nil {
		t.Fatalf("EncodeFrameFormat reduzido falhou: %v", err)
	}
	img2, _, err := image.Decode(bytes.NewReader(reduced))
	if err != nil {
		t.Fatalf("imagem reduzida nao decodifica: %v", err)
	}
	if b := img2.Bounds(); b.Dx() != 2000 {
		t.Fatalf("largura reduzida = %d, want 2000", b.Dx())
	}
}
