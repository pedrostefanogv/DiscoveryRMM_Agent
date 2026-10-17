package screenshot

import (
	"bytes"
	"testing"

	"discovery/app/core/screen"
)

// makeScrollFrame cria um frame BGRA sintético cuja PRIMEIRA linha corresponde
// à linha absoluta startY do "documento". Linhas com o mesmo índice absoluto são
// idênticas, o que permite verificar a costura por conteúdo.
func makeScrollFrame(width, startY, height, padding int) *screen.Frame {
	stride := width*4 + padding
	data := make([]byte, stride*height)
	for y := 0; y < height; y++ {
		row := startY + y
		for x := 0; x < width; x++ {
			off := y*stride + x*4
			data[off] = byte(x*7 + 1)
			data[off+1] = byte(row)
			data[off+2] = byte(row >> 8)
			data[off+3] = 255
		}
	}
	return &screen.Frame{Data: data, Width: width, Height: height, Stride: stride}
}

func outputRow(frame *screen.Frame, y int) []byte {
	start := y * frame.Stride
	return frame.Data[start : start+frame.Width*4]
}

func absoluteRowOf(frame *screen.Frame, y int) int {
	row := outputRow(frame, y)
	return int(row[1]) | int(row[2])<<8
}

func TestStitchScrollFramesExactOverlap(t *testing.T) {
	first := makeScrollFrame(10, 0, 100, 0)
	second := makeScrollFrame(10, 40, 100, 0)
	stitched, used := StitchScrollFrames([]*screen.Frame{first, second}, 80)
	if stitched == nil || used != 2 {
		t.Fatalf("costura = %v used=%d", stitched, used)
	}
	if stitched.Width != 10 || stitched.Height != 140 {
		t.Fatalf("dimensoes = %dx%d, want 10x140", stitched.Width, stitched.Height)
	}
	if stitched.Stride != stitched.Width*4 {
		t.Fatalf("stride = %d", stitched.Stride)
	}
	if got := absoluteRowOf(stitched, 139); got != 139 {
		t.Fatalf("ultima linha = %d, want 139", got)
	}
	if got := absoluteRowOf(stitched, 100); got != 100 {
		t.Fatalf("linha 100 = %d", got)
	}
}

func TestStitchScrollFramesPartialOverlap(t *testing.T) {
	first := makeScrollFrame(6, 0, 100, 0)
	second := makeScrollFrame(6, 70, 100, 0)
	stitched, used := StitchScrollFrames([]*screen.Frame{first, second}, 80)
	if stitched == nil || used != 2 {
		t.Fatalf("costura = %v used=%d", stitched, used)
	}
	if stitched.Height != 170 {
		t.Fatalf("altura = %d, want 170", stitched.Height)
	}
	if got := absoluteRowOf(stitched, 169); got != 169 {
		t.Fatalf("ultima linha = %d", got)
	}
}

func TestStitchScrollFramesToleratesFewDifferentRows(t *testing.T) {
	first := makeScrollFrame(8, 0, 100, 0)
	second := makeScrollFrame(8, 40, 100, 0)
	// Simula cursor piscando: uma linha do topo da sobreposição muda.
	row := outputRow(second, 3)
	for i := range row {
		row[i] ^= 0xFF
	}
	stitched, _ := StitchScrollFrames([]*screen.Frame{first, second}, 80)
	if stitched == nil || stitched.Height != 140 {
		t.Fatalf("altura = %d, want 140", stitched.Height)
	}
}

func TestStitchScrollFramesNoOverlapConcatenates(t *testing.T) {
	first := makeScrollFrame(5, 0, 100, 0)
	second := makeScrollFrame(5, 500, 100, 0)
	stitched, used := StitchScrollFrames([]*screen.Frame{first, second}, 80)
	if stitched == nil || used != 2 {
		t.Fatalf("costura = %v used=%d", stitched, used)
	}
	if stitched.Height != 200 {
		t.Fatalf("altura = %d, want 200", stitched.Height)
	}
	if got := absoluteRowOf(stitched, 100); got != 500 {
		t.Fatalf("inicio do segundo frame = %d, want 500", got)
	}
}

