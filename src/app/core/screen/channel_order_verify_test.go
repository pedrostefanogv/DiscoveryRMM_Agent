//go:build windows

package screen

import (
	"testing"
	"time"
)

// gdiLikeRef monta uma "referência GDI" (BGRA canônico) e um frame candidato
// com a ordem pedida: bgra=true entrega BGRA; false entrega RGBA (swap).
func gdiLikeRef(w, h int, bgra bool) (*Frame, *Frame) {
	pattern := testPattern(w, h)
	ref := frameFromPixels(pattern, w)
	cand := frameFromPixels(pattern, w)
	if !bgra {
		swapFrameRB(cand)
	}
	return ref, cand
}

func advanceCalibClock(cc *channelCalibrator) {
	cc.lastAttempt = time.Now().Add(-time.Hour)
}

// TestChannelCalibratorConfirmSeed: seed determinística correta é confirmada
// pela calibração e a verificação para (não custa capturas GDI a sessão toda).
func TestChannelCalibratorConfirmSeed(t *testing.T) {
	resetChannelOrderCache()
	defer resetChannelOrderCache()

	const w, h = 128, 72
	ref, cand := gdiLikeRef(w, h, true) // capturer de fato entrega BGRA
	cc := newChannelCalibratorWithKey("k-confirm", ChannelOrderBGRA)
	cc.refFn = func(*Frame) (*Frame, error) { return ref, nil }

	for i := 0; i < 10; i++ {
		f := &Frame{Data: append([]byte(nil), cand.Data...), Width: w, Height: h, Stride: w * 4}
		advanceCalibClock(&cc)
		cc.apply(f)
	}
	if !cc.verified {
		t.Fatal("seed correta deveria ser confirmada (verified=true)")
	}
	if cc.Order() != ChannelOrderBGRA {
		t.Fatalf("ordem = %v, want BGRA", cc.Order())
	}
}

// TestChannelCalibratorSelfHealWrongSeed: seed determinística ERRADA é
// contrarrestada pela calibração (self-heal) em vez de ficar invertida para
// sempre — o risco novo introduzido ao confiar na sonda do DuplicateOutput1.
func TestChannelCalibratorSelfHealWrongSeed(t *testing.T) {
	resetChannelOrderCache()
	defer resetChannelOrderCache()

	const w, h = 128, 72
	ref, cand := gdiLikeRef(w, h, false) // capturer entrega RGBA
	cc := newChannelCalibratorWithKey("k-heal", ChannelOrderBGRA)
	cc.refFn = func(*Frame) (*Frame, error) { return ref, nil }

	for i := 0; i < 10; i++ {
		f := &Frame{Data: append([]byte(nil), cand.Data...), Width: w, Height: h, Stride: w * 4}
		advanceCalibClock(&cc)
		cc.apply(f)
		if i >= 2 {
			// Depois da correção o apply deve trocar R↔B (ordem RGBA).
			if cc.Order() == ChannelOrderRGBA && f.Data[0] != cand.Data[2] {
				t.Fatalf("frame não foi normalizado para BGRA (ordem=%v)", cc.Order())
			}
		}
	}
	if cc.Order() != ChannelOrderRGBA {
		t.Fatalf("ordem = %v, want RGBA (seed BGRA corrigida)", cc.Order())
	}
	if got, _ := seedChannelOrder("k-heal"); got != ChannelOrderRGBA {
		t.Fatalf("cache = %v, want RGBA (decisão corrigida deve valer p/ próximas instâncias)", got)
	}
}

// TestChannelCalibratorGiveUpVerification: sem sinal decisivo (tela
// acromática/GDI falhando) a verificação da seed ENCERRA após o limite —
// não pode custar uma captura GDI por 200 ms durante a sessão inteira.
func TestChannelCalibratorGiveUpVerification(t *testing.T) {
	resetChannelOrderCache()
	defer resetChannelOrderCache()

	const w, h = 128, 72
	gray := make([][3]byte, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := byte((x + y) % 256)
			gray = append(gray, [3]byte{v, v, v})
		}
	}
	ref := frameFromPixels(gray, w)
	cc := newChannelCalibratorWithKey("k-giveup", ChannelOrderBGRA)
	calls := 0
	cc.refFn = func(*Frame) (*Frame, error) { calls++; return ref, nil }

	for i := 0; i < 40; i++ {
		f := &Frame{Data: append([]byte(nil), ref.Data...), Width: w, Height: h, Stride: w * 4}
		advanceCalibClock(&cc)
		cc.apply(f)
	}
	if !cc.verified {
		t.Fatal("verificação deveria encerrar após channelVerifyMaxAttempts")
	}
	if calls > channelVerifyMaxAttempts {
		t.Fatalf("referência consultada %d× (limite %d)", calls, channelVerifyMaxAttempts)
	}
	if cc.Order() != ChannelOrderBGRA {
		t.Fatalf("ordem = %v, want BGRA (seed mantida)", cc.Order())
	}
}
