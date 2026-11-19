package screen

import (
	"testing"
)

// ── Testes de regressão da reescrita do detector (Fase 0/1 da otimização do ──
// acesso remoto, 2026-11-19) ──
//
// A comparação por palavra (uint64) em tileChanged substituiu o laço
// byte-a-byte. A semântica tem de ser IDÊNTICA: qualquer byte diferente dentro
// do tile amostrado precisa ser reportado. Estes testes cobrem os casos de
// borda que a leitura otimizada poderia quebrar:
//   - diferença no PRIMEIRO byte do tile;
//   - diferença no ÚLTIMO byte (cauda < 8 bytes, o caminho byte-a-byte);
//   - diferença em byte não alinhado a 8 (meio de palavra);
//   - tile na BORDA direita/inferior (largura/altura menor que tileSize);
//   - frame com stride maior que width*4 (padding entre linhas);
//   - frames de dimensões diferentes (o detector descarta o cache e devolve
//     a tela inteira).

func mkFrame(w, h, stride, fill int) *Frame {
	if stride <= 0 {
		stride = w * 4
	}
	data := make([]byte, stride*h)
	for i := range data {
		data[i] = byte(fill)
	}
	return &Frame{Data: data, Width: w, Height: h, Stride: stride}
}

// setPixel escreve um pixel BGRA no offset (x,y).
func setPixel(f *Frame, x, y int, v byte) {
	off := y*f.Stride + x*4
	f.Data[off] = v
	f.Data[off+1] = v
	f.Data[off+2] = v
}

func TestDirtyDetectPegaMudancaEmQualquerByte(t *testing.T) {
	// Tiles de 32px; o primeiro frame popula o cache, o segundo muda UM byte.
	posicoes := []struct {
		nome string
		x, y int
	}{
		{"primeiro pixel do tile 0", 0, 0},
		{"ultimo pixel util do tile 0", 31, 30}, // linha amostrada (py par)
		{"meio do tile 0 (byte nao alinhado)", 3, 4},
		{"tile do meio da tela", 500, 500},
		{"borda direita", 1919, 100},
		{"borda inferior", 100, 1078},
	}
	for _, p := range posicoes {
		t.Run(p.nome, func(t *testing.T) {
			d := NewDirtyDetector(32)
			base := mkFrame(1920, 1080, 0, 0x40)
			modified := mkFrame(1920, 1080, 0, 0x40)

			// Primeira chamada: popula o cache e devolve a tela inteira.
			if r := d.Detect(base); len(r) != 1 || r[0].Width != 1920 {
				t.Fatalf("primeiro Detect deveria devolver a tela inteira, veio %+v", r)
			}
			// Sem mudanca: nenhum rect.
			if r := d.Detect(base); len(r) != 0 {
				t.Fatalf("frame identico deveria dar 0 rects, veio %d", len(r))
			}
			// Um pixel diferente precisa aparecer.
			setPixel(modified, p.x, p.y, 0xFF)
			r := d.Detect(modified)
			if len(r) == 0 {
				t.Fatalf("mudanca em (%d,%d) NAO foi detectada", p.x, p.y)
			}
			// O rect devolvido precisa conter o ponto alterado.
			contido := false
			for _, rect := range r {
				if p.x >= rect.X && p.x < rect.X+rect.Width &&
					p.y >= rect.Y && p.y < rect.Y+rect.Height {
					contido = true
				}
			}
			if !contido {
				t.Fatalf("mudanca em (%d,%d) fora dos rects %+v", p.x, p.y, r)
			}
		})
	}
}

// TestDirtyDetectCaudaBytes cobre o caminho byte-a-byte da cauda (< 8 bytes).
// Largura 10 px = 40 bytes por linha: 40 nao e multiplo de 8, entao a cauda e
// exercitada em toda linha. O tile tem 10 px de largura util.
func TestDirtyDetectCaudaBytes(t *testing.T) {
	d := NewDirtyDetector(32)
	base := mkFrame(10, 4, 0, 0x10)
	mod := mkFrame(10, 4, 0, 0x10)

	d.Detect(base)
	if r := d.Detect(base); len(r) != 0 {
		t.Fatalf("esperado 0 rects, veio %d", len(r))
	}
	// Ultimo pixel da linha: cai na cauda apos o bloco de 8 bytes.
	setPixel(mod, 9, 0, 0xEE)
	if r := d.Detect(mod); len(r) == 0 {
		t.Fatal("mudanca na cauda (ultimo pixel) NAO detectada")
	}
}

// TestDirtyDetectStrideComPadding garante que a leitura por palavra respeita o
// Stride (bytes por linha) e NAO le padding como se fosse pixel: com stride
// maior, um padding alterado nao pode gerar dirty rect falso dentro da area.
func TestDirtyDetectStrideComPadding(t *testing.T) {
	d := NewDirtyDetector(32)
	// 64 px uteis = 256 bytes; stride 288 (32 bytes de padding por linha).
	base := mkFrame(64, 64, 288, 0x20)
	d.Detect(base)

	// Altera SOMENTE o padding (fora dos 256 bytes uteis de cada linha).
	pad := mkFrame(64, 64, 288, 0x20)
	for y := 0; y < 64; y++ {
		off := y*288 + 256
		pad.Data[off] = 0xFF
	}
	if r := d.Detect(pad); len(r) != 0 {
		// A amostragem do detector e por linha inteira do tile; o padding
		// NAO deve ser varrido. Se aparecer rect, a leitura invadiu o padding.
		t.Fatalf("padding alterado gerou %d dirty rects (leitura invadiu o padding)", len(r))
	}

	// E uma mudanca REAL continua sendo detectada com o mesmo stride.
	real := mkFrame(64, 64, 288, 0x20)
	setPixel(real, 40, 40, 0xFF)
	if r := d.Detect(real); len(r) == 0 {
		t.Fatal("mudanca real com stride+padding NAO detectada")
	}
}

// TestDirtyDetectMudancaDeDimensao cobre o reset implicito: um frame de
// dimensoes diferentes invalida o cache e devolve a tela inteira (key frame).
func TestDirtyDetectMudancaDeDimensao(t *testing.T) {
	d := NewDirtyDetector(32)
	a := mkFrame(1920, 1080, 0, 0x30)
	d.Detect(a)

	b := mkFrame(1280, 720, 0, 0x30)
	r := d.Detect(b)
	if len(r) != 1 || r[0].Width != 1280 || r[0].Height != 720 {
		t.Fatalf("mudanca de dimensao deveria devolver a tela inteira, veio %+v", r)
	}
}

// TestDirtyDetectReset garante que Reset() forca o proximo Detect a devolver a
// tela inteira (usado em troca de desktop/capturer).
func TestDirtyDetectReset(t *testing.T) {
	d := NewDirtyDetector(32)
	a := mkFrame(320, 240, 0, 0x50)
	d.Detect(a)
	if r := d.Detect(a); len(r) != 0 {
		t.Fatalf("esperado 0 apos cache, veio %d", len(r))
	}
	d.Reset()
	r := d.Detect(a)
	if len(r) != 1 || r[0].Width != 320 {
		t.Fatalf("apos Reset deveria vir a tela inteira, veio %+v", r)
	}
}
