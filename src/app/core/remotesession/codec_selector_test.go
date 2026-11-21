//go:build windows

package remotesession

import (
	"errors"
	"testing"
)

// ── Codec: falha EXPLÍCITA em vez de fallback silencioso (Fase 6, 2026-11-20) ──
//
// Contexto: o projeto já teve um incidente de "H.264 anunciado sem
// implementação" (ver ANALISE_RMM_OPEN_SOURCE_ACESSO_REMOTO). A lição registrada
// foi: um codec pedido e não honrado precisa AVISAR, não cair em silêncio no
// JPEG. Estes testes travam esse contrato.

func TestCodecSelectorAutoUsaJPEG(t *testing.T) {
	cs := NewCodecSelector()
	enc, err := cs.SelectChecked("auto")
	if err != nil {
		t.Fatalf("auto não deve reportar erro: %v", err)
	}
	if enc == nil || enc.Name() != "jpeg" {
		t.Fatalf("auto deve resolver para jpeg, veio %v", enc)
	}
	// Vazio é o mesmo que auto (payload sem codec).
	enc, err = cs.SelectChecked("")
	if err != nil || enc == nil || enc.Name() != "jpeg" {
		t.Fatalf("vazio deve resolver para jpeg sem erro, veio %v / %v", enc, err)
	}
}

func TestCodecSelectorJPEGNaoReportaErro(t *testing.T) {
	cs := NewCodecSelector()
	for _, name := range []string{"jpeg", "jpg", "JPEG"} {
		enc, err := cs.SelectChecked(name)
		if err != nil {
			t.Fatalf("%q não deve reportar erro: %v", name, err)
		}
		if enc == nil || enc.Name() != "jpeg" {
			t.Fatalf("%q deve resolver para jpeg, veio %v", name, enc)
		}
	}
}

// TestCodecSelectorH264FalhaExplicito é o teste da lição do incidente: pedir
// H.264 tem de devolver erro, nunca JPEG silencioso.
func TestCodecSelectorH264FalhaExplicito(t *testing.T) {
	cs := NewCodecSelector()
	enc, err := cs.SelectChecked("h264")
	if err == nil {
		t.Fatal("h264 precisa reportar erro (não é implementado neste agente)")
	}
	var unavail *ErrCodecUnavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("erro deve ser *ErrCodecUnavailable, veio %T: %v", err, err)
	}
	if unavail.Codec != "h264" {
		t.Fatalf("codec no erro deve ser h264, veio %q", unavail.Codec)
	}
	if unavail.Reason == "" {
		t.Fatal("o motivo precisa ser informado ao operador")
	}
	// O fallback AINDA entrega um encoder utilizável: a sessão não pode
	// quebrar só porque o codec pedido não existe.
	if enc == nil || enc.Name() != "jpeg" {
		t.Fatalf("deve entregar fallback jpeg utilizável, veio %v", enc)
	}
	// Alias AVC.
	if _, err := cs.SelectChecked("avc"); err == nil {
		t.Fatal("avc deve ser tratado como h264 (erro explícito)")
	}
}

func TestCodecSelectorDesconhecidoFalhaExplicito(t *testing.T) {
	cs := NewCodecSelector()
	_, err := cs.SelectChecked("codec-que-nao-existe")
	if err == nil {
		t.Fatal("codec desconhecido precisa reportar erro")
	}
	var unavail *ErrCodecUnavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("erro deve ser *ErrCodecUnavailable, veio %T", err)
	}
}

// TestCodecSelectorWebpComportamento verifica o webp no build ATUAL:
// com cgo+libwebp ele resolve; sem isso, reporta erro explícito. Os dois
// resultados são válidos — o que NÃO pode é mentir.
func TestCodecSelectorWebpComportamento(t *testing.T) {
	cs := NewCodecSelector()
	enc, err := cs.SelectChecked("webp")
	if enc == nil {
		t.Fatal("nunca pode devolver encoder nil")
	}
	if err != nil {
		// Build sem libwebp: exige erro tipado E fallback jpeg.
		var unavail *ErrCodecUnavailable
		if !errors.As(err, &unavail) {
			t.Fatalf("erro deve ser *ErrCodecUnavailable, veio %T", err)
		}
		if enc.Name() != "jpeg" {
			t.Fatalf("fallback de webp deve ser jpeg, veio %s", enc.Name())
		}
		return
	}
	// Build com libwebp: resolve de verdade.
	if enc.Name() != "webp" {
		t.Fatalf("sem erro, o encoder deve ser webp, veio %s", enc.Name())
	}
}

// TestCodecSelectorSelectNuncaNil garante o contrato legado de Select.
func TestCodecSelectorSelectNuncaNil(t *testing.T) {
	cs := NewCodecSelector()
	for _, c := range []string{"", "auto", "jpeg", "webp", "h264", "lixo"} {
		if enc := cs.Select("high", c); enc == nil {
			t.Fatalf("Select(%q) devolveu nil — o pipeline não pode ficar sem encoder", c)
		}
	}
}
