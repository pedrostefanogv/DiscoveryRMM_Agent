//go:build windows

package notifyui

import (
	"math"
	"testing"
	"time"

	"discovery/app/core/winui"
)

// NormalizeCardRequest garante defaults estáveis usados pelo desenho.
func TestNormalizeCardRequest(t *testing.T) {
	cases := []struct {
		name string
		in   CardRequest
		want CardRequest
	}{
		{
			name: "defaults sem botões",
			in:   CardRequest{},
			want: CardRequest{
				Title:           "Discovery Agent",
				Subtitle:        "Discovery Agent",
				Message:         " ",
				Buttons:         []Button{{ID: "ok", Label: "OK", Kind: "primary"}},
				DefaultButtonID: "ok",
				CountdownLabel:  "",
				Hint:            "Enter confirma · Esc fecha",
			},
		},
		{
			name: "contador sem timeout button usa o default",
			in: CardRequest{
				Title:            "Teste",
				CountdownSeconds: 30,
				Buttons:          []Button{{ID: "cancel", Label: "Cancelar"}, {ID: "go", Label: "Ir"}},
			},
			want: CardRequest{
				Title:            "Teste",
				Subtitle:         "Discovery Agent",
				Message:          " ",
				Buttons:          []Button{{ID: "cancel", Label: "Cancelar"}, {ID: "go", Label: "Ir"}},
				DefaultButtonID:  "go",
				CountdownSeconds: 30,
				TimeoutButtonID:  "go",
				CountdownLabel:   "Tempo restante:",
				Hint:             "Enter confirma · Esc fecha",
			},
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got := NormalizeCardRequest(item.in)
			if got.Title != item.want.Title || got.Subtitle != item.want.Subtitle || got.Message != item.want.Message {
				t.Fatalf("cabeçalho inesperado: %+v", got)
			}
			if len(got.Buttons) != len(item.want.Buttons) {
				t.Fatalf("botões = %+v, want %+v", got.Buttons, item.want.Buttons)
			}
			for i := range got.Buttons {
				if got.Buttons[i] != item.want.Buttons[i] {
					t.Fatalf("botão %d = %+v, want %+v", i, got.Buttons[i], item.want.Buttons[i])
				}
			}
			if got.DefaultButtonID != item.want.DefaultButtonID {
				t.Fatalf("default = %q, want %q", got.DefaultButtonID, item.want.DefaultButtonID)
			}
			if got.TimeoutButtonID != item.want.TimeoutButtonID {
				t.Fatalf("timeout = %q, want %q", got.TimeoutButtonID, item.want.TimeoutButtonID)
			}
			if got.CountdownLabel != item.want.CountdownLabel {
				t.Fatalf("label = %q, want %q", got.CountdownLabel, item.want.CountdownLabel)
			}
		})
	}
}

// Sem contador o timeout button não pode ser inventado.
func TestNormalizeCardRequestNoCountdown(t *testing.T) {
	got := NormalizeCardRequest(CardRequest{Title: "x"})
	if got.TimeoutButtonID != "" {
		t.Fatalf("timeout button deveria ficar vazio sem contador, got %q", got.TimeoutButtonID)
	}
}

// CountdownClock formata mm:ss e trava em 00:00.
func TestCountdownClock(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "00:00"},
		{59 * time.Second, "00:59"},
		{time.Minute, "01:00"},
		{-5 * time.Second, "00:00"},
		{3600 * time.Second, "60:00"},
	}
	for _, item := range cases {
		if got := CountdownClock(item.in); got != item.want {
			t.Fatalf("CountdownClock(%v) = %q, want %q", item.in, got, item.want)
		}
	}
}

