package screen

import (
	"bytes"
	"testing"
)

// TestEncodeDirtyRectsScratchFormatoIdentico prova que a versão com scratch
// reutilizável produz EXATAMENTE os mesmos bytes da versão sem scratch. O
// formato do tile é contrato com o viewer (useFilesStream/fileFrameCodec no
// site e o decoder em RemoteScreenViewer): qualquer divergência aqui vira tela
// rasgada ou tile no lugar errado em produção.
func TestEncodeDirtyRectsScratchFormatoIdentico(t *testing.T) {
	frame := benchFrameBGRA(640, 480, 99)
	cases := [][]DirtyRect{
		{{X: 0, Y: 0, Width: 64, Height: 64}},
		{
			{X: 0, Y: 0, Width: 100, Height: 50},
			{X: 300, Y: 200, Width: 120, Height: 80},
			{X: 500, Y: 400, Width: 140, Height: 80}, // encosta na borda
		},
		{}, // nenhum rect
	}
	for i, rects := range cases {
		a, err := EncodeDirtyRects(frame, rects, 60)
		if err != nil {
			t.Fatalf("caso %d sem scratch: %v", i, err)
		}
		sc := &TileScratch{}
		b1, err := EncodeDirtyRectsScratch(frame, rects, 60, sc)
		if err != nil {
			t.Fatalf("caso %d com scratch: %v", i, err)
		}
		// Segunda chamada com o MESMO scratch: prova que a reutilização não
		// contamina o resultado (o bug clássico de buffer reaproveitado).
		b2, err := EncodeDirtyRectsScratch(frame, rects, 60, sc)
		if err != nil {
			t.Fatalf("caso %d scratch reusado: %v", i, err)
		}
		if !bytes.Equal(a, b1) {
			t.Fatalf("caso %d: bytes diferem entre com/sem scratch (%d vs %d)", i, len(a), len(b1))
		}
		if !bytes.Equal(a, b2) {
			t.Fatalf("caso %d: reutilizar o scratch mudou o resultado", i)
		}
		// Prova de que o resultado não é alias do scratch: sobrescrever o
		// scratch depois não pode alterar o resultado já devolvido.
		antes := append([]byte(nil), b1...)
		_, _ = EncodeDirtyRectsScratch(frame, [][]DirtyRect{{{X: 0, Y: 0, Width: 64, Height: 64}}}[0], 10, sc)
		if !bytes.Equal(b1, antes) {
			t.Fatalf("caso %d: o resultado devolvido é ALIAS do scratch (corrupção futura)", i)
		}
	}
}

// TestEncodeDirtyRectsScratchCanaisCorretos confere que a conversão BGRA→RGBA
// por palavra preserva a cor: um tile de cor conhecida decodifica com os canais
// certos (o swap invertido mostraria vermelho/azul trocados no viewer).
func TestEncodeDirtyRectsScratchCanaisCorretos(t *testing.T) {
	frame := &Frame{Data: make([]byte, 64*64*4), Width: 64, Height: 64, Stride: 64 * 4}
	// Preenche com BGRA = (B=0x10, G=0x20, R=0x30, A=0xFF).
	for i := 0; i < len(frame.Data); i += 4 {
		frame.Data[i] = 0x10
		frame.Data[i+1] = 0x20
		frame.Data[i+2] = 0x30
		frame.Data[i+3] = 0xFF
	}
	sc := &TileScratch{}
	img := sc.extractTile(frame, DirtyRect{X: 0, Y: 0, Width: 32, Height: 32})
	if img == nil {
		t.Fatal("extractTile devolveu nil")
	}
	// image.RGBA espera R,G,B,A nessa ordem.
	if img.Pix[0] != 0x30 || img.Pix[1] != 0x20 || img.Pix[2] != 0x10 || img.Pix[3] != 0xFF {
		t.Fatalf("canais errados: RGBA = %#x %#x %#x %#x (esperado 30 20 10 ff)",
			img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3])
	}
	// O stride da imagem precisa ser compacto (Width*4), não o do frame.
	if img.Stride != 32*4 {
		t.Fatalf("stride da imagem = %d, esperado %d", img.Stride, 32*4)
	}
}

// TestExtractTileReusoDimensoes garante que reaproveitar a imagem entre tiles de
// tamanhos diferentes não deixa lixo do tile anterior.
func TestExtractTileReusoDimensoes(t *testing.T) {
	frame := benchFrameBGRA(200, 200, 5)
	sc := &TileScratch{}

	// Tile grande primeiro (aloca), depois um pequeno (reusa).
	big := sc.extractTile(frame, DirtyRect{X: 0, Y: 0, Width: 100, Height: 100})
	if big == nil || big.Rect.Dx() != 100 {
		t.Fatal("tile grande inválido")
	}
	small := sc.extractTile(frame, DirtyRect{X: 10, Y: 10, Width: 20, Height: 20})
	if small == nil {
		t.Fatal("tile pequeno inválido")
	}
	if small.Rect.Dx() != 20 || small.Rect.Dy() != 20 {
		t.Fatalf("dimensões erradas no reuso: %v", small.Rect)
	}
	if small.Stride != 20*4 {
		t.Fatalf("stride errado no reuso: %d", small.Stride)
	}
	if len(small.Pix) != 20*20*4 {
		t.Fatalf("buffer errado no reuso: %d", len(small.Pix))
	}
	// O conteúdo do tile pequeno precisa corresponder ao frame nos offsets certos.
	want := frame.Data[10*frame.Stride+10*4+2] // R de (10,10) em BGRA
	if small.Pix[0] != want {
		t.Fatalf("pixel (10,10) = %#x, esperado %#x", small.Pix[0], want)
	}
}
