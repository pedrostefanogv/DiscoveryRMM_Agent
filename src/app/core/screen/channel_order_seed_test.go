//go:build windows

package screen

import "testing"

// TestNewChannelCalibratorWithKeySeed cobre a precedência cache > seed >
// palpite introduzida com a correção das cores invertidas (incidente de
// 2026-10-29): a ordem determinística (DuplicateOutput1) e o cache do processo
// devem produzir frames corretos desde o primeiro.
func TestNewChannelCalibratorWithKeySeed(t *testing.T) {
	resetChannelOrderCache()
	defer resetChannelOrderCache()

	// 1. Sem seed e sem cache: palpite BGRA, pendente de verificação.
	cc := newChannelCalibratorWithKey("k1", ChannelOrderUnknown)
	if cc.Resolved() {
		t.Fatal("sem seed/cache não deve estar resolvido")
	}
	if cc.Order() != ChannelOrderBGRA {
		t.Fatalf("palpite = %v, want BGRA", cc.Order())
	}

	// 2. Seed determinística: resolvido desde o 1º frame.
	cc = newChannelCalibratorWithKey("k2", ChannelOrderRGBA)
	if !cc.Resolved() || cc.Order() != ChannelOrderRGBA {
		t.Fatalf("seed: resolved=%v order=%v, want true/RGBA", cc.Resolved(), cc.Order())
	}

	// 3. Cache: a decisão de uma instância serve para a próxima (troca de
	// capturer não pode reabrir a janela de cor errada).
	cc = newChannelCalibratorWithKey("k2", ChannelOrderUnknown)
	if !cc.Resolved() || cc.Order() != ChannelOrderRGBA {
		t.Fatalf("cache: resolved=%v order=%v, want true/RGBA", cc.Resolved(), cc.Order())
	}
}

// TestChannelCalibratorCacheSurvivesNewInstance garante que a decisão
// resolvida por calibração é lembrada para a próxima instância do mesmo
// capturer (mesma chave).
func TestChannelCalibratorCacheSurvivesNewInstance(t *testing.T) {
	resetChannelOrderCache()
	defer resetChannelOrderCache()

	const key = "gdi-test|mon=0|100x50"
	rememberChannelOrder(key, ChannelOrderRGBA)

	cc := newChannelCalibratorWithKey(key, ChannelOrderBGRA)
	if !cc.Resolved() || cc.Order() != ChannelOrderRGBA {
		t.Fatalf("cache deve vencer o palpite: resolved=%v order=%v", cc.Resolved(), cc.Order())
	}
	// Com a ordem RGBA, apply deve trocar R↔B do frame.
	f := frameFromPixels([][3]byte{{10, 20, 30}}, 1)
	cc.apply(f)
	if f.Data[0] != 10 || f.Data[2] != 30 {
		t.Fatalf("apply não trocou R↔B: %v", f.Data[:3])
	}
}

// TestProbeChannelOrderDetailedReportsReason garante que a sonda devolve
// motivo (o incidente de 2026-10-29 ficou 68 s sem nenhuma linha de log
// explicando as tentativas perdidas).
func TestProbeChannelOrderDetailedReportsReason(t *testing.T) {
	const w, h = 128, 72
	ref := frameFromPixels(testPattern(w, h), w)
	_, reason := probeChannelOrderDetailed(frameFromPixels(testPattern(w, h), w), ref)
	if reason == "" {
		t.Fatal("motivo vazio para decisão BGRA")
	}
	gray := make([][3]byte, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := byte((x + y) % 256)
			gray = append(gray, [3]byte{v, v, v})
		}
	}
	g := frameFromPixels(gray, w)
	order, reason := probeChannelOrderDetailed(swapFrameRBCopy(g), g)
	if order != ChannelOrderUnknown || reason == "" {
		t.Fatalf("ordem=%v motivo=%q, want Unknown com motivo", order, reason)
	}
}

// swapFrameRBCopy devolve uma cópia com R↔B trocado (não muta a original).
func swapFrameRBCopy(f *Frame) *Frame {
	out := &Frame{Data: append([]byte(nil), f.Data...), Width: f.Width, Height: f.Height, Stride: f.Stride}
	swapRB_BGRA(out.Data)
	return out
}
