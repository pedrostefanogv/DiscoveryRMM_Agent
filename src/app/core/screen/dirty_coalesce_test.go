package screen

import (
	"testing"
)

// ── Testes da consolidação de dirty rects de HARDWARE (Fase 6, 2026-11-20) ──
//
// O contrato que estes testes protegem NÃO é cosmético: o decoder do viewer
// (RemoteScreenViewer.tsx:298) só reconhece o payload como tiles quando
// 1 <= numRects < 512. Estourar esse teto faz o viewer interpretar um payload
// de tiles como um JPEG único — tela corrompida. Por isso a limitação é
// testada como requisito, não como otimização.

func TestCoalesceDirtyRectsVazio(t *testing.T) {
	if got := CoalesceDirtyRects(nil, 1920, 1080); got != nil {
		t.Fatalf("nil deve devolver nil, veio %v", got)
	}
	if got := CoalesceDirtyRects([]DirtyRect{}, 1920, 1080); got != nil {
		t.Fatalf("vazio deve devolver nil, veio %v", got)
	}
	// Dimensões inválidas: não há como recortar — nil é o contrato.
	if got := CoalesceDirtyRects([]DirtyRect{{X: 0, Y: 0, Width: 10, Height: 10}}, 0, 0); got != nil {
		t.Fatalf("dimensão zero deve devolver nil, veio %v", got)
	}
}

func TestCoalesceDirtyRectsRecortaNasBordas(t *testing.T) {
	// Um rect que passa das bordas precisa ser recortado, nunca descartado:
	// descartar deixaria aquela região desatualizada na tela do viewer.
	got := CoalesceDirtyRects([]DirtyRect{{X: -50, Y: -50, Width: 200, Height: 200}}, 100, 100)
	if len(got) != 1 {
		t.Fatalf("esperava 1 rect, veio %d: %v", len(got), got)
	}
	r := got[0]
	if r.X != 0 || r.Y != 0 || r.Width != 100 || r.Height != 100 {
		t.Fatalf("recorte errado: %+v (esperado 0,0 100x100)", r)
	}
}

func TestCoalesceDirtyRectsDescartaForaDoFrame(t *testing.T) {
	// Totalmente fora do frame: nada a atualizar.
	got := CoalesceDirtyRects([]DirtyRect{{X: 5000, Y: 5000, Width: 10, Height: 10}}, 1920, 1080)
	if got != nil {
		t.Fatalf("rect fora do frame deve sumir, veio %v", got)
	}
	// Dimensão degenerada também some.
	got = CoalesceDirtyRects([]DirtyRect{{X: 10, Y: 10, Width: 0, Height: 10}}, 1920, 1080)
	if got != nil {
		t.Fatalf("rect de largura zero deve sumir, veio %v", got)
	}
}

func TestCoalesceDirtyRectsUneSobrepostos(t *testing.T) {
	// Dois rects sobrepostos viram um só, cobrindo a união.
	got := CoalesceDirtyRects([]DirtyRect{
		{X: 0, Y: 0, Width: 100, Height: 100},
		{X: 50, Y: 50, Width: 100, Height: 100},
	}, 1920, 1080)
	if len(got) != 1 {
		t.Fatalf("esperava 1 rect unido, veio %d: %v", len(got), got)
	}
	r := got[0]
	if r.X != 0 || r.Y != 0 || r.Width != 150 || r.Height != 150 {
		t.Fatalf("união errada: %+v (esperado 0,0 150x150)", r)
	}
}

func TestCoalesceDirtyRectsUneAdjacentes(t *testing.T) {
	// Tocar na borda conta como unir: dois tiles lado a lado viram um JPEG só
	// (header menor + compressão melhor) — mesma lógica do MeshAgent.
	got := CoalesceDirtyRects([]DirtyRect{
		{X: 0, Y: 0, Width: 64, Height: 64},
		{X: 64, Y: 0, Width: 64, Height: 64},
	}, 1920, 1080)
	if len(got) != 1 {
		t.Fatalf("esperava 1 rect (adjacentes unem), veio %d: %v", len(got), got)
	}
	if got[0].Width != 128 || got[0].Height != 64 {
		t.Fatalf("união de adjacentes errada: %+v (esperado 128x64)", got[0])
	}
}

func TestCoalesceDirtyRectsMantemSeparados(t *testing.T) {
	// Rects distantes NÃO podem ser fundidos: fundir criaria um retângulo
	// gigante cobrindo área que não mudou (banda desperdiçada).
	got := CoalesceDirtyRects([]DirtyRect{
		{X: 0, Y: 0, Width: 20, Height: 20},
		{X: 1900, Y: 1000, Width: 20, Height: 20},
	}, 1920, 1080)
	if len(got) != 2 {
		t.Fatalf("esperava 2 rects separados, veio %d: %v", len(got), got)
	}
}

