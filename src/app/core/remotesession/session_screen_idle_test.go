//go:build windows

package remotesession

import (
	"testing"

	"discovery/app/core/screen"
)

// Testes de frameIsIdle — regressão do bug crítico da revisão 2 (2026-11-19):
// o gate antigo lia rects==nil do caminho HW como "tela ociosa" e descartava
// TODOS os frames após o primeiro, congelando o stream em toda sessão DXGI.
func TestFrameIsIdle(t *testing.T) {
	casos := []struct {
		nome            string
		hw              bool
		rects           []screen.DirtyRect
		useDirtyRect    bool
		keyframePending bool
		quer            bool // true = deve considerar idle
	}{
		{
			// O CASO DO BUG: caminho HW, rects nil, pós-primeiro frame. O gate antigo
			// descartava o frame aqui — o stream congelava na primeira imagem.
			nome: "HW com rects nil NUNCA e idle", hw: true, rects: nil, useDirtyRect: true, keyframePending: false, quer: false,
		},
		{nome: "HW com keyframe pendente", hw: true, rects: nil, useDirtyRect: true, keyframePending: true, quer: false},
		{nome: "SW rects vazio pos-primeiro = idle", hw: false, rects: nil, useDirtyRect: true, keyframePending: false, quer: true},
		{nome: "primeiro frame nunca e idle", hw: false, rects: nil, useDirtyRect: false, keyframePending: false, quer: false},
		{nome: "keyframe pendente nunca e idle", hw: false, rects: nil, useDirtyRect: true, keyframePending: true, quer: false},
		{nome: "rects presentes nunca sao idle", hw: false, rects: []screen.DirtyRect{{X: 0, Y: 0, Width: 10, Height: 10}}, useDirtyRect: true, keyframePending: false, quer: false},
		{nome: "SW vazio com slice vazio nao-nil = idle", hw: false, rects: []screen.DirtyRect{}, useDirtyRect: true, keyframePending: false, quer: true},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := frameIsIdle(c.hw, c.rects, c.useDirtyRect, c.keyframePending)
			if got != c.quer {
				t.Fatalf("frameIsIdle(hw=%v, rects=%d, dirty=%v, kf=%v) = %v, queria %v",
					c.hw, len(c.rects), c.useDirtyRect, c.keyframePending, got, c.quer)
			}
		})
	}
}
