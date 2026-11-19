package screen

import (
	"testing"

	"discovery/app/core/winui"
)

// BenchmarkPatchFrameBannerSemMascara mede o custo do patcher quando NÃO há
// cartão na tela — o caso da esmagadora maioria dos frames (só existe cartão
// durante uma sessão de suporte com o aviso visível). A versão anterior pagava
// make+append+lock por frame mesmo aqui.
func BenchmarkPatchFrameBannerSemMascara(b *testing.B) {
	limparMascarasBench(b)
	frame := benchFrameBGRA(1920, 1080, 31)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PatchFrameBanner(frame)
	}
}

// BenchmarkPatchFrameBannerComMascara mede o caminho com UMA máscara ativa
// (cartão do banner: ~620x68), que é o formato real do cartão do agente.
func BenchmarkPatchFrameBannerComMascara(b *testing.B) {
	limparMascarasBench(b)
	m := &mascaraRegiao{
		hwnd:  0xBEEF,
		rect:  winui.Rect{X: 100, Y: 100, W: 620, H: 68},
		px:    make([]byte, 620*68*4),
		ativa: true,
	}
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()
	mascaraVisivel = func(uintptr) bool { return true }

	frame := benchFrameBGRA(1920, 1080, 31)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PatchFrameBanner(frame)
	}
}

// limparMascarasBench é o equivalente de limparMascaras para benchmarks (que
// não têm *testing.T).
func limparMascarasBench(b *testing.B) {
	b.Helper()
	mascarasMu.Lock()
	mascaras = map[uintptr]*mascaraRegiao{}
	mascarasMu.Unlock()
	b.Cleanup(func() {
		mascarasMu.Lock()
		mascaras = map[uintptr]*mascaraRegiao{}
		mascarasMu.Unlock()
		mascaraVisivel = mascaraVisivelOriginal
	})
}
