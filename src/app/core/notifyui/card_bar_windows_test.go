//go:build windows

package notifyui

import (
	"syscall"
	"testing"
	"time"

	"discovery/app/core/winui"
)

// ── Regressão: repinte incremental da barra da contagem ──
//
// A animação reaproveita o back buffer do cartão e repinta só a faixa da barra.
// O antialiasing do GDI+ NÃO é idempotente: redesenhar a cápsula sobre os
// pixels do quadro anterior acumula cobertura e, em ~20 quadros, a borda
// sub-pixel vira borda dura (e como o relógio força um repinte completo a cada
// segundo, ela "piscaria" a 1 Hz — exatamente o serrilhado que a mudança
// queria eliminar). O repinte incremental restaura a faixa a partir do barBase
// antes de desenhar; este teste exige pixels idênticos entre o 1º e o 20º
// quadro com o MESMO ratio.

var (
	barTestUser32   = syscall.NewLazyDLL("user32.dll")
	barTestGdi32    = syscall.NewLazyDLL("gdi32.dll")
	barTestKernel32 = syscall.NewLazyDLL("kernel32.dll")

	barTestGetGuiResources = barTestUser32.NewProc("GetGuiResources")

	barTestGetDC           = barTestUser32.NewProc("GetDC")
	barTestReleaseDC       = barTestUser32.NewProc("ReleaseDC")
	barTestCreateCompatDC  = barTestGdi32.NewProc("CreateCompatibleDC")
	barTestCreateCompatBmp = barTestGdi32.NewProc("CreateCompatibleBitmap")
	barTestSelectObject    = barTestGdi32.NewProc("SelectObject")
	barTestDeleteObject    = barTestGdi32.NewProc("DeleteObject")
	barTestDeleteDC        = barTestGdi32.NewProc("DeleteDC")
	barTestGetPixel        = barTestGdi32.NewProc("GetPixel")
)

// barTestDC cria um DC de memória compatível com a tela (mesmo padrão dos
// testes de winui, mas local para não depender de outro pacote).
func barTestDC(t *testing.T, width, height int) uintptr {
	t.Helper()
	screenDC, _, _ := barTestGetDC.Call(0)
	if screenDC == 0 {
		t.Skip("GetDC falhou neste ambiente")
	}
	memDC, _, _ := barTestCreateCompatDC.Call(screenDC)
	if memDC == 0 {
		barTestReleaseDC.Call(0, screenDC)
		t.Skip("CreateCompatibleDC falhou neste ambiente")
	}
	bitmap, _, _ := barTestCreateCompatBmp.Call(screenDC, uintptr(width), uintptr(height))
	if bitmap == 0 {
		barTestDeleteDC.Call(memDC)
		barTestReleaseDC.Call(0, screenDC)
		t.Skip("CreateCompatibleBitmap falhou neste ambiente")
	}
	old, _, _ := barTestSelectObject.Call(memDC, bitmap)
	t.Cleanup(func() {
		barTestSelectObject.Call(memDC, old)
		barTestDeleteObject.Call(bitmap)
		barTestDeleteDC.Call(memDC)
		barTestReleaseDC.Call(0, screenDC)
	})
	return memDC
}

// barTestPixels lê a área e devolve as cores RGB (COLORREF sem o byte alto).
func barTestPixels(t *testing.T, dc uintptr, area winui.Rect) []uint32 {
	t.Helper()
	pixels := make([]uint32, 0, area.W*area.H)
	for y := area.Y; y < area.Bottom(); y++ {
		for x := area.X; x < area.Right(); x++ {
			value, _, _ := barTestGetPixel.Call(dc, uintptr(x), uintptr(y))
			pixels = append(pixels, uint32(value)&0x00FFFFFF)
		}
	}
	return pixels
}

func barTestDiffCount(a, b []uint32) int {
	if len(a) != len(b) {
		return len(a) + len(b)
	}
	diff := 0
	for i := range a {
		if a[i] != b[i] {
			diff++
		}
	}
	return diff
}

// newBarTestState monta o estado mínimo para desenhar a barra (só layout/tema/
// relógio são usados por renderCountdownTrack/renderCountdownFill).
func newBarTestState(scale float64, ratio float64) (*cardState, winui.Rect) {
	layout := cardLayoutFor(scale, 0, true, 2, 0)
	state := &cardState{
		req:     CardRequest{CountdownSeconds: 60},
		total:   60 * time.Second,
		layout:  layout,
		scale:   scale,
		theme:   winui.DarkTheme(),
		hover:   -1,
		pressed: -1,
	}
	// Relógio congelado: o desenho tem que ser determinístico no teste.
	frozen := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return frozen }
	state.deadline = frozen.Add(time.Duration(ratio * float64(state.total)))
	return state, state.countdownArea()
}

