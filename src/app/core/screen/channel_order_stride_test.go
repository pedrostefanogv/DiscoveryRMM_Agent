//go:build windows

package screen

import (
	"testing"
)

// ── Regressão do segfault de 2026-11-20 ──────────────────────────────────────
//
// Incidente: o worker de sessão remota morria em loop (88 faults + 44
// relançamentos só em 2026-11-20) com o topo do stack SEMPRE em
// probeChannelOrderDetailed e endereço de falha sempre 0x...000 (página não
// mapeada). O watchdog relançava o worker a cada ~2 s, antes de ele publicar
// qualquer frame — a sessão remota nunca funcionava.
//
// Causa: os offsets eram calculados com Frame.Stride, mas o guard de limites
// validava contra len(Frame.Data). Como Data é um buffer REUTILIZADO entre
// frames (frameBuf no DXGI, dibBits na DIB do GDI), existe uma janela real em
// que Stride > o que o buffer comporta — o guard passava e o índice caía fora
// da página mapeada.
//
// Os testes abaixo cobrem as invariantes que impedem a volta do defeito:
//   1. Stride declarado maior que o buffer não pode ser usado para indexar.
//   2. A sonda NUNCA indexa fora de Data (verificado por construção do frame).
//   3. safeFrameStride é a barreira única: se ele devolve > 0, a indexação
//      até (h-1, w-1) é segura.

// frameComStride monta um frame com Data de exatamente dataLen bytes e Stride
// DECLARADO arbitrário — é a situação que o capturer real produz quando o
// buffer foi recriado/encolhido e o Stride ainda reflete a geometria antiga.
func frameComStride(w, h, stride, dataLen int) *Frame {
	return &Frame{
		Data:   make([]byte, dataLen),
		Width:  w,
		Height: h,
		Stride: stride,
	}
}

// TestProbeChannelOrderStrideMaiorQueBufferNaoCrasha é o teste de regressão
// direto: antes da correção este cenário lia fora do buffer (segfault); agora
// a sonda precisa devolver Unknown sem panicar.
//
// O Stride declarado (muito maior que o buffer) é exatamente o gatilho: o
// guard antigo "co+3 >= len(Data)" passava para y pequeno e estourava depois.
func TestProbeChannelOrderStrideMaiorQueBufferNaoCrasha(t *testing.T) {
	const w, h = 64, 64
	// Buffer do tamanho correto (w*4*h) mas Stride declarado 10x maior.
	cand := frameComStride(w, h, w*4*10, w*4*h)
	ref := frameComStride(w, h, w*4*10, w*4*h)

	// Conteúdo informativo: sem isso a sonda sai cedo por "poucas amostras" e
	// o teste não exercitaria o caminho de indexação.
	for i := 0; i+3 < len(ref.Data); i += 4 {
		ref.Data[i] = byte(i % 251)       // B
		ref.Data[i+1] = byte((i * 7) % 251) // G
		ref.Data[i+2] = byte((i * 13) % 251) // R
		ref.Data[i+3] = 255
	}
	copy(cand.Data, ref.Data)

	order, reason := probeChannelOrderDetailed(cand, ref)
	// O essencial: NÃO pode panicar. A ordem pode ser Unknown (o stride seguro
	// reduz a área amostrada) — o contrato é "nunca derrubar o worker".
	t.Logf("order=%v reason=%q", order, reason)
	if order == ChannelOrderUnknown && reason == "" {
		t.Fatal("Unknown sem motivo: o diagnóstico precisa ser registrado")
	}
}

