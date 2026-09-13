package terminal

import "testing"

func TestUtf8IncompleteTail(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"vazio", "", 0},
		{"ascii completo", "abc", 0},
		{"runa 2 bytes completa (é)", "x\xc3\xa9", 0},
		{"runa 3 bytes completa", "ab\xe7\x83\x8d", 0},
		{"runa 4 bytes completa (emoji)", "\xf0\x9f\x9a\x80", 0},
		{"1 byte de runa 2", "x\xc3", 1},
		{"1 byte de runa 3", "ab\xe7", 1},
		{"2 bytes de runa 4 (emoji cortada)", "\xf0\x9f", 2},
		{"3 bytes de runa 4 (emoji cortada)", "\xf0\x9f\x9a", 3},
		{"texto + 1 byte cortado", "Estat\xc3", 1},
		{"texto com acento completo", "Estat\xc3\xadsticas", 0},
		{"continuação órfã passa direto", "ab\x80", 0},
		{"lead inválido passa direto", "ab\xff", 0},
	}
	for _, c := range cases {
		got := Utf8IncompleteTail([]byte(c.in))
		if got != c.want {
			t.Errorf("%s: Utf8IncompleteTail(%q) = %d; want %d", c.name, c.in, got, c.want)
		}
	}
}

// Split de runa no limite do chunk (cenário real do coalescer): o ConPTY manda
// "Estat\xc3" e depois "\xadsticas" ("í" = \xc3\xad dividida). O chunk 1 retém 1
// byte (o lead); no próximo flush o carry + chunk 2 formam a runa completa.
func TestUtf8IncompleteTailCarry(t *testing.T) {
	chunk1 := []byte("Estat\xc3")
	if got := Utf8IncompleteTail(chunk1); got != 1 {
		t.Fatalf("tail(chunk1) = %d; want 1", got)
	}
	carry := chunk1[len(chunk1)-1:]           // [0xC3] retido
	chunk2 := []byte("\xadsticas")
	joined := append(append([]byte(nil), carry...), chunk2...) // [0xC3, 0xAD, 's'...]
	if got := Utf8IncompleteTail(joined); got != 0 {
		t.Fatalf("tail(joined) = %d; want 0", got)
	}
	if string(joined) != "\xc3\xadsticas" {
		t.Fatalf("joined = %q; want \xc3\xadsticas (ísticas)", string(joined))
	}
}