// TestCardCountdownFramesAreIdempotent é a regressão do acúmulo de AA.
func TestCardCountdownFramesAreIdempotent(t *testing.T) {
	state, area := newBarTestState(1, 0.5)
	width := winui.ScalePx(cardWidth, state.scale)
	height := cardHeightFor(state.layout, state.scale)

	screenDC := barTestDC(t, width, height)

	var work, base winui.BackBuffer
	if !work.Ensure(screenDC, width, height) || !base.Ensure(work.DC(), width, height) {
		t.Skip("back buffers indisponíveis neste ambiente")
	}
	defer work.Release()
	defer base.Release()

	// barBase: fundo liso do cartão na faixa + trilho sem preenchimento
	// (mesma preparação do primeBarBase da produção).
	winui.FillRect(base.DC(), area, state.theme.Card)
	state.renderCountdownTrack(base.DC())

	frame := func() []uint32 {
		base.BlitTo(work.DC(), area)
		state.renderCountdownFill(work.DC())
		return barTestPixels(t, work.DC(), area)
	}

	first := frame()
	accent := 0
	for _, px := range first {
		if winui.Color(px).B() > winui.Color(state.theme.Card).B()+20 {
			accent++
		}
	}
	if accent == 0 {
		t.Fatal("nenhum pixel de accent na faixa: a barra não foi desenhada")
	}

	for i := 2; i <= 20; i++ {
		got := frame()
		if diff := barTestDiffCount(got, first); diff != 0 {
			t.Fatalf("quadro %d difere do 1º em %d pixels da faixa: o repinte incremental está acumulando antialiasing", i, diff)
		}
	}
}

// TestCardCountdownFrameDriftIsDetectable garante que o teste acima tem dentes:
// desenhar a cápsula DUAS vezes sem restaurar o fundo realmente altera pixels
// (é o defeito que o barBase corrige).
func TestCardCountdownFrameDriftIsDetectable(t *testing.T) {
	state, area := newBarTestState(1, 0.5)
	width := winui.ScalePx(cardWidth, state.scale)
	height := cardHeightFor(state.layout, state.scale)

	dc := barTestDC(t, width, height)
	winui.FillRect(dc, winui.Rect{W: width, H: height}, state.theme.Card)
	state.renderCountdownTrack(dc)
	state.renderCountdownFill(dc)
	once := barTestPixels(t, dc, area)

	// Mesmo ratio, mas sem restaurar o fundo: o AA acumula.
	for i := 0; i < 20; i++ {
		state.renderCountdownFill(dc)
	}
	twice := barTestPixels(t, dc, area)

	if barTestDiffCount(once, twice) == 0 {
		t.Fatal("redesenhar o preenchimento sem restaurar o fundo NÃO mudou pixels — o teste de idempotência perdeu o sentido (AA pode ter deixado de ser acumulativo)")
	}
}

// newCardTestState monta um cartão completo (fontes, tema, layout) com o
// relógio congelado no ratio pedido. Libera fontes e buffers no cleanup.
func newCardTestState(t *testing.T, scale, ratio float64) *cardState {
	t.Helper()
	req := NormalizeCardRequest(CardRequest{
		Title:            "Instalando ThunderBird",
		Subtitle:         "Instalação do Aplicativo",
		Message:          "O aplicativo ThunderBird esta sendo instalado na sua estacao de trabalho. Suas janelas abertas serao fechadas ao final.",
		Buttons:          []Button{{ID: "defer", Label: "Adiar"}, {ID: "install", Label: "Instalar", Kind: "primary"}},
		DefaultButtonID:  "install",
		TimeoutButtonID:  "install",
		CloseButtonID:    "defer",
		CountdownSeconds: 60,
		CountdownLabel:   "A instalacao continuara automaticamente em:",
		Hint:             "Enter confirma · Esc adia",
		Icon:             "info",
	})
	layout := cardLayoutFor(scale, 0, true, len(req.Buttons), len(req.ProcessList))
	state := &cardState{
		req:     req,
		total:   time.Duration(req.CountdownSeconds) * time.Second,
		layout:  layout,
		scale:   scale,
		theme:   winui.DarkTheme(),
		hover:   -1,
		pressed: -1,
		now:     func() time.Time { return frozenCardTime() },
	}
	state.deadline = frozenCardTime().Add(time.Duration(ratio * float64(state.total)))
	state.fonts = cardFonts{
		title:           winui.CreateFont(winui.ScalePx(22, scale), 600),
		subtitle:        winui.CreateFont(winui.ScalePx(14, scale), 400),
		body:            winui.CreateFont(winui.ScalePx(15, scale), 400),
		countLabel:      winui.CreateFont(winui.ScalePx(12, scale), 400),
		countValue:      winui.CreateFont(winui.ScalePx(26, scale), 600),
		button:          winui.CreateFont(winui.ScalePx(14, scale), 600),
		hint:            winui.CreateFont(winui.ScalePx(11, scale), 400),
		processFallback: winui.CreateFont(winui.ScalePx(14, scale), 600),
	}
	state.focus = state.defaultIndex()
	state.lastClock = state.clockText()
	t.Cleanup(func() {
		state.fonts.release()
		state.buffer.Release()
		state.barBase.Release()
	})
	return state
}

