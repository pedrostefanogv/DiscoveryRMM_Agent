//go:build windows

package screen

import (
	"testing"

	"discovery/app/core/winui"
)

// ── Escada de afinidade (regressão do retângulo preto) ──
//
// O bug de 2026-11-03 nasceu aqui: um WDA_EXCLUDEFROMCAPTURE bem-sucedido era
// rebaixado para WDA_MONITOR (preto em toda captura) porque a escada continuava
// depois do sucesso. Estes testes fixam a ordem e o curto-circuito sem precisar
// de janela real (affinitySet/affinityGet são injetáveis).

// injetarAfinidade troca os procs por um fake que registra as chamadas e
// responde conforme aceita.
func injetarAfinidade(t *testing.T, aceita func(value uint32) bool) *[]uint32 {
	t.Helper()
	anteriorSet, anteriorGet := affinitySet, affinityGet
	chamadas := &[]uint32{}
	affinitySet = func(_ uintptr, value uint32) (bool, error) {
		*chamadas = append(*chamadas, value)
		if aceita(value) {
			return true, nil
		}
		return false, errTeste
	}
	affinityGet = func(uintptr) (uint32, bool) { return 0, true }
	t.Cleanup(func() {
		affinitySet, affinityGet = anteriorSet, anteriorGet
	})
	return chamadas
}

func TestApplyDisplayAffinityNaoRebaixaExclude(t *testing.T) {
	chamadas := injetarAfinidade(t, func(value uint32) bool { return value == wdaExcludeFromCapture })

	mode, err := applyDisplayAffinity(0x1234)
	if err != nil {
		t.Fatalf("EXCLUDE aceito não deveria devolver erro: %v", err)
	}
	if mode != bannerCaptureExcluded {
		t.Fatalf("modo = %d, esperado bannerCaptureExcluded", mode)
	}
	if len(*chamadas) != 1 || (*chamadas)[0] != wdaExcludeFromCapture {
		t.Fatalf("chamadas = %v, esperado apenas [0x11] (nada de WDA_NONE/MONITOR depois do sucesso)", *chamadas)
	}
}

func TestApplyDisplayAffinityCaiParaMonitor(t *testing.T) {
	chamadas := injetarAfinidade(t, func(value uint32) bool { return value == wdaMonitor })

	mode, err := applyDisplayAffinity(0x1234)
	if err != nil {
		t.Fatalf("MONITOR aceito não deveria devolver erro: %v", err)
	}
	if mode != bannerCaptureBlack {
		t.Fatalf("modo = %d, esperado bannerCaptureBlack", mode)
	}
	esperado := []uint32{wdaExcludeFromCapture, wdaNone, wdaMonitor}
	if len(*chamadas) != len(esperado) {
		t.Fatalf("chamadas = %v, esperado %v", *chamadas, esperado)
	}
	for i := range esperado {
		if (*chamadas)[i] != esperado[i] {
			t.Fatalf("chamadas = %v, esperado %v", *chamadas, esperado)
		}
	}
}

func TestApplyDisplayAffinitySemAfinidade(t *testing.T) {
	chamadas := injetarAfinidade(t, func(uint32) bool { return false })

	mode, err := applyDisplayAffinity(0x1234)
	if mode != bannerCaptureVisible {
		t.Fatalf("modo = %d, esperado bannerCaptureVisible", mode)
	}
	if err == nil {
		t.Fatal("sem nenhuma afinidade aceita o erro precisa ser reportado")
	}
	if len(*chamadas) != 3 {
		t.Fatalf("chamadas = %v, esperado 3 tentativas", *chamadas)
	}
}