func TestStitchScrollFramesEmpty(t *testing.T) {
	if frame, used := StitchScrollFrames(nil, 0); frame != nil || used != 0 {
		t.Fatalf("nil = %v used=%d", frame, used)
	}
	empty := &screen.Frame{Width: 0, Height: 0}
	if frame, used := StitchScrollFrames([]*screen.Frame{nil, empty}, 0); frame != nil || used != 0 {
		t.Fatalf("frames invalidos = %v used=%d", frame, used)
	}
}

func TestStitchScrollFramesContainedFrame(t *testing.T) {
	first := makeScrollFrame(4, 0, 100, 0)
	same := makeScrollFrame(4, 0, 100, 0)
	stitched, used := StitchScrollFrames([]*screen.Frame{first, same}, 200)
	if stitched == nil || used != 2 {
		t.Fatalf("costura = %v used=%d", stitched, used)
	}
	if stitched.Height != 100 {
		t.Fatalf("frame contido nao deveria crescer: %d", stitched.Height)
	}
}

func TestStitchScrollFramesLargeOverlap(t *testing.T) {
	// Cenário real de rolagem curta: tela de 1080 px desceu 360 px, ou seja,
	// 720 px de sobreposição. A busca precisa cobrir a altura do frame; com uma
	// faixa curta a costura falhava e duplicava o conteúdo.
	first := makeScrollFrame(8, 0, 1080, 0)
	second := makeScrollFrame(8, 360, 1080, 0)
	stitched, used := StitchScrollFrames([]*screen.Frame{first, second}, first.Height)
	if stitched == nil || used != 2 {
		t.Fatalf("costura = %v used=%d", stitched, used)
	}
	if stitched.Height != 1440 {
		t.Fatalf("altura = %d, want 1440", stitched.Height)
	}
	if got := absoluteRowOf(stitched, 1439); got != 1439 {
		t.Fatalf("ultima linha = %d, want 1439", got)
	}
}

func TestStitchScrollFramesSkipsDifferentWidth(t *testing.T) {
	first := makeScrollFrame(8, 0, 40, 0)
	wider := makeScrollFrame(9, 20, 40, 0)
	stitched, used := StitchScrollFrames([]*screen.Frame{first, wider}, 60)
	if stitched == nil || used != 1 {
		t.Fatalf("costura = %v used=%d", stitched, used)
	}
	if stitched.Width != 8 || stitched.Height != 40 {
		t.Fatalf("dimensoes = %dx%d", stitched.Width, stitched.Height)
	}
}

func TestStitchScrollFramesCapsHeight(t *testing.T) {
	first := makeScrollFrame(4, 0, 15000, 0)
	second := makeScrollFrame(4, 14000, 15000, 0)
	stitched, used := StitchScrollFrames([]*screen.Frame{first, second}, 2000)
	if stitched == nil || used != 2 {
		t.Fatalf("costura = %v used=%d", stitched, used)
	}
	if stitched.Height != MaxStitchHeight {
		t.Fatalf("altura = %d, want %d", stitched.Height, MaxStitchHeight)
	}
}

func TestStitchScrollFramesHandlesPaddedStride(t *testing.T) {
	first := makeScrollFrame(7, 0, 60, 12)
	second := makeScrollFrame(7, 20, 60, 12)
	stitched, _ := StitchScrollFrames([]*screen.Frame{first, second}, 60)
	if stitched == nil {
		t.Fatal("costura nil")
	}
	if stitched.Stride != stitched.Width*4 {
		t.Fatalf("stride de saida = %d, want %d", stitched.Stride, stitched.Width*4)
	}
	if stitched.Height != 80 {
		t.Fatalf("altura = %d, want 80", stitched.Height)
	}
	for y := 0; y < stitched.Height; y++ {
		expected := absoluteRowOf(stitched, y)
		if expected != y {
			t.Fatalf("linha %d contem %d", y, expected)
		}
	}
}

