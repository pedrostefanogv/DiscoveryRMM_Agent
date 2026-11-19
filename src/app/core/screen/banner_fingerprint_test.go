package screen

import (
	"testing"

	"discovery/app/core/winui"
)

// ── Regressão: o cache do patcher NUNCA pode servir snapshot obsoleto ──
//
// O patcher reescreve a área do cartão do suporte em todo frame. Se ele servir
// um snapshot obsoleto, o cartão do operador (dado sensível: nome de quem
// atende) VAZA para o stream. É o pior modo de falha deste arquivo, então a
// invalidação é testada pelos DOIS lados que o fingerprint cobre.

// lado 1: troca de buffer detectada mesmo se o chamador esquecer de versionar
// (o endereço muda).
func TestFingerprintPegaTrocaDeBufferSemVersao(t *testing.T) {
	limparMascaras(t)

	m := mascaraDeTeste(0xBEEF, winui.Rect{X: 0, Y: 0, W: 64, H: 64}, 0x11, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	fp1 := fingerprintDoMapaMascaras()
	mascarasMu.Unlock()

	// Troca o buffer SEM tocar em versao (simula um caminho que esqueceu).
	novo := make([]byte, len(m.px))
	for i := range novo {
		novo[i] = 0x22
	}
	mascarasMu.Lock()
	m.px = novo
	fp2 := fingerprintDoMapaMascaras()
	mascarasMu.Unlock()

	if fp1 == fp2 {
		t.Fatalf("fingerprint nao mudou na troca de buffer (fp=%#x): o endereco deveria pegar", fp1)
	}
}

// lado 2: troca detectada mesmo se o ALOCADOR reusar o endereço (a versão muda).
func TestFingerprintPegaReusoDeEndereco(t *testing.T) {
	limparMascaras(t)

	m := mascaraDeTeste(0xBEEF, winui.Rect{X: 0, Y: 0, W: 64, H: 64}, 0x11, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	fp1 := fingerprintDoMapaMascaras()
	mascarasMu.Unlock()

	// Mesmo ENDERECO (reuso explicito do buffer) mas versao nova: é o caso que
	// o endereco sozinho NAO pegaria.
	mascarasMu.Lock()
	m.versao++
	fp2 := fingerprintDoMapaMascaras()
	mascarasMu.Unlock()

	if fp1 == fp2 {
		t.Fatalf("fingerprint nao mudou com versao nova (fp=%#x): o alocador teria reusado o endereco", fp1)
	}
}

// Propriedade negativa: sem NENHUMA mudanca, o fingerprint é estável — senão o
// cache nunca acerta e a otimização seria inútil.
func TestFingerprintEstavelSemMudanca(t *testing.T) {
	limparMascaras(t)

	m := mascaraDeTeste(0xBEEF, winui.Rect{X: 0, Y: 0, W: 64, H: 64}, 0x11, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	fp1 := fingerprintDoMapaMascaras()
	fp2 := fingerprintDoMapaMascaras()
	mascarasMu.Unlock()

	if fp1 != fp2 {
		t.Fatalf("fingerprint instavel sem mudanca (%#x != %#x): o cache nunca acertaria", fp1, fp2)
	}
}

// A flag ativa e o retangulo tambem precisam invalidar: sao campos que o
// patcher usa para decidir SE e ONDE reescrever.
func TestFingerprintPegaMudancaDeFlagsERetangulo(t *testing.T) {
	limparMascaras(t)

	m := mascaraDeTeste(0xBEEF, winui.Rect{X: 0, Y: 0, W: 64, H: 64}, 0x11, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	base := fingerprintDoMapaMascaras()

	m.ativa = false
	desativada := fingerprintDoMapaMascaras()
	m.ativa = true

	m.rect = winui.Rect{X: 10, Y: 10, W: 64, H: 64}
	movida := fingerprintDoMapaMascaras()
	mascarasMu.Unlock()

	if base == desativada {
		t.Error("fingerprint ignorou a flag ativa")
	}
	if base == movida {
		t.Error("fingerprint ignorou mudanca de retangulo")
	}
}