// Layout: botões alinhados à direita, contador empurra os botões para baixo.
func TestCardLayoutFor(t *testing.T) {
	plain := cardLayoutFor(1, 0, false, 2, 0)
	if len(plain.buttons) != 2 {
		t.Fatalf("buttons = %d, want 2", len(plain.buttons))
	}
	// Último botão encosta na borda direita menos o pad.
	last := plain.buttons[1]
	if last.Right() != cardWidth-cardPad {
		t.Fatalf("último botão right = %d, want %d", last.Right(), cardWidth-cardPad)
	}
	first := plain.buttons[0]
	if first.Right()+cardButtonGap != last.X {
		t.Fatalf("gap entre botões inesperado: first.Right=%d last.X=%d", first.Right(), last.X)
	}

	withCount := cardLayoutFor(1, 0, true, 2, 0)
	if withCount.buttons[0].Y <= plain.buttons[0].Y {
		t.Fatalf("contador deveria empurrar os botões para baixo: %d <= %d", withCount.buttons[0].Y, plain.buttons[0].Y)
	}
	if withCount.progressTrack.W != cardWidth-2*cardPad {
		t.Fatalf("largura da barra = %d, want %d", withCount.progressTrack.W, cardWidth-2*cardPad)
	}

	// Mensagem longa cresce o cartão.
	grown := cardLayoutFor(1, 40, false, 1, 0)
	if grown.messageBox.H != 84+40 {
		t.Fatalf("caixa de mensagem = %d, want %d", grown.messageBox.H, 84+40)
	}

	// Escala DPI cresce tudo proporcionalmente.
	scaled := cardLayoutFor(2, 0, false, 1, 0)
	if scaled.buttons[0].W != cardButtonW*2 {
		t.Fatalf("botão escalado = %d, want %d", scaled.buttons[0].W, cardButtonW*2)
	}
}

// MaxButtons: só os três primeiros botões entram.
func TestNormalizeCardRequestMaxButtons(t *testing.T) {
	got := NormalizeCardRequest(CardRequest{
		Buttons: []Button{
			{ID: "a", Label: "A"}, {ID: "b", Label: "B"}, {ID: "c", Label: "C"}, {ID: "d", Label: "D"},
		},
	})
	if len(got.Buttons) != 3 {
		t.Fatalf("botões = %d, want 3", len(got.Buttons))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got.Buttons[i].ID != want {
			t.Fatalf("botão %d = %q, want %q", i, got.Buttons[i].ID, want)
		}
	}
}

// A lista de processos empurra botões/contador para baixo e as linhas ficam
// dentro da caixa (mesmo padrão do Welcome do PSADT).
func TestCardLayoutForProcessRows(t *testing.T) {
	plain := cardLayoutFor(1, 0, true, 2, 0)
	withList := cardLayoutFor(1, 0, true, 2, 3)
	if withList.buttons[0].Y <= plain.buttons[0].Y {
		t.Fatalf("lista deveria empurrar os botões: %d <= %d", withList.buttons[0].Y, plain.buttons[0].Y)
	}
	if len(withList.processRows) != 3 {
		t.Fatalf("linhas = %d, want 3", len(withList.processRows))
	}
	box := withList.processBox
	for i, row := range withList.processRows {
		if row.Y < box.Y || row.Bottom() > box.Bottom() {
			t.Fatalf("linha %d fora da caixa: row=%+v box=%+v", i, row, box)
		}
		if row.X <= box.X || row.Right() > box.Right() {
			t.Fatalf("linha %d fora da margem da caixa", i)
		}
	}

	// Sem lista, nada muda em relação ao layout antigo.
	if plain.processBox.W != 0 || len(plain.processRows) != 0 {
		t.Fatalf("sem lista não deveria haver caixa de processos")
	}
}

// NormalizeCardRequest limpa a lista e limita a cardMaxProcessRows linhas.
func TestNormalizeCardRequestProcessList(t *testing.T) {
	got := NormalizeCardRequest(CardRequest{
		ProcessList: []ProcessItem{
			{DisplayName: " firefox.exe ", ImagePath: " C:\\x\\firefox.exe "},
			{},
			{DisplayName: "code.exe"},
			{DisplayName: "a"}, {DisplayName: "b"}, {DisplayName: "c"},
			{DisplayName: "d"},
		},
	})
	if len(got.ProcessList) != cardMaxProcessRows {
		t.Fatalf("lista = %d, want %d", len(got.ProcessList), cardMaxProcessRows)
	}
	if got.ProcessList[0].DisplayName != "firefox.exe" || got.ProcessList[0].ImagePath != "C:\\x\\firefox.exe" {
		t.Fatalf("item 0 não foi limpo: %+v", got.ProcessList[0])
	}
	if got.ProcessList[1].DisplayName != "code.exe" {
		t.Fatalf("item vazio deveria ser removido: %+v", got.ProcessList[1])
	}
}

// ── Barra da contagem regressiva ──
//
// Regressão do "chute/pulo" na barra: o preenchimento é calculado em
// coordenadas fracionárias (nada de arredondar para o pixel inteiro) e avança
// pouco por quadro, então o movimento é contínuo.