func TestFramesIdentical(t *testing.T) {
	a := makeScrollFrame(6, 0, 20, 0)
	b := makeScrollFrame(6, 0, 20, 0)
	if !FramesIdentical(a, b) {
		t.Fatal("frames iguais deveriam ser identicos")
	}
	c := makeScrollFrame(6, 1, 20, 0)
	if FramesIdentical(a, c) {
		t.Fatal("frames diferentes nao podem ser identicos")
	}
	if FramesIdentical(a, nil) || FramesIdentical(nil, a) {
		t.Fatal("nil nunca e identico")
	}
}

func TestFindVerticalOverlapExact(t *testing.T) {
	top := makeScrollFrame(5, 0, 50, 0)
	bottom := makeScrollFrame(5, 30, 50, 0)
	if got := findVerticalOverlap(top, bottom, 40); got != 20 {
		t.Fatalf("sobreposicao = %d, want 20", got)
	}
	if got := findVerticalOverlap(top, makeScrollFrame(5, 400, 50, 0), 40); got != 0 {
		t.Fatalf("sem sobreposicao = %d, want 0", got)
	}
}

func TestHashRowDifferentContent(t *testing.T) {
	a := makeScrollFrame(4, 0, 2, 0)
	b := makeScrollFrame(4, 1, 2, 0)
	if bytes.Equal(outputRow(a, 0), outputRow(b, 0)) {
		t.Fatal("linhas de teste deveriam diferir")
	}
	if hashRow(outputRow(a, 0)) == hashRow(outputRow(b, 0)) {
		t.Fatal("hash de linhas diferentes colidiu no teste")
	}
}

// makeChromeFrame cria um frame sintético de "desktop com chrome fixo": faixa
// superior fixa (barra de título), conteúdo rolável que começa na linha absoluta
// startY e faixa inferior fixa (taskbar). Linhas de conteúdo carregam o índice
// absoluto em BGRA para verificar a costura linha a linha.
func makeChromeFrame(width, startY, contentHeight, headerH, footerH int) *screen.Frame {
	height := headerH + contentHeight + footerH
	stride := width * 4
	data := make([]byte, stride*height)
	writeRow := func(y int, kind byte, value int) {
		for x := 0; x < width; x++ {
			off := y*stride + x*4
			data[off] = byte(x*7 + 1)
			data[off+1] = byte(value)
			data[off+2] = byte(value >> 8)
			data[off+3] = kind
		}
	}
	for y := 0; y < headerH; y++ {
		writeRow(y, 0xFE, y) // chrome de cima: fixedo por posição
	}
	for y := 0; y < contentHeight; y++ {
		writeRow(headerH+y, 0x01, startY+y)
	}
	for y := 0; y < footerH; y++ {
		writeRow(headerH+contentHeight+y, 0xFD, y)
	}
	return &screen.Frame{Data: data, Width: width, Height: height, Stride: stride}
}

