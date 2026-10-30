//go:build windows

package notifyui

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"discovery/app/core/winui"
	"golang.org/x/sys/windows"
)

// ── Harness de pré-visualização do cartão (DESATIVADO por padrão) ──
//
// Este arquivo NÃO roda no "go test" normal: todos os testes pulam a menos que
// DISCOVERY_UI_PREVIEW=1 esteja definido. Com a variável, o teste renderiza o
// cartão real (cardState.paint) em um DIB 32bpp de memória e grava PNGs em
// _scratch/ui/card-preview/{before,after}/ para comparação visual
// antes/depois da reforma da barra de contagem.
//
// Uso (PowerShell, a partir de Discovery/src):
//
//	$env:DISCOVERY_UI_PREVIEW="1"
//	go test ./app/core/notifyui -run Preview -v
//
// A fase (nome do subdiretório) é escolhida com DISCOVERY_UI_PREVIEW_PHASE
// (default "before"). Nenhum arquivo de produto é tocado aqui.

// previewRepoRoot localiza a raiz do projeto (RMM_Discovery) subindo a partir
// do diretório de trabalho do teste, para que os PNGs caiam sempre em
// <raiz>/_scratch/ui/card-preview independente de onde o go test roda. A
// procura NÃO para no primeiro .git porque há repositórios aninhados.
func previewRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if filepath.Base(dir) == "RMM_Discovery" {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// previewOutDir base é resolvida em tempo de execução (raiz do repo).
func previewBaseDir() string {
	return filepath.Join(previewRepoRoot(), "_scratch", "ui", "card-preview")
}

// syscalls GDI/user32 usadas apenas pelo harness de preview.
var (
	procGetDC              = windows.NewLazySystemDLL("user32.dll").NewProc("GetDC")
	procReleaseDC          = windows.NewLazySystemDLL("user32.dll").NewProc("ReleaseDC")
	procCreateCompatibleDC = windows.NewLazySystemDLL("gdi32.dll").NewProc("CreateCompatibleDC")
	procDeleteDC           = windows.NewLazySystemDLL("gdi32.dll").NewProc("DeleteDC")
	procSelectObject       = windows.NewLazySystemDLL("gdi32.dll").NewProc("SelectObject")
	procDeleteObject       = windows.NewLazySystemDLL("gdi32.dll").NewProc("DeleteObject")
	procCreateDIBSection   = windows.NewLazySystemDLL("gdi32.dll").NewProc("CreateDIBSection")
)

// bitmapinfoHeader espelha BITMAPINFOHEADER (40 bytes).
type bitmapinfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32 // negativo = top-down (linha 0 no topo)
	Planes        uint16
	BitCount      uint16
	Compression   uint32 // BI_RGB = 0
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// previewSurface é um DIB 32bpp top-down selecionado em um DC de memória. Os
// pixels ficam acessíveis direto no slice bits (ordem B,G,R,X na memória). O
// DC da tela de origem é mantido porque CreateCompatibleBitmap (usado pelo
// double buffer de paint) deriva o formato do bitmap selecionado no DC.
type previewSurface struct {
	dc       uintptr
	bitmap   uintptr
	old      uintptr
	screenDC uintptr
	bits     []uint8 // BGRA, width*height*4 bytes
	w, h     int
}

// newPreviewSurface cria um DIB 32bpp do tamanho informado.
func newPreviewSurface(width, height int) (*previewSurface, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("tamanho inválido: %dx%d", width, height)
	}
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC(0) falhou")
	}
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		procReleaseDC.Call(0, screenDC)
		return nil, fmt.Errorf("CreateCompatibleDC falhou")
	}
	bmi := struct {
		hdr bitmapinfoHeader
		_   uint32 // a struct BITMAPINFO pede 1 entrada de paleta após o header
	}{hdr: bitmapinfoHeader{
		Size:     40,
		Width:    int32(width),
		Height:   -int32(height), // top-down
		Planes:   1,
		BitCount: 32,
	}}
	// bits sai como void** do CreateDIBSection; guardamos em unsafe.Pointer
	// direto para evitar conversão uintptr→unsafe.Pointer (o vet reclama).
	var bits unsafe.Pointer
	bitmap, _, _ := procCreateDIBSection.Call(
		memDC, uintptr(unsafe.Pointer(&bmi)), 0 /*DIB_RGB_COLORS*/, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		procDeleteDC.Call(memDC)
		procReleaseDC.Call(0, screenDC)
		return nil, fmt.Errorf("CreateDIBSection falhou")
	}
	old, _, _ := procSelectObject.Call(memDC, bitmap)
	return &previewSurface{
		dc: memDC, bitmap: bitmap, old: old, screenDC: screenDC,
		bits: unsafe.Slice((*uint8)(bits), width*height*4),
		w:    width, h: height,
	}, nil
}