// cardBarFill: cápsula deslocada 1px para dentro do trilho, largura
// proporcional ao tempo restante e saturação fora de [0,1].
func TestCardBarFillGeometry(t *testing.T) {
	track := winui.Rect{X: 24, Y: 200, W: 512, H: cardBarHeight}

	full := cardBarFill(track, 1, 1)
	if full.X != float64(track.X+cardBarInset) || full.Y != float64(track.Y+cardBarInset) {
		t.Fatalf("origem do preenchimento = (%v,%v), esperado (%d,%d)", full.X, full.Y, track.X+cardBarInset, track.Y+cardBarInset)
	}
	fullWidth := float64(track.W - 2*cardBarInset)
	if math.Abs(full.W-fullWidth) > 1e-9 || math.Abs(full.H-float64(cardBarHeight-2*cardBarInset)) > 1e-9 {
		t.Fatalf("tamanho cheio = %vx%v, esperado %vx%d", full.W, full.H, fullWidth, cardBarHeight-2*cardBarInset)
	}
	if got, want := cardBarFill(track, 1, 0.5).W, fullWidth*0.5; math.Abs(got-want) > 1e-9 {
		t.Fatalf("metade = %v, esperado %v", got, want)
	}
	if got := cardBarFill(track, 1, 1.7).W; math.Abs(got-fullWidth) > 1e-9 {
		t.Fatalf("ratio > 1 deveria saturar em %v, veio %v", fullWidth, got)
	}
	if !cardBarFill(track, 1, 0).Empty() || !cardBarFill(track, 1, -2).Empty() {
		t.Fatal("ratio <= 0 deveria devolver preenchimento vazio")
	}

	// Em 150% de DPI o inset acompanha a escala (senão o aro fica fino demais).
	scaledTrack := winui.Rect{X: 36, Y: 300, W: 768, H: winui.ScalePx(cardBarHeight, 1.5)}
	inset := winui.ScalePx(cardBarInset, 1.5)
	if inset < 2 {
		t.Fatalf("inset em 150%% = %d, esperado >= 2", inset)
	}
	scaled := cardBarFill(scaledTrack, 1.5, 0.25)
	if got, want := scaled.W, float64(768-2*inset)*0.25; math.Abs(got-want) > 1e-9 {
		t.Fatalf("largura em 150%% = %v, esperado %v", got, want)
	}
}

// TestCardBarFillSmoothMotion prova numericamente a suavidade: em quadros de
// 16ms o maior salto da borda é a fração de pixel esperada (e a maioria das
// larguras cai ENTRE pixels, o que só é desenhável com geometria fracionária).
func TestCardBarFillSmoothMotion(t *testing.T) {
	track := winui.Rect{X: 24, Y: 200, W: 512, H: cardBarHeight}
	fullWidth := float64(track.W - 2*cardBarInset)
	step := 16 * time.Millisecond

	for _, countdown := range []time.Duration{60 * time.Second, 15 * time.Second} {
		frames := int(countdown / step)
		prev := cardBarFill(track, 1, 1).W
		maxDelta := 0.0
		fractionalFrames := 0
		for i := 1; i <= frames; i++ {
			ratio := 1 - float64(i)*float64(step)/float64(countdown)
			width := cardBarFill(track, 1, ratio).W
			delta := prev - width
			if delta < -1e-9 {
				t.Fatalf("contagem %v: barra cresceu no quadro %d (delta %v)", countdown, i, delta)
			}
			if delta > maxDelta {
				maxDelta = delta
			}
			if width != math.Trunc(width) {
				fractionalFrames++
			}
			prev = width
		}
		if want := fullWidth * float64(step) / float64(countdown); maxDelta > want*1.01 {
			t.Fatalf("contagem %v: maior salto/quadro = %.3f px, esperado <= %.3f", countdown, maxDelta, want)
		}
		if fractionalFrames < frames/2 {
			t.Fatalf("contagem %v: só %d de %d quadros com largura fracionária — movimento preso ao pixel", countdown, fractionalFrames, frames)
		}
	}
}