// TestProbeChannelOrderStrideZeroOuNegativo garante que um Stride inválido
// (0 ou negativo) cai para a largura em bytes em vez de zerar os offsets —
// offsets zerados fariam todas as linhas lerem a linha 0 (decisão silenciosa
// errada em vez de falha explícita).
func TestProbeChannelOrderStrideZeroOuNegativo(t *testing.T) {
	for _, stride := range []int{0, -1, -1024} {
		w, h := 32, 32
		cand := frameComStride(w, h, stride, w*4*h)
		ref := frameComStride(w, h, stride, w*4*h)
		for i := 0; i+3 < len(ref.Data); i += 4 {
			ref.Data[i+2] = byte(i % 251)
			ref.Data[i] = byte((i * 3) % 251)
			ref.Data[i+3] = 255
		}
		copy(cand.Data, ref.Data)

		got := safeFrameStride(cand, w, h)
		if got != w*4 {
			t.Fatalf("stride=%d: safeFrameStride=%d, esperado %d (largura em bytes)", stride, got, w*4)
		}
		// Não pode panicar.
		_, _ = probeChannelOrderDetailed(cand, ref)
	}
}

// TestSafeFrameStrideNuncaExcedeBuffer é a invariante central da correção:
// sempre que safeFrameStride devolve um valor > 0, indexar até a última linha
// e a última coluna amostrada permanece DENTRO de Data.
//
// Este é o teste que fecha a classe do defeito — não apenas o caso concreto do
// incidente, mas qualquer combinação de geometria/stride/buffer.
func TestSafeFrameStrideNuncaExcedeBuffer(t *testing.T) {
	casos := []struct {
		nome          string
		w, h, stride  int
		dataLen       int
	}{
		{"buffer exato, stride correto", 64, 64, 64 * 4, 64 * 4 * 64},
		{"stride maior que buffer", 64, 64, 64 * 4 * 10, 64 * 4 * 64},
		{"stride absurdo", 64, 64, 1 << 20, 64 * 4 * 64},
		{"buffer menor que h linhas", 64, 64, 64 * 4, 64 * 4 * 10},
		{"buffer de uma linha", 64, 64, 64 * 4, 64 * 4},
		{"buffer minúsculo", 64, 64, 64 * 4, 16},
		{"buffer vazio", 64, 64, 64 * 4, 0},
		{"largura 1", 1, 1, 4, 4},
		{"h=1", 64, 1, 64 * 4, 64 * 4},
		{"stride com padding legítimo", 63, 64, 64 * 4, 64 * 4 * 64},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			f := frameComStride(tc.w, tc.h, tc.stride, tc.dataLen)
			fw := safeFrameWindow(f, tc.w, tc.h)
			if !fw.ok() {
				return // desistiu explicitamente: nenhuma indexação acontece
			}
			if fw.stride <= 0 || fw.rows <= 0 {
				t.Fatalf("janela inválida: stride=%d rows=%d", fw.stride, fw.rows)
			}
			if fw.rows > tc.h {
				t.Fatalf("rows=%d excede a altura do frame (%d)", fw.rows, tc.h)
			}
			// Simula a indexação da sonda no pior caso REAL: a última linha da
			// janela segura (rows-1) e a última coluna amostrada (w-1).
			last := (fw.rows-1)*fw.stride + (tc.w-1)*4
			if last+3 >= len(f.Data) {
				t.Fatalf("indexação insegura: offset final=%d, len(Data)=%d (stride=%d rows=%d w=%d h=%d)",
					last+3, len(f.Data), fw.stride, fw.rows, tc.w, tc.h)
			}
			// safeFrameStride precisa concordar com a janela.
			if got := safeFrameStride(f, tc.w, tc.h); got != fw.stride {
				t.Fatalf("safeFrameStride=%d divergente da janela=%d", got, fw.stride)
			}
		})
	}
}

// TestSafeFrameStrideNilEFramesDegenerados cobre as entradas que não podem
// indexar nada — devem devolver 0 (desistir) em vez de calcular lixo.
func TestSafeFrameStrideNilEFramesDegenerados(t *testing.T) {
	if got := safeFrameStride(nil, 10, 10); got != 0 {
		t.Fatalf("frame nil: esperado 0, obtido %d", got)
	}
	if got := safeFrameStride(frameComStride(0, 10, 0, 100), 0, 10); got != 0 {
		t.Fatalf("w=0: esperado 0, obtido %d", got)
	}
	if got := safeFrameStride(frameComStride(10, 0, 40, 100), 10, 0); got != 0 {
		t.Fatalf("h=0: esperado 0, obtido %d", got)
	}
}

