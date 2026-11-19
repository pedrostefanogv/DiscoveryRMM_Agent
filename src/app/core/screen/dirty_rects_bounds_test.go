package screen

import (
	"testing"
)

// TestTileChangedNaoLeForaDoBuffer faz varredura adversarial de strides e
// dimensões para provar que a comparação por palavra NUNCA lê fora dos buffers.
// Roda com -race e também com o detector de bounds do Go (panic em OOB), então
// é uma prova executável, não um argumento.
func TestTileChangedNaoLeForaDoBuffer(t *testing.T) {
	casos := []struct {
		nome         string
		w, h, stride int
		tile         int
	}{
		{"stride compacto", 64, 64, 64 * 4, 32},
		{"stride com padding", 64, 64, 64*4 + 37, 32},
		{"stride MENOR que width*4", 64, 64, 64, 32}, // malformado
		{"stride zero", 64, 64, 0, 32},
		{"tile maior que o frame", 10, 10, 0, 32},
		{"menos de 8 bytes por linha", 1, 4, 0, 32},
		{"linha de 8 bytes exatos", 2, 4, 0, 32},
		{"altura 0", 64, 0, 0, 32},
		{"largura 0", 0, 64, 0, 32},
		{"tileSize minimo", 64, 64, 0, 8},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			// Alocação EXATA: qualquer leitura além do fim panica.
			stride := c.stride
			if stride <= 0 {
				stride = c.w * 4
			}
			if stride <= 0 {
				stride = 4
			}
			n := stride * c.h
			if n < 0 {
				n = 0
			}
			cur := &Frame{Data: make([]byte, n), Width: c.w, Height: c.h, Stride: stride}
			prev := make([]byte, n)
			// Diferença no último byte: força o caminho de cauda.
			if n > 0 {
				cur.Data[n-1] = 0xFF
			}
			d := &DirtyDetector{lastFrame: prev, lastWidth: c.w, lastHeight: c.h, tileSize: c.tile}
			cols := (c.w + c.tile - 1) / c.tile
			rows := (c.h + c.tile - 1) / c.tile
			if cols < 0 {
				cols = 0
			}
			if rows < 0 {
				rows = 0
			}
			for row := 0; row < rows; row++ {
				for col := 0; col < cols; col++ {
					_ = d.tileChanged(cur, col, row) // panic = bug
				}
			}
		})
	}
}

// TestDetectNuncaLeForaDoBuffer roda o Detect completo (inclui mergeTilesFlat)
// em frames degenerados. Garante que mudanças de dimensão/stride não estouram.
func TestDetectNuncaLeForaDoBuffer(t *testing.T) {
	casos := []struct {
		nome         string
		w, h, stride int
	}{
		{"1080p compacto", 1920, 1080, 0},
		{"1080p com padding", 1920, 1080, 1920*4 + 64},
		{"stride maior que o buffer", 1920, 1080, 1920*4 + 4096},
		{"frame minusculo", 1, 1, 0},
		{"frame vazio", 0, 0, 0},
		{"dimensoes trocadas", 1080, 1920, 0},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			stride := c.stride
			if stride <= 0 {
				stride = c.w * 4
			}
			if stride <= 0 {
				stride = 4
			}
			n := stride * c.h
			if n < 0 {
				n = 0
			}
			d := NewDirtyDetector(32)
			for i := 0; i < 3; i++ {
				f := &Frame{Data: make([]byte, n), Width: c.w, Height: c.h, Stride: stride}
				if n > 0 {
					f.Data[i%n] = byte(i)
				}
				_ = d.Detect(f) // panic = bug
			}
		})
	}
}