// cardBarArea: a faixa do repinte incremental cobre o trilho com folga para o
// antialiasing (é o que o WM_TIMER invalida a cada quadro).
func TestCardBarAreaCoversTrack(t *testing.T) {
	track := winui.Rect{X: 24, Y: 200, W: 512, H: cardBarHeight}
	area := cardBarArea(track, 1)
	if !area.ContainsRect(track) {
		t.Fatalf("área %+v não cobre o trilho %+v", area, track)
	}
	if want := track.Inflate(cardBarPad, cardBarPad); area != want {
		t.Fatalf("área = %+v, esperado %+v", area, want)
	}
	if scaled := cardBarArea(track, 1.5); !scaled.ContainsRect(track) {
		t.Fatalf("área escalada %+v não cobre o trilho", scaled)
	}
	// Trilho ausente (cartão sem contador) devolve área vazia: o chamador
	// interpreta como "nada a repintar".
	if !cardBarArea(winui.Rect{}, 1).Empty() {
		t.Fatal("trilho vazio deveria devolver área vazia")
	}
}

// A faixa repintada pela animação precisa cair sobre fundo LISO do cartão: o
// primeBarBase preenche essa região com theme.Card em vez de copiar o cartão.
// Se o layout mudar (algo desenhado perto da barra), este teste quebra antes
// de a animação restaurar o fundo errado.
func TestCardBarAreaOnFlatCardBackground(t *testing.T) {
	for _, scale := range []float64{1, 1.25, 1.5, 2} {
		l := cardLayoutFor(scale, 0, true, 3, 4)
		area := cardBarArea(l.progressTrack, scale)
		if area.Empty() {
			t.Fatalf("escala %v: área vazia", scale)
		}
		if area.Y <= l.messageBox.Bottom() {
			t.Fatalf("escala %v: faixa (%+v) invade a caixa de mensagem (bottom=%d)", scale, area, l.messageBox.Bottom())
		}
		if area.Bottom() >= l.buttons[0].Y {
			t.Fatalf("escala %v: faixa (%+v) invade os botões (Y=%d)", scale, area, l.buttons[0].Y)
		}
		// Dentro do cartão e longe das bordas arredondadas (cantos) e da faixa
		// de accent da esquerda: só assim o fundo da faixa é liso.
		radius := winui.ScalePx(cardRadius, scale)
		if area.X <= winui.ScalePx(8, scale) || area.Right() > winui.ScalePx(cardWidth, scale)-radius {
			t.Fatalf("escala %v: faixa (%+v) encosta na borda/accent do cartão", scale, area)
		}
		if area.Y < radius || area.Bottom() > cardHeightFor(l, scale)-radius {
			t.Fatalf("escala %v: faixa (%+v) cruza um canto arredondado", scale, area)
		}
	}
}

// Layout: a barra é mais alta que a antiga (7px), fica abaixo do relógio e os
// botões continuam depois dela, dentro das margens do cartão.
func TestCardLayoutCountdownBar(t *testing.T) {
	l := cardLayoutFor(1, 0, true, 2, 0)
	if l.progressTrack.H != cardBarHeight {
		t.Fatalf("altura da barra = %d, esperado %d", l.progressTrack.H, cardBarHeight)
	}
	if l.progressTrack.Y <= l.countValue.Y {
		t.Fatalf("barra deveria ficar abaixo do relógio: barra.Y=%d relogio.Y=%d", l.progressTrack.Y, l.countValue.Y)
	}
	if l.buttons[0].Y <= l.progressTrack.Bottom() {
		t.Fatalf("botões deveriam ficar abaixo da barra: botao.Y=%d barra.Bottom=%d", l.buttons[0].Y, l.progressTrack.Bottom())
	}
	if l.progressTrack.X < cardPad || l.progressTrack.Right() > cardWidth-cardPad {
		t.Fatalf("barra fora das margens do cartão: %+v", l.progressTrack)
	}

	scaled := cardLayoutFor(1.5, 0, true, 2, 0)
	if scaled.progressTrack.H != winui.ScalePx(cardBarHeight, 1.5) {
		t.Fatalf("altura escalada = %d, esperado %d", scaled.progressTrack.H, winui.ScalePx(cardBarHeight, 1.5))
	}
	limit := winui.ScalePx(cardWidth, 1.5) - winui.ScalePx(cardPad, 1.5)
	if scaled.progressTrack.Right() > limit {
		t.Fatalf("barra escalada estourou a margem: %+v", scaled.progressTrack)
	}
}

// O timer da contagem precisa ser rápido o bastante para parecer contínuo
// (a 5 fps — 200 ms — o salto por quadro era visível, principalmente em
// contagens curtas).
func TestCardAnimTickIsHighFrequency(t *testing.T) {
	if cardAnimTickMS <= 0 || cardAnimTickMS > 33 {
		t.Fatalf("cardAnimTickMS = %d ms, esperado <= 33 ms", cardAnimTickMS)
	}
}