// frozenCardTime é o instante usado pelos testes de desenho.
func frozenCardTime() time.Time {
	return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
}

// TestCardPaintIncrementalMatchesFullRepaint é o teste de contrato do repinte
// incremental (o caminho que roda a 60 fps): repintar SÓ a faixa da barra tem
// que produzir os mesmos pixels de um repinte completo no mesmo instante — e
// não pode alterar nada fora da faixa.
func TestCardPaintIncrementalMatchesFullRepaint(t *testing.T) {
	state := newCardTestState(t, 1, 0.5)
	width := winui.ScalePx(cardWidth, state.scale)
	height := state.cardHeight()
	full := winui.Rect{W: width, H: height}
	area := state.countdownArea()
	if area.Empty() {
		t.Fatal("área da barra vazia")
	}

	screen := barTestDC(t, width, height)
	// 1º repinte completo (semeia work buffer + barBase) em ratio 0,5.
	state.paint(screen, winui.Rect{})
	before := barTestPixels(t, screen, full)

	// Avança a contagem e pede APENAS a faixa (é o que o WM_TIMER faz entre
	// trocas de segundo; lastClock atualizado = sem repinte completo).
	state.deadline = frozenCardTime().Add(20 * time.Second) // ratio 1/3
	state.lastClock = state.clockText()
	state.paint(screen, area)
	after := barTestPixels(t, screen, full)

	// Referência independente: repinte COMPLETO no mesmo instante.
	reference := barTestDC(t, width, height)
	state.paint(reference, winui.Rect{})
	want := barTestPixels(t, reference, full)

	// (1) A faixa tem que ficar idêntica ao repinte completo.
	stripDirty := 0
	for y := area.Y; y < area.Bottom(); y++ {
		for x := area.X; x < area.Right(); x++ {
			if after[y*width+x] != want[y*width+x] {
				stripDirty++
			}
		}
	}
	if stripDirty != 0 {
		t.Fatalf("repinte incremental difere do completo em %d pixels da faixa", stripDirty)
	}

	// (2) Nada fora da faixa pode ter mudado.
	outsideDirty := 0
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if x >= area.X && x < area.Right() && y >= area.Y && y < area.Bottom() {
				continue
			}
			if after[y*width+x] != before[y*width+x] {
				outsideDirty++
			}
		}
	}
	if outsideDirty != 0 {
		t.Fatalf("repinte incremental alterou %d pixels fora da faixa da barra", outsideDirty)
	}
}

// TestCardAnimationDoesNotLeakGDI garante que o caminho por quadro (60 fps)
// não aloca recursos de kernel por frame: cada volta cria apenas objetos GDI+
// liberados por defer.
func TestCardAnimationDoesNotLeakGDI(t *testing.T) {
	if err := barTestGetGuiResources.Find(); err != nil {
		t.Skip("GetGuiResources indisponível")
	}
	state := newCardTestState(t, 1, 0.5)
	width := winui.ScalePx(cardWidth, state.scale)
	height := state.cardHeight()
	screen := barTestDC(t, width, height)
	area := state.countdownArea()
	state.paint(screen, winui.Rect{})

	process, _, _ := barTestKernel32.NewProc("GetCurrentProcess").Call()
	count := func() int64 {
		value, _, _ := barTestGetGuiResources.Call(process, 0) // GR_GDIOBJECTS
		return int64(value)
	}
	before := count()
	for i := 0; i < 300; i++ {
		state.paint(screen, area)
	}
	after := count()
	if after > before+5 {
		t.Fatalf("objetos GDI cresceram de %d para %d em 300 quadros", before, after)
	}
}
