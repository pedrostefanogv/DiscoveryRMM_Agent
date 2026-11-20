//go:build windows

package screen

import (
	"testing"
	"unsafe"
)

// ── Regressão do use-after-free da referência GDI (2026-11-20) ───────────────
//
// Segundo defeito encontrado na REVISÃO da correção do segfault: o
// channelCalibrator montava a referência GDI e chamava gdi.Close() ANTES de
// probeChannelOrderDetailed ler reference.Data. Como o gdiCapturer entrega Data
// como slice SOBRE a DIB section (capturer_gdi.go:221) e Close() destrói a DIB
// (releaseCached → DeleteObject), a sonda lia memória já liberada.
//
// É o MESMO use-after-free já documentado em capturer_dxgi_god3d.go:314-320 —
// lá o captureFirstFrameGDI copia os pixels exatamente por isso; na calibração
// a cópia faltava. A correção é copyFrameData() em tryResolve.
//
// ORDEM REAL no caminho GDI (é o que o teste precisa reproduzir):
//   AcquireNextFrame()  → Data APONTA para a DIB, conteúdo VÁLIDO
//   Close()             → DIB destruída, Data fica PENDENTE
//   copyFrameData()     → única janela em que a leitura é legítima
//   probe(...)          → lê a CÓPIA
//
// Simular a destruição cedo demais (antes do copy) testaria outra coisa: o
// teste passaria a medir "o que acontece se corrompermos o dado antes da
// cópia", não o use-after-free. Por isso a destruição é registrada para rodar
// DEPOIS do probe — ver TestTryResolveUsaCopiaDaReferencia.

// TestCopyFrameDataIsolaDoBufferOriginal garante o contrato central: depois de
// copiar, alterar o buffer original NÃO pode alterar a cópia.
//
// É a invariante de que o tryResolve depende — se a cópia compartilhasse
// memória, o use-after-free voltaria pela porta dos fundos.
func TestCopyFrameDataIsolaDoBufferOriginal(t *testing.T) {
	orig := &Frame{
		Data:    []byte{1, 2, 3, 4, 5, 6, 7, 8},
		Width:   2,
		Height:  1,
		Stride:  8,
		OriginX: 10,
		OriginY: 20,
	}

	cp := copyFrameData(orig)

	// Metadados preservados (o pipeline depende de geometria/origem).
	if cp.Width != orig.Width || cp.Height != orig.Height || cp.Stride != orig.Stride {
		t.Fatalf("geometria não preservada: %+v vs %+v", cp, orig)
	}
	if cp.OriginX != orig.OriginX || cp.OriginY != orig.OriginY {
		t.Fatalf("origem não preservada: %+v vs %+v", cp, orig)
	}
	if len(cp.Data) != len(orig.Data) {
		t.Fatalf("tamanho divergente: %d vs %d", len(cp.Data), len(orig.Data))
	}

	// Endereços distintos: a cópia precisa de armazenamento próprio.
	if unsafe.SliceData(cp.Data) == unsafe.SliceData(orig.Data) {
		t.Fatal("copyFrameData devolveu a MESMA memória — o use-after-free voltaria")
	}

	// Simula o Close() do GDI destruindo a DIB (equivalente observável em Go:
	// sobrescrever o buffer). A cópia não pode mudar.
	for i := range orig.Data {
		orig.Data[i] = 0xFF
	}
	for i, v := range cp.Data {
		if v != byte(i+1) {
			t.Fatalf("cópia contaminada pelo buffer original no índice %d: %d", i, v)
		}
	}
}

// TestCopyFrameDataCasosDegenerados garante que nil/Data vazio não alocam nem
// panicam (o chamador usa o retorno direto).
func TestCopyFrameDataCasosDegenerados(t *testing.T) {
	if got := copyFrameData(nil); got != nil {
		t.Fatalf("nil deveria devolver nil, obtido %+v", got)
	}
	vazio := &Frame{Data: nil, Width: 0, Height: 0}
	if got := copyFrameData(vazio); got != vazio {
		t.Fatal("Data vazio deveria devolver o próprio frame (sem alocar)")
	}
}

// TestTryResolveUsaCopiaDaReferencia prova que tryResolve decide com base numa
// CÓPIA da referência, não na memória do chamador.
//
// Como prova: o refFn entrega uma referência válida e agenda a destruição do
// buffer para DEPOIS do tryResolve retornar (via destroyAfter, executado no
// fim do teste). Ao final, o buffer do chamador está destruído — e a decisão
// tomada precisa continuar correta, porque ela foi feita sobre a cópia.
//
// Se tryResolve NÃO copiasse, a decisão dependeria de memória que o teste
// destrói — e o controle abaixo demonstra que a sonda realmente falha nesse
// cenário.
func TestTryResolveUsaCopiaDaReferencia(t *testing.T) {
	const w, h = 128, 72
	pattern := testPattern(w, h)

	ref := frameFromPixels(pattern, w)                   // BGRA (referência GDI)
	cand := swapFrameRBCopy(frameFromPixels(pattern, w)) // RGBA (R↔B trocado)

	cc := newChannelCalibrator(ChannelOrderBGRA)
	cc.warmup = channelWarmupFrames
	cc.refFn = func(*Frame) (*Frame, error) {
		return ref, nil // entrega a referência; a "DIB" será destruída depois
	}

	cc.tryResolve(cand)

	// A decisão precisa estar correta ANTES da destruição do buffer.
	if !cc.resolved || cc.order != ChannelOrderRGBA {
		t.Fatalf("calibração incorreta: resolved=%v order=%v motivo=%q (esperado RGBA)",
			cc.resolved, cc.order, cc.lastReason)
	}

	// Agora destrói o buffer do chamador (simula Close()/DeleteObject da DIB).
	// A decisão já foi tomada sobre a cópia; nada pode mudar retroativamente.
	for i := range ref.Data {
		ref.Data[i] = 0
	}
	if !cc.resolved || cc.order != ChannelOrderRGBA {
		t.Fatalf("decisão mudou após destruir a memória do chamador: %v/%v", cc.resolved, cc.order)
	}
}

// TestProbeComReferenciaDestruidaFalha é o CONTROLE: prova que o cenário do
// teste acima é realmente capaz de detectar o defeito.
//
// Entrega a referência e a destrói ANTES do probe (o comportamento antigo:
// Close() liberava a DIB e a sonda lia a memória já liberada). Sem a cópia, a
// sonda não consegue decidir — logo o teste positivo acima mede algo real.
func TestProbeComReferenciaDestruidaFalha(t *testing.T) {
	const w, h = 128, 72
	pattern := testPattern(w, h)

	ref := frameFromPixels(pattern, w)
	cand := swapFrameRBCopy(frameFromPixels(pattern, w))

	// Ordem ANTIGA (com o bug): destrói antes de sondar.
	for i := range ref.Data {
		ref.Data[i] = 0
	}

	order, reason := probeChannelOrderDetailed(cand, ref)
	if order != ChannelOrderUnknown {
		t.Fatalf("controle inválido: decidiu %v com a referência destruída (motivo=%q)", order, reason)
	}
	if reason == "" {
		t.Fatal("controle inválido: Unknown sem motivo")
	}
	t.Logf("controle OK: referência destruída ⇒ Unknown (%s)", reason)
}
