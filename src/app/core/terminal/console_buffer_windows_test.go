//go:build windows

package terminal

import (
	"strings"
	"testing"
)

func TestAttrToSGR(t *testing.T) {
	cases := []struct {
		attr uint16
		want string
	}{
		// Default DOS (cinza sobre preto) → branco ANSI sobre preto.
		{0x0007, "\x1b[37;40m"},
		// Foreground vermelho (DOS 4) sobre preto.
		{0x0004, "\x1b[31;40m"},
		// Foreground vermelho INTENSO (DOS 0xC = bright) → ANSI 91.
		{0x000C, "\x1b[91;40m"},
		// Background vermelho (DOS 0x40).
		{0x0044, "\x1b[31;41m"},
		// Background azul INTENSO (DOS 0x90 → nibble 9, bright) → ANSI 104.
		{0x0094, "\x1b[31;104m"},
	}
	for _, c := range cases {
		if got := attrToSGR(c.attr); got != c.want {
			t.Fatalf("attrToSGR(0x%04X) = %q, want %q", c.attr, got, c.want)
		}
	}
}

func TestRenderFrameRowTrimAndCup(t *testing.T) {
	// Linha vazia (só células default) → nada a emitir.
	empty := make([]cbCell, 10)
	var b strings.Builder
	renderFrameRow(&b, empty, 0, 3)
	if b.Len() != 0 {
		t.Fatalf("linha vazia não deveria emitir nada: %q", b.String())
	}

	// Linha com texto: CUP na coluna do primeiro caractere + texto.
	row := make([]cbCell, 10)
	copy(row, []cbCell{
		{ch: ' ', attr: 0x0007}, {ch: ' ', attr: 0x0007},
		{ch: 'O', attr: 0x0007}, {ch: 'K', attr: 0x0007},
		{ch: ' ', attr: 0x0007}, {ch: ' ', attr: 0x0007},
		{ch: ' ', attr: 0x0007}, {ch: ' ', attr: 0x0007},
		{ch: ' ', attr: 0x0007}, {ch: ' ', attr: 0x0007},
	})
	b.Reset()
	renderFrameRow(&b, row, 0, 3)
	out := b.String()
	if !strings.HasPrefix(out, "\x1b[4;3H") {
		t.Fatalf("CUP esperado em linha 4 col 3: %q", out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("texto esperado ausente: %q", out)
	}
	if strings.Contains(out, "   ") {
		t.Fatalf("trailing spaces deveriam ser cortados: %q", out)
	}
}

func TestMatchVTSequence(t *testing.T) {
	cases := []struct {
		seq  string
		want uint16
		ok   bool
	}{
		{"\x1b[A", cbVKUp, true},
		{"\x1bOA", cbVKUp, true}, // forma de aplicação do PSReadLine
		{"\x1b[B", cbVKDown, true},
		{"\x1b[C", cbVKRight, true},
		{"\x1b[D", cbVKLeft, true},
		{"\x1b[H", cbVKHome, true},
		{"\x1b[3~", cbVKDelete, true},
		{"\x1b[Z", 0, false},          // não suportada → ESC puro
		{"\x1b", 0, false},            // ESC solto
	}
	for _, c := range cases {
		runes := []rune(c.seq)
		i := 0
		got, ok := matchVTSequence(runes, &i)
		if ok != c.ok {
			t.Fatalf("matchVTSequence(%q) ok=%v, want %v", c.seq, ok, c.ok)
		}
		if ok && got != c.want {
			t.Fatalf("matchVTSequence(%q) vk=0x%X, want 0x%X", c.seq, got, c.want)
		}
	}
}

func TestTranslateKeysSpecials(t *testing.T) {
	recs, drops := translateKeysToRecords("\x1b[A\r\x03")
	if drops != 0 {
		t.Fatalf("drops inesperados: %d", drops)
	}
	if len(recs) < 6 {
		t.Fatalf("esperava >= 6 eventos (seta, enter, ctrl+c com down+up), veio %d", len(recs))
	}
	// Seta para cima: VK 0x26.
	if recs[0].KeyEvent.WVirtualKeyCode != cbVKUp {
		t.Fatalf("primeira tecla deveria ser VK_UP (0x26), veio 0x%X", recs[0].KeyEvent.WVirtualKeyCode)
	}
	if recs[0].KeyEvent.BKeyDown != 1 {
		t.Fatalf("primeiro evento deveria ser keyDown")
	}
	// Enter: VK_RETURN 0x0D (após down+up da seta).
	if recs[2].KeyEvent.WVirtualKeyCode != cbVKReturn {
		t.Fatalf("terceira tecla deveria ser VK_RETURN (0x0D), veio 0x%X", recs[2].KeyEvent.WVirtualKeyCode)
	}
	// Ctrl+C: 'C' (0x43) com LEFT_CTRL.
	if recs[4].KeyEvent.WVirtualKeyCode != 'C' {
		t.Fatalf("quinta tecla deveria ser 'C', veio 0x%X", recs[4].KeyEvent.WVirtualKeyCode)
	}
	if recs[4].KeyEvent.DwControlKeyState&cbLeftCtrlPressed == 0 {
		t.Fatalf("Ctrl+C deveria carregar LEFT_CTRL_PRESSED")
	}
	// Todos os eventos em pares down/up.
	for i := 0; i < len(recs); i += 2 {
		if recs[i].KeyEvent.BKeyDown != 1 || recs[i+1].KeyEvent.BKeyDown != 0 {
			t.Fatalf("par down/up quebrado no índice %d", i)
		}
	}
}
