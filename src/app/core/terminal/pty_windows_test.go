//go:build windows

package terminal

import (
	"strings"
	"testing"
)

// TestNormalizeToUtf8OemPingBytes reproduz o mojibake real do ping.exe
// (bug 2026-09-20): nativos em pipe emitem OEM bytes — \xa1=í, \xa0=á,
// \x82=é (mesmos offsets em CP437 e CP850). Decodificar como CP1252 (código
// antigo) produzia "Estat¡sticas"/"M¡nimo"/"M ximo"/"M‚dia".
func TestNormalizeToUtf8OemPingBytes(t *testing.T) {
	in := []byte("Estat\xa1sticas do Ping\r\nM\xa1nimo = 0ms, M\xa0ximo = 0ms, M\x82dia = 0ms")
	out := normalizeToUtf8(in)
	for _, want := range []string{"Estat\u00edsticas", "M\u00ednimo", "M\u00e1ximo", "M\u00e9dia"} {
		if !contains(out, want) {
			t.Errorf("normalizeToUtf8 OEM: %q n\u00e3o cont\u00e9m %q", out, want)
		}
	}
}

// Trecho j\u00e1 UTF-8 v\u00e1lido deve passar intacto (caminho do wrapper UTF-8).
func TestNormalizeToUtf8Passthrough(t *testing.T) {
	in := []byte("Estat\u00edsticas n\u00e3o \u2014 UTF-8 v\u00e1lido")
	if got := normalizeToUtf8(in); got != string(in) {
		t.Errorf("passthrough: got %q want %q", got, string(in))
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }