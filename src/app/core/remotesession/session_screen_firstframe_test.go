//go:build windows

package remotesession

import (
	"testing"

	"discovery/app/core/screen"
)

// TestFirstFrameIsKeyFrame trava a regressão do bug de 2026-11-07: o primeiro
// frame publicado de uma sessão/capturer PRECISA ser enviado COMPLETO (key
// frame, sem tiles), porque é a base sobre a qual o viewer desenha a tela.
//
// O bug original: "s.useDirtyRect = true" rodava ANTES de
// "fullFrame := !s.useDirtyRect", então a expressão era sempre false, o
// comentário "primeiro frame = key frame" era código morto e o primeiro frame
// só saía completo por acidente. Sintoma no viewer: tela remota chegando com
// faixas pretas, "carregando" só conforme o mouse gerava novos dirty rects.
//
// O teste exercita a função de produção decideFrameKind (não uma cópia da
// lógica) para que a regra não volte a ser quebrada sem falhar aqui.
func TestFirstFrameIsKeyFrame(t *testing.T) {
	tests := []struct {
		name          string
		useDirtyRect  bool // estado ANTES do frame
		forceKeyFrame bool // forceKeyFrame armado (viewer pediu, gate, etc.)
		rects         []screen.DirtyRect
		tileMode      bool
		wantFullFrame bool
	}{
		{
			name:          "primeiro frame da sessao vai completo",
			useDirtyRect:  false,
			rects:         []screen.DirtyRect{{X: 0, Y: 0, Width: 32, Height: 32}},
			tileMode:      true,
			wantFullFrame: true,
		},
		{
			name:          "primeiro frame vai completo mesmo sem rects",
			useDirtyRect:  false,
			rects:         nil,
			tileMode:      true,
			wantFullFrame: true,
		},
		{
			name:          "primeiro frame vai completo com tile-mode desligado",
			useDirtyRect:  false,
			rects:         []screen.DirtyRect{{X: 0, Y: 0, Width: 8, Height: 8}},
			tileMode:      false,
			wantFullFrame: true,
		},
		{
			name:          "segundo frame com mudanca pequena vira tile",
			useDirtyRect:  true,
			rects:         []screen.DirtyRect{{X: 0, Y: 0, Width: 32, Height: 32}},
			tileMode:      true,
			wantFullFrame: false,
		},
		{
			name:          "forceKeyFrame sempre entrega completo",
			useDirtyRect:  true,
			forceKeyFrame: true,
			rects:         []screen.DirtyRect{{X: 0, Y: 0, Width: 32, Height: 32}},
			tileMode:      true,
			wantFullFrame: true,
		},
		{
			name:          "rect cobrindo ~100% da tela vira completo mesmo em tile-mode",
			useDirtyRect:  true,
			rects:         []screen.DirtyRect{{X: 0, Y: 0, Width: 100, Height: 100}},
			tileMode:      true,
			wantFullFrame: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &SessionScreen{}
			s.useDirtyRect = tc.useDirtyRect
			s.tileMode = tc.tileMode
			if tc.forceKeyFrame {
				s.forceKeyFrame.Store(true)
			}

			frame := &screen.Frame{Width: 100, Height: 100, Stride: 400}
			fullFrame, jobRects := s.decideFrameKind(frame, tc.rects)

			if fullFrame != tc.wantFullFrame {
				t.Fatalf("fullFrame = %v, esperado %v", fullFrame, tc.wantFullFrame)
			}
			// Contrato do encode: frame completo NUNCA leva tiles (o encode
			// worker usa job.rects != nil para escolher EncodeDirtyRects).
			if fullFrame && jobRects != nil {
				t.Fatalf("frame completo não pode levar tiles, veio %v", jobRects)
			}
			if !fullFrame && tc.tileMode && len(tc.rects) > 0 && jobRects == nil {
				t.Fatal("tile-mode com rects deveria produzir jobRects")
			}
			// Após classificar um frame, a base existe.
			if !s.useDirtyRect {
				t.Fatal("useDirtyRect deve estar armado após classificar um frame")
			}
		})
	}
}

// TestFirstFrameFlagOrder garante a ORDEM das operações: a flag useDirtyRect só
// pode virar true DEPOIS de o frame corrente ser classificado. Se voltar a
// subir antes, o primeiro frame deixa de ser key frame e o bug reaparece
// silenciosamente — foi exatamente o que aconteceu antes.
func TestFirstFrameFlagOrder(t *testing.T) {
	s := &SessionScreen{useDirtyRect: false, tileMode: true} // primeira publicação
	frame := &screen.Frame{Width: 64, Height: 64, Stride: 256}

	fullFrame, _ := s.decideFrameKind(frame, []screen.DirtyRect{{X: 0, Y: 0, Width: 8, Height: 8}})
	if !fullFrame {
		t.Fatal("primeiro frame deveria ser key frame")
	}
	if !s.useDirtyRect {
		t.Fatal("após classificar um frame, useDirtyRect deve estar armado")
	}

	// Segunda passada: a base já existe, o diff pode ser usado.
	fullFrame2, rects2 := s.decideFrameKind(frame, []screen.DirtyRect{{X: 0, Y: 0, Width: 8, Height: 8}})
	if fullFrame2 {
		t.Fatal("segundo frame com mudança pequena NÃO deveria ser completo")
	}
	if len(rects2) == 0 {
		t.Fatal("segundo frame deveria ir em tiles")
	}
}

// TestForceKeyFrameConsumedOnce garante que o pedido de keyframe é consumido
// por UM frame (não fica reenviando tela inteira eternamente) — o flag usa
// CompareAndSwap e precisa zerar após o uso.
func TestForceKeyFrameConsumedOnce(t *testing.T) {
	s := &SessionScreen{}
	frame := &screen.Frame{Width: 64, Height: 64, Stride: 256}
	rects := []screen.DirtyRect{{X: 0, Y: 0, Width: 8, Height: 8}}

	s.forceKeyFrame.Store(true)
	if full, _ := s.decideFrameKind(frame, rects); !full {
		t.Fatal("keyframe pedido deveria forçar frame completo")
	}
	if s.forceKeyFrame.Load() {
		t.Fatal("forceKeyFrame deveria ter sido consumido")
	}
	if full, _ := s.decideFrameKind(frame, rects); full {
		t.Fatal("sem novo pedido, o frame seguinte volta a ser tile")
	}
}