func TestStitchScrollFramesFixedChromeDoesNotDuplicateScreen(t *testing.T) {
	// Cenário real: tela de 990 px, barra de título de 30 px e taskbar de 20 px
	// fixas; o conteúdo rola 60 px entre capturas. Sem a detecção de faixas fixas
	// a costura concatenava os frames inteiros (duplicando chrome e conteúdo).
	const (
		width, contentHeight = 8, 940
		headerH, footerH     = 30, 20
		scroll               = 60
	)
	frames := []*screen.Frame{
		makeChromeFrame(width, 0, contentHeight, headerH, footerH),
		makeChromeFrame(width, scroll, contentHeight, headerH, footerH),
		makeChromeFrame(width, 2*scroll, contentHeight, headerH, footerH),
	}

	// Passa uma janela de busca pequena de propósito: com as faixas fixas
	// detectadas, a busca precisa cobrir a altura do CONTEÚDO, não a da tela.
	stitched, used := StitchScrollFrames(frames, headerH+footerH+scroll)
	if stitched == nil || used != 3 {
		t.Fatalf("costura = %v used=%d", stitched, used)
	}

	expectedHeight := headerH + contentHeight + 2*scroll + footerH
	if stitched.Height != expectedHeight {
		t.Fatalf("altura = %d, want %d (conteudo sem duplicacao)", stitched.Height, expectedHeight)
	}

	// Topo: chrome fixo (alpha 0xFE), sem conteúdo duplicado.
	for y := 0; y < headerH; y++ {
		if got := outputRow(stitched, y)[3]; got != 0xFE {
			t.Fatalf("linha %d do cabecalho tem alpha %#x, want 0xFE", y, got)
		}
	}
	// Conteúdo: sequência contínua 0..contentHeight+2*scroll-1, sem buraco/dup.
	for i := 0; i < contentHeight+2*scroll; i++ {
		row := outputRow(stitched, headerH+i)
		if row[3] != 0x01 {
			t.Fatalf("linha %d do conteudo tem alpha %#x", headerH+i, row[3])
		}
		if got := int(row[1]) | int(row[2])<<8; got != i {
			t.Fatalf("linha %d do conteudo = %d, want %d", headerH+i, got, i)
		}
	}
	// Rodapé: chrome fixo aparece UMA vez, no fim.
	for i := 0; i < footerH; i++ {
		row := outputRow(stitched, headerH+contentHeight+2*scroll+i)
		if row[3] != 0xFD {
			t.Fatalf("linha %d do rodape tem alpha %#x, want 0xFD", headerH+contentHeight+2*scroll+i, row[3])
		}
	}
}

func TestDetectFixedBandsRequiresConstantRows(t *testing.T) {
	// Sem chrome (conteúdo puro deslocado) não há faixa fixa a detectar.
	plainA := makeScrollFrame(6, 0, 100, 0)
	plainB := makeScrollFrame(6, 40, 100, 0)
	if top, bottom := detectFixedBands(plainA, []*screen.Frame{plainB}); top != 0 || bottom != 0 {
		t.Fatalf("bandas sem chrome = %d/%d, want 0/0", top, bottom)
	}

	// Com chrome de 20 px em cima e 10 px embaixo, as bandas são identificadas.
	chromeA := makeChromeFrame(6, 0, 200, 20, 10)
	chromeB := makeChromeFrame(6, 50, 200, 20, 10)
	top, bottom := detectFixedBands(chromeA, []*screen.Frame{chromeB})
	if top != 20 || bottom != 10 {
		t.Fatalf("bandas com chrome = %d/%d, want 20/10", top, bottom)
	}
}

func TestStitchScrollFramesFixedChromeCapsHeight(t *testing.T) {
	const (
		width, contentHeight = 4, 15000
		headerH, footerH     = 100, 100
	)
	frames := []*screen.Frame{
		makeChromeFrame(width, 0, contentHeight, headerH, footerH),
		makeChromeFrame(width, 14000, contentHeight, headerH, footerH),
	}
	stitched, _ := StitchScrollFrames(frames, 4000)
	if stitched == nil {
		t.Fatal("costura nil")
	}
	if stitched.Height != MaxStitchHeight {
		t.Fatalf("altura = %d, want %d", stitched.Height, MaxStitchHeight)
	}
	// O corte acontece no conteúdo: o rodapé fixo continua sendo a última linha.
	if got := outputRow(stitched, stitched.Height-1)[3]; got != 0xFD {
		t.Fatalf("ultima linha alpha = %#x, want 0xFD (rodape preservado)", got)
	}
}
