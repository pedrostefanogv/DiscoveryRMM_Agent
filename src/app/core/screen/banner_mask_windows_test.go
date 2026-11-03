//go:build windows

package screen

import (
	"testing"

	"discovery/app/core/winui"
)

// ── Máscara de região (banner/aviso fora do stream em capturers DXGI) ──
//
// Regressão de 2026-11-03: com capturer DXGI o cartão do banner aparecia como
// um RETÂNGULO PRETO no stream (WDA_MONITOR). A máscara reescreve a área com os
// pixels lidos antes de o cartão aparecer — estes testes fixam o contrato:
// só máscara ATIVA é aplicada, o recorte respeita a origem do frame e frame HDR
// não é tocado.

// limparMascaras zera o registry global entre testes e faz todo HWND sintético
// contar como visível (os testes não criam janela real).
func limparMascaras(t *testing.T) {
	t.Helper()
	anterior := mascaraVisivel
	mascaraVisivel = func(uintptr) bool { return true }
	mascarasMu.Lock()
	mascaras = map[uintptr]*mascaraRegiao{}
	mascarasMu.Unlock()
	t.Cleanup(func() {
		mascaraVisivel = anterior
		mascarasMu.Lock()
		mascaras = map[uintptr]*mascaraRegiao{}
		mascarasMu.Unlock()
	})
}

// mascaraDeTeste monta uma máscara com todos os pixels iguais a valor.
func mascaraDeTeste(hwnd uintptr, rect winui.Rect, valor byte, ativa bool) *mascaraRegiao {
	px := make([]byte, rect.W*rect.H*4)
	for i := range px {
		px[i] = valor
	}
	return &mascaraRegiao{hwnd: hwnd, rect: rect, px: px, ativa: ativa}
}

// frameDeTeste monta um frame BGRA preenchido com valor.
func frameDeTeste(w, h, originX, originY int, valor byte) *Frame {
	data := make([]byte, w*h*4)
	for i := range data {
		data[i] = valor
	}
	return &Frame{Data: data, Width: w, Height: h, Stride: w * 4, OriginX: originX, OriginY: originY}
}

// pixel devolve o byte B do pixel (x,y) do frame.
func pixel(f *Frame, x, y int) byte {
	return f.Data[y*f.Stride+x*4]
}