// TestProbeChannelOrderFramesNil garante que o caminho de frame ausente
// continua devolvendo Unknown com motivo (contrato pré-existente).
func TestProbeChannelOrderFramesNil(t *testing.T) {
	if order, reason := probeChannelOrderDetailed(nil, nil); order != ChannelOrderUnknown || reason == "" {
		t.Fatalf("nil/nil: order=%v reason=%q", order, reason)
	}
	w := frameComStride(8, 8, 32, 32*8)
	if order, reason := probeChannelOrderDetailed(w, nil); order != ChannelOrderUnknown || reason == "" {
		t.Fatalf("cand/ nil: order=%v reason=%q", order, reason)
	}
}

// TestTryResolveRecoverNaoDerruba garante a defesa em profundidade: um panic
// dentro da calibração desativa a calibração e MANTÉM a ordem corrente, em vez
// de propagar e matar o worker de sessão.
//
// Injeta o panic via refFn — o mesmo ponto de extensão usado pelos testes de
// calibração existentes (não depende de GDI nem de GPU).
func TestTryResolveRecoverNaoDerruba(t *testing.T) {
	cc := newChannelCalibrator(ChannelOrderBGRA)
	cc.refFn = func(*Frame) (*Frame, error) {
		panic("falha simulada da referência GDI")
	}
	// Consome o warmup para chegar ao ponto que chama refFn.
	cc.warmup = channelWarmupFrames

	frame := &Frame{
		Data:   make([]byte, 16*16*4),
		Width:  16,
		Height: 16,
		Stride: 16 * 4,
	}

	// Não pode panicar (o recover precisa absorver).
	cc.tryResolve(frame)

	if !cc.verified {
		t.Fatal("calibração deveria estar encerrada (verified) após o panic")
	}
	if !cc.disabled {
		t.Fatal("calibração deveria estar DESATIVADA após o panic")
	}
	if cc.order != ChannelOrderBGRA {
		t.Fatalf("ordem deveria ser preservada (BGRA), obtido %v", cc.order)
	}
	if cc.lastReason == "" {
		t.Fatal("o motivo do panic deveria ser registrado para diagnóstico")
	}
}
// TestProbeChannelOrderNuncaIndexaForaDoBuffer varre combinações degeneradas de
// geometria/stride/buffer e garante que a sonda completa (não só o helper)
// nunca indexa fora de Data. É o teste de fechamento da classe do defeito.
func TestProbeChannelOrderNuncaIndexaForaDoBuffer(t *testing.T) {
	w, h := 64, 64
	full := w * 4 * h
	comprimentos := []int{0, 16, w * 4, w * 4 * 2, full / 2, full - 1, full, full + 4096}
	strides := []int{0, 1, w * 4, w * 4 * 2, 1 << 20, -8}

	for _, dataLen := range comprimentos {
		for _, stride := range strides {
			cand := frameComStride(w, h, stride, dataLen)
			ref := frameComStride(w, h, stride, dataLen)
			// Preenche o que couber com conteúdo informativo.
			for i := 0; i+3 < len(ref.Data); i += 4 {
				ref.Data[i] = byte(i % 251)
				ref.Data[i+1] = byte((i * 7) % 251)
				ref.Data[i+2] = byte((i * 13) % 251)
				ref.Data[i+3] = 255
			}
			copy(cand.Data, ref.Data)

			// O contrato: nunca panicar, qualquer que seja a combinação.
			order, reason := probeChannelOrderDetailed(cand, ref)
			if order == ChannelOrderUnknown && reason == "" {
				t.Fatalf("dataLen=%d stride=%d: Unknown sem motivo", dataLen, stride)
			}
		}
	}
}