// fill pinta o fundo com uma cor sólida (para evidenciar cantos e bordas).
func (s *previewSurface) fill(c color.RGBA) {
	for i := 0; i+3 < len(s.bits); i += 4 {
		s.bits[i] = c.B
		s.bits[i+1] = c.G
		s.bits[i+2] = c.R
		s.bits[i+3] = 255
	}
}

// close libera bitmap, DC de memória e DC de tela.
func (s *previewSurface) close() {
	procSelectObject.Call(s.dc, s.old)
	procDeleteObject.Call(s.bitmap)
	procDeleteDC.Call(s.dc)
	if s.screenDC != 0 {
		procReleaseDC.Call(0, s.screenDC)
		s.screenDC = 0
	}
}

// pixelAt devolve o pixel (x, y) como RGBA (canal A fixo em 255).
func (s *previewSurface) pixelAt(x, y int) color.RGBA {
	i := (y*s.w + x) * 4
	return color.RGBA{R: s.bits[i+2], G: s.bits[i+1], B: s.bits[i], A: 255}
}

// savePNG grava o conteúdo do DIB como PNG no caminho informado.
func (s *previewSurface) savePNG(path string) error {
	img := image.NewRGBA(image.Rect(0, 0, s.w, s.h))
	for y := 0; y < s.h; y++ {
		for x := 0; x < s.w; x++ {
			img.SetRGBA(x, y, s.pixelAt(x, y))
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// previewRequest monta um CardRequest representativo do print do usuário
// ("Instalando ThunderBird"), já normalizado.
func previewRequest() CardRequest {
	return NormalizeCardRequest(CardRequest{
		Title:    "Instalando ThunderBird",
		Subtitle: "Instalação do Aplicativo",
		Message: "O aplicativo ThunderBird esta sendo instalado na sua estacao de trabalho. " +
			"Suas janelas abertas serao fechadas ao final da instalacao. " +
			"Voce pode adiar esta acao caso esteja ocupado no momento.",
		Buttons: []Button{
			{ID: "defer", Label: "Adiar", Kind: ""},
			{ID: "install", Label: "Instalar", Kind: "primary"},
		},
		DefaultButtonID:  "install",
		TimeoutButtonID:  "install",
		CountdownSeconds: 60,
		CountdownLabel:   "A instalacao continuara automaticamente em:",
		Hint:             "Enter confirma · Esc adia",
		Icon:             "info",
	})
}

// newPreviewState monta um cardState completo (como runCardWindow faria), sem
// criar janela: layout, fontes, tema e contagem configurados à mão.
func newPreviewState(t *testing.T, req CardRequest, themeName string, scale float64) *cardState {
	t.Helper()
	hasCountdown := req.CountdownSeconds > 0

	// Crescimento dinâmico da caixa de mensagem, igual ao runCardWindow.
	bodyFont := winui.CreateFont(winui.ScalePx(15, scale), 400)
	messageTextRect := cardLayoutFor(scale, 0, hasCountdown, len(req.Buttons), len(req.ProcessList)).
		messageText.Inset(winui.ScalePx(14, scale), winui.ScalePx(10, scale))
	extraHeight := 0
	if needed := winui.MeasureWrappedText(bodyFont, req.Message, messageTextRect.W); needed > messageTextRect.H {
		extraHeight = needed - messageTextRect.H
		if maxExtra := winui.ScalePx(cardMessageMaxExtraPx, scale); extraHeight > maxExtra {
			extraHeight = maxExtra
		}
	}

	state := &cardState{
		req:     req,
		total:   time.Duration(req.CountdownSeconds) * time.Second,
		hover:   -1,
		pressed: -1,
		layout:  cardLayoutFor(scale, extraHeight, hasCountdown, len(req.Buttons), len(req.ProcessList)),
		scale:   scale,
	}
	state.fonts = cardFonts{
		title:           winui.CreateFont(winui.ScalePx(22, scale), 600),
		subtitle:        winui.CreateFont(winui.ScalePx(14, scale), 400),
		body:            bodyFont,
		countLabel:      winui.CreateFont(winui.ScalePx(12, scale), 400),
		countValue:      winui.CreateFont(winui.ScalePx(26, scale), 600),
		button:          winui.CreateFont(winui.ScalePx(14, scale), 600),
		hint:            winui.CreateFont(winui.ScalePx(11, scale), 400),
		processFallback: winui.CreateFont(winui.ScalePx(14, scale), 600),
	}
	switch themeName {
	case "light":
		state.theme = winui.LightTheme()
	default:
		state.theme = winui.DarkTheme()
	}
	state.focus = state.defaultIndex()

	return state
}

// previewDir devolve o diretório de saída da fase (before/after).
func previewDir(phase string) string { return filepath.Join(previewBaseDir(), phase) }

// previewOutPath devolve o caminho final do PNG de um nome de card.
func previewOutPath(phase, name string) string {
	return filepath.Join(previewDir(phase), name+".png")
}

// renderCardPNG renderiza o estado na fase informada com a fração de contagem
// dada e grava o PNG. Devolve a superfície (o chamador deve fechá-la).
// O relógio é congelado (state.now) para deixar o quadro determinístico: o
// mesmo frozen hora é usado no "agora" e na âncora do deadline.
func renderCardPNG(t *testing.T, phase, name string, state *cardState, ratio float64) *previewSurface {
	t.Helper()
	total := state.total
	frozen := time.Now()
	state.now = func() time.Time { return frozen }
	if ratio <= 0 {
		state.deadline = frozen.Add(-time.Second) // contagem encerrada
	} else {
		state.deadline = frozen.Add(time.Duration(float64(total) * ratio))
	}
	surf, err := newPreviewSurface(winui.ScalePx(cardWidth, state.scale), state.cardHeight())
	if err != nil {
		t.Fatalf("criando superfície %s: %v", name, err)
	}
	surf.fill(color.RGBA{R: 0x40, G: 0x80, B: 0x40, A: 255}) // fundo "área de trabalho"
	// dirty vazio = repinte completo (mesmo caminho do WM_PRINTCLIENT).
	state.paint(surf.dc, winui.Rect{})
	out := previewOutPath(phase, name)
	if err := surf.savePNG(out); err != nil {
		surf.close()
		t.Fatalf("gravando %s: %v", out, err)
	}
	return surf
}

// accentNear diz se o pixel está próximo da cor de destaque do tema.
func accentNear(px color.RGBA, accent winui.Color) bool {
	dr := int(px.R) - int(accent.R())
	dg := int(px.G) - int(accent.G())
	db := int(px.B) - int(accent.B())
	return dr*dr+dg*dg+db*db <= 40*40
}

// fillRightEdge varre a linha central do trilho da barra e devolve a borda
// direita do preenchimento accent (x máximo ainda accent), ou -1 se não achar.
func fillRightEdge(surf *previewSurface, state *cardState) int {
	track := state.layout.progressTrack
	if track.W <= 0 || track.H <= 0 {
		return -1
	}
	y := track.Y + track.H/2
	accent := state.theme.Accent
	for x := track.X + track.W - 1; x > track.X; x-- {
		// Exige 2 pixels consecutivos accent para ignorar a borda AA do trilho.
		if accentNear(surf.pixelAt(x, y), accent) && accentNear(surf.pixelAt(x-1, y), accent) {
			return x
		}
	}
	return -1
}

// previewCardName monta o nome do PNG: card-<tema>[-dpiNNN]-rNNN.
func previewCardName(themeName string, scale float64, ratio float64) string {
	dpiTag := ""
	if scale != 1.0 {
		dpiTag = fmt.Sprintf("-dpi%d", int(scale*96))
	}
	return fmt.Sprintf("card-%s%s-r%03d", themeName, dpiTag, int(ratio*100+0.5))
}

// TestPreviewCardRender gera os PNGs de pré-visualização do cartão.
func TestPreviewCardRender(t *testing.T) {
	if os.Getenv("DISCOVERY_UI_PREVIEW") != "1" {
		t.Skip("preview visual desativado (defina DISCOVERY_UI_PREVIEW=1)")
	}
	phase := os.Getenv("DISCOVERY_UI_PREVIEW_PHASE")
	if phase == "" {
		phase = "before"
	}
	req := previewRequest()
	for _, themeName := range []string{"dark", "light"} {
		for _, scale := range []float64{1.0, 1.5} {
			state := newPreviewState(t, req, themeName, scale)
			for _, ratio := range []float64{1.0, 0.75, 0.5, 0.25, 0.05} {
				name := previewCardName(themeName, scale, ratio)
				surf := renderCardPNG(t, phase, name, state, ratio)
				t.Logf("gerado %s", previewOutPath(phase, name))
				surf.close()
			}
			state.fonts.release()
		}
	}
}

// TestPreviewDumpLayout exporta a geometria REAL do layout (px do DIB) para
// compare/layout-<phase>.json, consumida pelo crop.py — assim os recortes usam
// progressTrack/countValue em vez de procurar a barra por cor. Não desenha
// nada; só usa cardLayoutFor (função pura) e a mesma lógica de altura extra.
func TestPreviewDumpLayout(t *testing.T) {
	if os.Getenv("DISCOVERY_UI_PREVIEW") != "1" {
		t.Skip("preview visual desativado (defina DISCOVERY_UI_PREVIEW=1)")
	}
	phase := os.Getenv("DISCOVERY_UI_PREVIEW_PHASE")
	if phase == "" {
		phase = "before"
	}
	req := previewRequest()
	type rectJSON struct {
		X int `json:"x"`
		Y int `json:"y"`
		W int `json:"w"`
		H int `json:"h"`
	}
	out := map[string]map[string]rectJSON{}
	for _, scale := range []float64{1.0, 1.5} {
		state := newPreviewState(t, req, "dark", scale)
		toJSON := func(r winui.Rect) rectJSON { return rectJSON{r.X, r.Y, r.W, r.H} }
		out[fmt.Sprintf("%.1f", scale)] = map[string]rectJSON{
			"progressTrack": toJSON(state.layout.progressTrack),
			"countValue":    toJSON(state.layout.countValue),
			"countLabel":    toJSON(state.layout.countLabel),
		}
		state.fonts.release()
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	dir := filepath.Join(previewBaseDir(), "compare")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "layout-"+phase+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("gravando %s: %v", path, err)
	}
	t.Logf("layout %s: %s", phase, path)
}

// TestPreviewBarSmoothness mede a suavidade da barra de contagem: renderiza
// frames simulados a cada 16 ms (60 fps) e mede a borda direita do
// preenchimento; complementa com uma simulação geométrica exata da fórmula
// atual de paint() atualizada a cada tick de 200 ms.
func TestPreviewBarSmoothness(t *testing.T) {
	if os.Getenv("DISCOVERY_UI_PREVIEW") != "1" {
		t.Skip("preview visual desativado (defina DISCOVERY_UI_PREVIEW=1)")
	}
	req := previewRequest()

	for _, totalSec := range []int{60, 15} {
		state := newPreviewState(t, req, "dark", 1.0)
		total := time.Duration(totalSec) * time.Second
		state.total = total

		trackW := state.layout.progressTrack.W
		t.Logf("contagem %ds: trilho = %d px", totalSec, trackW)

		// Simulação geométrica do comportamento ANTIGO (200 ms): W =
		// int(trackW * ratio) com clamp mínimo de 8 px, atualizado a cada tick.
		// É esse passo inteiro por tick que causava os "pulos" percebidos.
		const tick = 200 * time.Millisecond
		maxJumpTick := 0
		prevW := -1
		for elapsed := time.Duration(0); elapsed <= total; elapsed += tick {
			ratio := float64(total-elapsed) / float64(total)
			w := int(float64(trackW) * ratio)
			if w < winui.ScalePx(8, state.scale) {
				w = winui.ScalePx(8, state.scale)
			}
			if w > trackW {
				w = trackW
			}
			if prevW >= 0 {
				jump := prevW - w
				if jump < 0 {
					jump = -jump
				}
				if jump > maxJumpTick {
					maxJumpTick = jump
				}
			}
			prevW = w
		}
		t.Logf("[geom %ds] maior salto por tick de 200ms = %d px (passo teórico %.2f px/tick)",
			totalSec, maxJumpTick, float64(trackW)*float64(tick)/float64(total))

		// Medição real por pixels: frames a cada 16 ms durante uma janela de
		// 5 s no meio da contagem (a contagem é linear; a janela representa o
		// comportamento inteiro e mantém o custo do teste baixo).
		const frame = 16 * time.Millisecond
		const window = 5 * time.Second
		startElapsed := total/2 - window/2
		// Via geométrica da versão NOVA: cardBarFill (coordenadas fracionárias)
		// avaliada a cada 16 ms. A borda direita avança frações de pixel por
		// quadro — sem quantização inteira.
		maxJumpGeom16 := 0.0
		prevRight := -1.0
		for i := 0; i <= int(window/frame); i++ {
			elapsed := startElapsed + time.Duration(i)*frame
			ratio := float64(total-elapsed) / float64(total)
			fill := cardBarFill(state.layout.progressTrack, state.scale, ratio)
			if fill.Empty() {
				continue
			}
			right := fill.Right()
			if prevRight >= 0 {
				jump := right - prevRight
				if jump < 0 {
					jump = -jump
				}
				if jump > maxJumpGeom16 {
					maxJumpGeom16 = jump
				}
			}
			prevRight = right
		}
		t.Logf("[geom16 %ds] maior salto da borda direita por frame (16ms, cardBarFill) = %.3f px",
			totalSec, maxJumpGeom16)

		maxJumpFrame := 0
		sumJumpFrame := 0
		nFrames := 0
		prevEdge := -1
		for i := 0; i <= int(window/frame); i++ {
			elapsed := startElapsed + time.Duration(i)*frame
			ratio := float64(total-elapsed) / float64(total)
			if ratio > 1 {
				ratio = 1
			}
			if ratio < 0 {
				ratio = 0
			}
			// Relógio congelado: quadro determinístico (mesma âncora no
			// now() e no deadline, igual ao renderCardPNG).
			frozen := time.Now()
			state.now = func() time.Time { return frozen }
			state.deadline = frozen.Add(time.Duration(float64(total) * ratio))
			surf, err := newPreviewSurface(winui.ScalePx(cardWidth, state.scale), state.cardHeight())
			if err != nil {
				t.Fatalf("frame %d: %v", i, err)
			}
			surf.fill(color.RGBA{R: 0x40, G: 0x80, B: 0x40, A: 255})
			state.paint(surf.dc, winui.Rect{})
			edge := fillRightEdge(surf, state)
			surf.close()
			if edge < 0 {
				t.Logf("[pixel %ds] frame %d: borda do preenchimento não encontrada", totalSec, i)
				continue
			}
			if prevEdge >= 0 {
				jump := edge - prevEdge
				if jump < 0 {
					jump = -jump
				}
				nFrames++
				sumJumpFrame += jump
				if jump > maxJumpFrame {
					maxJumpFrame = jump
				}
			}
			prevEdge = edge
		}
		avg := 0.0
		if nFrames > 0 {
			avg = float64(sumJumpFrame) / float64(nFrames)
		}
		t.Logf("[pixel %ds] frames=%d maior salto px/frame(16ms) = %d px; salto médio = %.2f px/frame",
			totalSec, nFrames, maxJumpFrame, avg)
		state.fonts.release()
	}
}

// aaEdgeCoverage mede a cobertura (0..1) da coluna de transição da borda
// direita do preenchimento. No trilho sem preenchimento a coluna é trilho
// puro (0.0); preenchimento pleno = 1.0; 0<x<1 = pixel sub-pixel (AA).
// Usa as cores reais amostradas do próprio DIB (não constantes do tema).
func aaEdgeCoverage(surf *previewSurface, state *cardState) (edge int, cov float64) {
	track := state.layout.progressTrack
	if track.W <= 4 || track.H <= 0 {
		return -1, 0
	}
	y := track.Y + track.H/2
	// Cores de referência: trilho na ponta direita, preenchimento na ponta esquerda.
	trackPx := surf.pixelAt(track.X+track.W-2, y)
	fillPx := surf.pixelAt(track.X+2, y)
	trackB := float64(trackPx.B)
	fillB := float64(fillPx.B)
	if fillB-trackB < 40 {
		return -1, 0
	}
	for x := track.X + track.W - 1; x > track.X; x-- {
		b := float64(surf.pixelAt(x, y).B)
		if b <= trackB+2 {
			continue
		}
		cov = (b - trackB) / (fillB - trackB)
		if cov > 1 {
			cov = 1
		}
		return x, cov
	}
	return -1, 0
}

// TestPreviewBarIncrementalAA é a regressão do anti-aliasing em quadros
// repetidos: no refresh incremental (~60 fps) a MESMA faixa da barra é
// redesenhada por cima do quadro anterior. O AA do GDI+ não é idempotente, e
// sem restaurar o snapshot estático (primeBarBase) a coluna de transição
// endurecia em ~20 quadros (borda vira serrilhado). Aqui congelamos o relógio,
// forçamos a borda em posição fracionária e medimos a cobertura ao longo de 40
// quadros: ela deve ficar estável e fracionária.
func TestPreviewBarIncrementalAA(t *testing.T) {
	if os.Getenv("DISCOVERY_UI_PREVIEW") != "1" {
		t.Skip("preview visual desativado (defina DISCOVERY_UI_PREVIEW=1)")
	}
	req := previewRequest()
	state := newPreviewState(t, req, "dark", 1.0)
	defer state.fonts.release()

	total := state.total
	// 0,25 cai em borda fracionária (25 + 510*0,25 = 152,5 px): cobertura ~0,45.
	frozen := time.Now()
	state.now = func() time.Time { return frozen }
	state.deadline = frozen.Add(time.Duration(float64(total) * 0.25))

	surf, err := newPreviewSurface(winui.ScalePx(cardWidth, state.scale), state.cardHeight())
	if err != nil {
		t.Fatalf("superfície: %v", err)
	}
	defer surf.close()
	surf.fill(color.RGBA{R: 0x40, G: 0x80, B: 0x40, A: 255})

	// Primeiro quadro completo (preenche o back buffer e o snapshot da barra).
	state.paint(surf.dc, winui.Rect{})
	edge0, cov0 := aaEdgeCoverage(surf, state)
	if edge0 < 0 {
		t.Fatalf("borda do preenchimento não encontrada no quadro inicial")
	}

	minCov, maxCov := cov0, cov0
	maxEdgeDrift := 0
	for i := 0; i < 40; i++ {
		// Repinte incremental: só a faixa da barra (caminho da animação).
		state.paint(surf.dc, state.countdownArea())
		edge, cov := aaEdgeCoverage(surf, state)
		if edge < 0 {
			t.Fatalf("quadro %d: borda não encontrada", i+1)
		}
		if cov < minCov {
			minCov = cov
		}
		if cov > maxCov {
			maxCov = cov
		}
		if d := edge - edge0; d > maxEdgeDrift {
			maxEdgeDrift = d
		} else if d < -maxEdgeDrift {
			maxEdgeDrift = -d
		}
	}
	t.Logf("[AA-40-quadros] cobertura inicial=%.2f final=%0.2f min=%.2f max=%.2f deriva_max=%d px",
		cov0, maxCov, minCov, maxCov, maxEdgeDrift)
	if maxEdgeDrift != 0 {
		t.Errorf("borda da barra derivou %d px em 40 quadros com relógio congelado", maxEdgeDrift)
	}
	if cov0 <= 0.02 || cov0 >= 0.98 {
		t.Errorf("cobertura inicial %.2f não é fracionária: a borda não está em posição sub-pixel", cov0)
	}
	if maxCov-minCov > 0.02 {
		t.Errorf("cobertura da borda variou %.2f em 40 quadros (AA não idempotente)", maxCov-minCov)
	}
}