func TestPatchFrameBannerAplicaMascaraAtiva(t *testing.T) {
	limparMascaras(t)
	frame := frameDeTeste(100, 50, 0, 0, 0x11)
	m := mascaraDeTeste(0xABC, winui.Rect{X: 10, Y: 5, W: 20, H: 10}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	PatchFrameBanner(frame)

	if got := pixel(frame, 10, 5); got != 0x22 {
		t.Fatalf("canto superior esquerdo da máscara = %#x, esperado 0x22", got)
	}
	if got := pixel(frame, 29, 14); got != 0x22 {
		t.Fatalf("canto inferior direito da máscara = %#x, esperado 0x22", got)
	}
	if got := pixel(frame, 9, 5); got != 0x11 {
		t.Fatalf("pixel à esquerda da máscara = %#x, esperado 0x11 (fora da área)", got)
	}
	if got := pixel(frame, 30, 5); got != 0x11 {
		t.Fatalf("pixel à direita da máscara = %#x, esperado 0x11 (fora da área)", got)
	}
	if got := pixel(frame, 10, 15); got != 0x11 {
		t.Fatalf("pixel abaixo da máscara = %#x, esperado 0x11 (fora da área)", got)
	}
}

func TestPatchFrameBannerIgnoraMascaraInativa(t *testing.T) {
	limparMascaras(t)
	frame := frameDeTeste(100, 50, 0, 0, 0x11)
	m := mascaraDeTeste(0xABC, winui.Rect{X: 10, Y: 5, W: 20, H: 10}, 0x22, false)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	PatchFrameBanner(frame)

	if got := pixel(frame, 10, 5); got != 0x11 {
		t.Fatalf("máscara inativa não pode alterar o frame (pixel = %#x)", got)
	}
}

func TestPatchFrameBannerRespeitaOrigemDoFrame(t *testing.T) {
	limparMascaras(t)
	// Frame do monitor secundário: origem (100,50) no desktop virtual.
	frame := frameDeTeste(100, 50, 100, 50, 0x11)
	m := mascaraDeTeste(0xABC, winui.Rect{X: 110, Y: 55, W: 20, H: 10}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	PatchFrameBanner(frame)

	if got := pixel(frame, 10, 5); got != 0x22 {
		t.Fatalf("máscara deveria cair em (10,5) do frame, pixel = %#x", got)
	}
	if got := pixel(frame, 0, 0); got != 0x11 {
		t.Fatalf("origem do frame não pode ser pintada, pixel = %#x", got)
	}
}

func TestPatchFrameBannerRecortaRegiaoParcial(t *testing.T) {
	limparMascaras(t)
	frame := frameDeTeste(40, 30, 0, 0, 0x11)
	// Máscara começa fora do frame (x negativo) e termina dentro.
	m := mascaraDeTeste(0xABC, winui.Rect{X: -10, Y: -5, W: 20, H: 15}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	PatchFrameBanner(frame) // não pode entrar em pânico nem escrever fora

	if got := pixel(frame, 0, 0); got != 0x22 {
		t.Fatalf("interseção deveria ser pintada, pixel = %#x", got)
	}
	if got := pixel(frame, 10, 10); got != 0x11 {
		t.Fatalf("fora da interseção não pode ser pintado, pixel = %#x", got)
	}
}

func TestPatchFrameBannerIgnoraFrameHDR(t *testing.T) {
	limparMascaras(t)
	frame := frameDeTeste(100, 50, 0, 0, 0x11)
	frame.ColorSpace = 1 // scRGB float: 8 bytes/px, fora do contrato BGRA
	m := mascaraDeTeste(0xABC, winui.Rect{X: 10, Y: 5, W: 20, H: 10}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	PatchFrameBanner(frame)

	if got := pixel(frame, 10, 5); got != 0x11 {
		t.Fatalf("frame HDR não pode ser mascarado com pixels BGRA (pixel = %#x)", got)
	}
}

// capturerComEmenda simula o GDI: contorna a região por conta própria.
type capturerComEmenda struct{}

func (capturerComEmenda) AcquireNextFrame() (*Frame, error) { return nil, nil }
func (capturerComEmenda) ReleaseFrame()                     {}
func (capturerComEmenda) Close() error                      { return nil }
func (capturerComEmenda) Name() string                      { return "fake-emenda" }
func (capturerComEmenda) Geometry() (int, int, int, int)    { return 0, 0, 0, 0 }
func (capturerComEmenda) SupportsBannerPatch() bool         { return true }

// capturerSemEmenda simula o DXGI: entrega o frame pronto, sem emenda.
type capturerSemEmenda struct{}

func (capturerSemEmenda) AcquireNextFrame() (*Frame, error) { return nil, nil }
func (capturerSemEmenda) ReleaseFrame()                     {}
func (capturerSemEmenda) Close() error                      { return nil }
func (capturerSemEmenda) Name() string                      { return "fake-dxgi" }
func (capturerSemEmenda) Geometry() (int, int, int, int)    { return 0, 0, 0, 0 }

func TestPatchFrameBannerIfNeededPulaCapturerComEmenda(t *testing.T) {
	limparMascaras(t)
	m := mascaraDeTeste(0xABC, winui.Rect{X: 10, Y: 5, W: 20, H: 10}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	comEmenda := frameDeTeste(100, 50, 0, 0, 0x11)
	PatchFrameBannerIfNeeded(capturerComEmenda{}, comEmenda)
	if got := pixel(comEmenda, 10, 5); got != 0x11 {
		t.Fatalf("capturer com emenda própria não deve receber a máscara (pixel = %#x)", got)
	}

	semEmenda := frameDeTeste(100, 50, 0, 0, 0x11)
	PatchFrameBannerIfNeeded(capturerSemEmenda{}, semEmenda)
	if got := pixel(semEmenda, 10, 5); got != 0x22 {
		t.Fatalf("capturer sem emenda deve receber a máscara (pixel = %#x)", got)
	}
}

func TestPatchFrameBannerIgnoraCartaoOculto(t *testing.T) {
	limparMascaras(t)
	mascaraVisivel = func(uintptr) bool { return false }
	frame := frameDeTeste(100, 50, 0, 0, 0x11)
	m := mascaraDeTeste(0xABC, winui.Rect{X: 10, Y: 5, W: 20, H: 10}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	PatchFrameBanner(frame)

	if got := pixel(frame, 10, 5); got != 0x11 {
		t.Fatalf("cartão oculto não pode mascarar o frame (pixel = %#x)", got)
	}
}

func TestPatchFrameBannerStrideComPadding(t *testing.T) {
	limparMascaras(t)
	// Frame com padding entre linhas (stride > width*4): o patcher precisa
	// respeitar o stride e não escrever no padding.
	const w, h, stride = 20, 10, 20*4 + 16
	data := make([]byte, stride*h)
	for i := range data {
		data[i] = 0x11
	}
	frame := &Frame{Data: data, Width: w, Height: h, Stride: stride}
	m := mascaraDeTeste(0xABC, winui.Rect{X: 2, Y: 1, W: 4, H: 3}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	PatchFrameBanner(frame)

	if got := frame.Data[1*stride+2*4]; got != 0x22 {
		t.Fatalf("pixel mascarado = %#x, esperado 0x22", got)
	}
	if got := frame.Data[1*stride+6*4]; got != 0x11 {
		t.Fatalf("pixel fora da máscara = %#x, esperado 0x11", got)
	}
	// Padding da primeira linha não pode ser tocado.
	if got := frame.Data[w*4]; got != 0x11 {
		t.Fatalf("padding da linha 0 foi alterado (%#x)", got)
	}
}

func TestPatchFrameBannerAtUsaOrigemExplicita(t *testing.T) {
	limparMascaras(t)
	// Cenário HDR: o tone mapping recria o Frame e zera OriginX/OriginY; a
	// origem física vem por parâmetro.
	frame := frameDeTeste(100, 50, 0, 0, 0x11)
	m := mascaraDeTeste(0xABC, winui.Rect{X: 110, Y: 55, W: 20, H: 10}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	PatchFrameBannerAt(frame, 100, 50)

	if got := pixel(frame, 10, 5); got != 0x22 {
		t.Fatalf("máscara com origem explícita deveria cair em (10,5), pixel = %#x", got)
	}
	if got := pixel(frame, 0, 0); got != 0x11 {
		t.Fatalf("origem do frame não pode ser pintada, pixel = %#x", got)
	}
}

func TestMascaraDisponivelPorJanela(t *testing.T) {
	limparMascaras(t)
	m := mascaraDeTeste(0xABC, winui.Rect{X: 0, Y: 0, W: 4, H: 4}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	if !mascaraDisponivel(0xABC) {
		t.Fatal("mascaraDisponivel deveria ser true para a janela com máscara")
	}
	if mascaraDisponivel(0xDEF) {
		t.Fatal("mascaraDisponivel não pode vazar para outra janela (máscara do aviso não cobre o banner)")
	}
	if !RegionMaskAvailable() {
		t.Fatal("RegionMaskAvailable continua sendo 'existe alguma máscara'")
	}
}

// TestPatchFrameBannerConcorrenteComRefresh garante que a troca do buffer de
// pixels pelo refresh (copy-on-write) não corre com a leitura do patcher.
// Rode com -race para valer.
func TestPatchFrameBannerConcorrenteComRefresh(t *testing.T) {
	limparMascaras(t)
	m := mascaraDeTeste(0xABC, winui.Rect{X: 0, Y: 0, W: 8, H: 8}, 0x22, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	parar := make(chan struct{})
	feito := make(chan struct{})
	go func() {
		defer close(feito)
		for {
			select {
			case <-parar:
				return
			default:
			}
			// Simula o refresh: buffer novo + troca do slice sob lock.
			mascarasMu.Lock()
			if atual := mascaras[m.hwnd]; atual != nil {
				novo := make([]byte, len(atual.px))
				copy(novo, atual.px)
				atual.px = novo
			}
			mascarasMu.Unlock()
		}
	}()

	frame := frameDeTeste(32, 32, 0, 0, 0x11)
	for i := 0; i < 200; i++ {
		PatchFrameBanner(frame)
	}
	close(parar)
	<-feito
}

func TestMascaraCicloDeVida(t *testing.T) {
	limparMascaras(t)
	if RegionMaskAvailable() {
		t.Fatal("registry deveria começar vazio")
	}
	m := mascaraDeTeste(0xABC, winui.Rect{X: 0, Y: 0, W: 4, H: 4}, 0x22, false)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()
	// Capturada mas INATIVA (modo WDA_EXCLUDEFROMCAPTURE) não conta como emenda
	// de região disponível — o compositor já remove o cartão nesse modo.
	if RegionMaskAvailable() {
		t.Fatal("máscara inativa não deveria contar como emenda de região")
	}
	if !mascaraDisponivel(m.hwnd) {
		t.Fatal("a máscara ESTÁ capturada para esta janela (mascaraDisponivel)")
	}
	ativarMascara(m.hwnd)
	mascarasMu.Lock()
	ativa := mascaras[m.hwnd].ativa
	mascarasMu.Unlock()
	if !ativa {
		t.Fatal("ativarMascara deveria marcar a máscara como ativa")
	}
	if !RegionMaskAvailable() {
		t.Fatal("máscara ativa deveria habilitar a emenda de região")
	}
	removerMascara(m.hwnd)
	if RegionMaskAvailable() {
		t.Fatal("removerMascara deveria limpar o registry")
	}
}
