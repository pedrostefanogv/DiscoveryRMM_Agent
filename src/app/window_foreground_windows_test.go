//go:build windows

package app

import "testing"

// FIX 20/10 (20:37): AttachThreadInput numa thread de OUTRO processo travada
// (janela "Não está respondendo") bloqueia a NOSSA thread para sempre — a
// interface inteira congelava no instante em que a janela do chat tentava
// subir. O guard pula o attach quando a thread de primeiro plano está hung.
func TestShouldAttachForegroundInput(t *testing.T) {
	self := uint32(4242)

	casos := []struct {
		nome     string
		fgPid    uint32
		selfPid  uint32
		hung     bool
		esperado bool
	}{
		{"sem primeiro plano: nada a anexar", 0, self, false, false},
		{"primeiro plano é nosso (outra thread): anexa", self, self, false, true},
		{"primeiro plano é nosso mesmo hung: anexa", self, self, true, true},
		{"estrangeira respondendo: anexa", 1111, self, false, true},
		{"estrangeira TRAVADA: pula (evita travar nossa thread)", 1111, self, true, false},
	}
	for _, c := range casos {
		if got := shouldAttachForegroundInput(c.fgPid, c.selfPid, c.hung); got != c.esperado {
			t.Fatalf("%s: esperado %v, obtido %v", c.nome, c.esperado, got)
		}
	}
}

// Com a thread estrangeira travada o fallback (topmost/não-topmost + flash)
// continua sendo executado — o teste garante a DECISÃO, não o fallback em si
// (que é exercitado no Windows real).