// TestCoalesceDirtyRectsRespeitaTetoDoProtocolo é o teste de CORREÇÃO mais
// importante deste arquivo: o viewer rejeita numRects >= 512.
func TestCoalesceDirtyRectsRespeitaTetoDoProtocolo(t *testing.T) {
	// Simula um arrasto de janela agressivo: centenas de rects pequenos e
	// espalhados, como o DXGI costuma reportar. A grade é densa (a cada 20 px)
	// para garantir que o cenário passe mesmo do teto de 512 — do contrário o
	// teste não exercita a limitação que ele existe para proteger.
	var rects []DirtyRect
	for y := 0; y < 1080; y += 20 {
		for x := 0; x < 1920; x += 20 {
			if (x/20+y/20)%2 == 0 {
				rects = append(rects, DirtyRect{X: x, Y: y, Width: 12, Height: 12})
			}
		}
	}
	if len(rects) < 512 {
		t.Fatalf("o cenário de teste precisa passar de 512 rects, tem %d", len(rects))
	}

	got := CoalesceDirtyRects(rects, 1920, 1080)
	if len(got) == 0 {
		t.Fatal("não pode devolver vazio com entradas válidas")
	}
	if len(got) >= 512 {
		t.Fatalf("ESTOUROU o teto do protocolo: %d rects (o viewer exige < 512)", len(got))
	}
	if len(got) > maxDirtyRectsProtocol {
		t.Fatalf("excedeu o alvo interno: %d > %d", len(got), maxDirtyRectsProtocol)
	}
}

// TestCoalesceDirtyRectsNaoPerdeCobertura garante que LIMITAR não descarta
// região: cada pixel coberto na entrada continua coberto na saída. Cortar
// tiles deixaria a região sem atualização (artefato permanente no viewer);
// fundir só paga banda extra.
func TestCoalesceDirtyRectsNaoPerdeCobertura(t *testing.T) {
	const w, h = 800, 600
	var rects []DirtyRect
	for y := 0; y < h; y += 40 {
		for x := 0; x < w; x += 40 {
			if (x/40+y/40)%2 == 0 {
				rects = append(rects, DirtyRect{X: x, Y: y, Width: 20, Height: 20})
			}
		}
	}

	got := CoalesceDirtyRects(rects, w, h)
	// Amostra pontos de cada rect original: todos devem estar cobertos.
	for _, orig := range rects {
		cx, cy := orig.X+orig.Width/2, orig.Y+orig.Height/2
		covered := false
		for _, g := range got {
			if cx >= g.X && cx < g.X+g.Width && cy >= g.Y && cy < g.Y+g.Height {
				covered = true
				break
			}
		}
		if !covered {
			t.Fatalf("ponto (%d,%d) do rect %+v ficou DESCOBERTO após a consolidação", cx, cy, orig)
		}
	}
}

func TestCoalesceDirtyRectsOrdenaPorArea(t *testing.T) {
	// Maiores primeiro: o viewer desenha na ordem, e mandar a base maior
		// antes reduz trabalho de sobreposição no canvas.
	got := CoalesceDirtyRects([]DirtyRect{
		{X: 900, Y: 900, Width: 10, Height: 10},
		{X: 0, Y: 0, Width: 200, Height: 200},
	}, 1920, 1080)
	if len(got) != 2 {
		t.Fatalf("esperava 2, veio %d", len(got))
	}
	if got[0].Width*got[0].Height < got[1].Width*got[1].Height {
		t.Fatalf("ordem errada: %+v", got)
	}
}

func TestCoalesceDirtyRectsNaoMutaEntrada(t *testing.T) {
	// O slice de entrada pode ser o buffer do capturer, reutilizado entre
	// frames — mutá-lo corromperia o próximo frame.
	in := []DirtyRect{{X: 0, Y: 0, Width: 100, Height: 100}}
	copyOf := in[0]
	_ = CoalesceDirtyRects(in, 1920, 1080)
	if in[0] != copyOf {
		t.Fatalf("a entrada foi mutada: %+v (era %+v)", in[0], copyOf)
	}
}

func TestDirtyRectsCoverage(t *testing.T) {
	// Cobertura total.
	full := DirtyRectsCoverage([]DirtyRect{{X: 0, Y: 0, Width: 1920, Height: 1080}}, 1920, 1080)
	if full < 0.95 {
		t.Fatalf("tela inteira deveria dar cobertura ~1, deu %.2f", full)
	}
	// Cobertura pequena.
	small := DirtyRectsCoverage([]DirtyRect{{X: 0, Y: 0, Width: 19, Height: 10}}, 1920, 1080)
	if small > 0.05 {
		t.Fatalf("rect pequeno não pode dar cobertura alta, deu %.2f", small)
	}
	// Sem rects.
	if c := DirtyRectsCoverage(nil, 1920, 1080); c != 0 {
		t.Fatalf("sem rects deve dar 0, deu %.2f", c)
	}
}