// TestHideBannerFromCapturePrefereExclude garante a correção da regressão de
// 2026-11-03: o WDA_EXCLUDEFROMCAPTURE tem de ser tentado ANTES da máscara/emenda.
// Quando ele é aceito, nada mais pode ser aplicado (nem máscara nem MONITOR) —
// é o único mecanismo que o compositor garante em qualquer captura.
func TestHideBannerFromCapturePrefereExclude(t *testing.T) {
	limparMascaras(t)
	chamadas := injetarAfinidade(t, func(value uint32) bool { return value == wdaExcludeFromCapture })
	anteriorDecidido := bannerModoDecidido.Load()
	anteriorAtraso := reavaliarAtraso
	reavaliarAtraso = 0
	t.Cleanup(func() {
		bannerModoDecidido.Store(anteriorDecidido)
		reavaliarAtraso = anteriorAtraso
		SetCapturerAtual(nil)
	})

	// Cenário do bug: máscara pronta E capturer GDI (emenda) disponíveis. Mesmo
	// assim o EXCLUDE precisa ser o primeiro caminho.
	m := mascaraDeTeste(0x321, winui.Rect{X: 0, Y: 0, W: 4, H: 4}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()
	SetCapturerAtual(capturerComEmenda{})
	registerBannerCapture(0x321)
	defer unregisterBannerCapture(0x321)

	mode, err := hideBannerFromCapture(0x321)
	if err != nil {
		t.Fatalf("EXCLUDE aceito não deveria devolver erro: %v", err)
	}
	if mode != bannerCaptureExcluded {
		t.Fatalf("modo = %d (%s), esperado bannerCaptureExcluded", mode, bannerCaptureModeName(mode))
	}
	if len(*chamadas) != 1 || (*chamadas)[0] != wdaExcludeFromCapture {
		t.Fatalf("chamadas de afinidade = %v, esperado somente [0x11] (nada de máscara/MONITOR depois do EXCLUDE)", *chamadas)
	}
}

func TestMascaraAtivaPara(t *testing.T) {
	casos := []struct {
		nome  string
		mode  bannerCaptureMode
		err   error
		ativa bool
	}{
		{"regiao com mascara", bannerCaptureRegiao, nil, true},
		{"monitor com mascara", bannerCaptureBlack, nil, true},
		{"sem afinidade com mascara", bannerCaptureVisible, nil, true},
		{"exclude nao mascara", bannerCaptureExcluded, nil, false},
		{"regiao sem mascara", bannerCaptureRegiao, errTeste, false},
	}
	for _, c := range casos {
		if got := mascaraAtivaPara(c.mode, c.err); got != c.ativa {
			t.Fatalf("%s: mascaraAtivaPara = %v, esperado %v", c.nome, got, c.ativa)
		}
	}
}

// errTeste é um erro qualquer para o caso "máscara indisponível".
var errTeste = &erroSimples{}

type erroSimples struct{}

func (*erroSimples) Error() string { return "mascara indisponivel" }

// TestReavaliarBannerNoCapturer garante que a troca de capturer no meio da
// sessão não deixa o cartão vazar visível.
func TestReavaliarBannerNoCapturer(t *testing.T) {
	limparMascaras(t)
	anteriorSet, anteriorGet := affinitySet, affinityGet
	anteriorDecidido := bannerModoDecidido.Load()
	anteriorAtraso := reavaliarAtraso
	// Sem debounce no teste: a reavaliação precisa ser observável na mesma chamada.
	reavaliarAtraso = 0
	t.Cleanup(func() {
		affinitySet, affinityGet = anteriorSet, anteriorGet
		reavaliarAtraso = anteriorAtraso
		bannerModoDecidido.Store(anteriorDecidido)
		SetCapturerAtual(nil)
		unregisterBannerCapture(0x777)
	})

	chamadas := &[]uint32{}
	affinitySet = func(_ uintptr, value uint32) (bool, error) {
		*chamadas = append(*chamadas, value)
		if value == wdaExcludeFromCapture {
			return true, nil
		}
		return false, errTeste
	}
	affinityGet = func(uintptr) (uint32, bool) { return wdaNone, true }

	// Banner ativo, modo já decidido, sem máscara.
	registerBannerCapture(0x777)
	bannerModoDecidido.Store(true)

	// Capturer DXGI (não emenda) → precisa reaplicar a afinidade.
	SetCapturerAtual(capturerSemEmenda{})
	if len(*chamadas) == 0 {
		t.Fatal("troca para capturer sem emenda deveria reaplicar a afinidade")
	}
	if (*chamadas)[0] != wdaExcludeFromCapture {
		t.Fatalf("primeira afinidade reaplicada = %#x, esperado EXCLUDE", (*chamadas)[0])
	}

	// Capturer GDI (emenda própria) → não precisa de afinidade.
	*chamadas = nil
	SetCapturerAtual(capturerComEmenda{})
	if len(*chamadas) != 0 {
		t.Fatalf("capturer com emenda não deveria reaplicar afinidade (chamadas=%v)", *chamadas)
	}

	// Com máscara de região disponível → a máscara cobre qualquer capturer.
	*chamadas = nil
	m := mascaraDeTeste(0x777, winui.Rect{X: 0, Y: 0, W: 4, H: 4}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()
	SetCapturerAtual(capturerSemEmenda{})
	if len(*chamadas) != 0 {
		t.Fatalf("com máscara pronta não deveria reaplicar afinidade (chamadas=%v)", *chamadas)
	}

	// Modo ainda não decidido (banner nascendo) → listener não age.
	*chamadas = nil
	bannerModoDecidido.Store(false)
	SetCapturerAtual(capturerSemEmenda{})
	if len(*chamadas) != 0 {
		t.Fatalf("antes da decisão do banner o listener não pode agir (chamadas=%v)", *chamadas)
	}
}
