package notifyui

import (
	"testing"
	"time"
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
				Title:    "Discovery Agent",
				Subtitle: "Discovery Agent",
				Message:  " ",
				Buttons:  []Button{{ID: "ok", Label: "OK", Kind: "primary"}},
				DefaultButtonID: "ok",
				CountdownLabel: "",
				Hint: "Enter confirma · Esc fecha",
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
				Title:    "Teste",
				Subtitle: "Discovery Agent",
				Message:  " ",
				Buttons:  []Button{{ID: "cancel", Label: "Cancelar"}, {ID: "go", Label: "Ir"}},
				DefaultButtonID: "go",
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
	plain := cardLayoutFor(1, 0, false, 2)
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

	withCount := cardLayoutFor(1, 0, true, 2)
	if withCount.buttons[0].Y <= plain.buttons[0].Y {
		t.Fatalf("contador deveria empurrar os botões para baixo: %d <= %d", withCount.buttons[0].Y, plain.buttons[0].Y)
	}
	if withCount.progressTrack.W != cardWidth-2*cardPad {
		t.Fatalf("largura da barra = %d, want %d", withCount.progressTrack.W, cardWidth-2*cardPad)
	}

	// Mensagem longa cresce o cartão.
	grown := cardLayoutFor(1, 40, false, 1)
	if grown.messageBox.H != 84+40 {
		t.Fatalf("caixa de mensagem = %d, want %d", grown.messageBox.H, 84+40)
	}

	// Escala DPI cresce tudo proporcionalmente.
	scaled := cardLayoutFor(2, 0, false, 1)
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
