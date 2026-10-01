package screenshot

import "testing"

func TestRectOverlapArea(t *testing.T) {
	cases := []struct {
		name           string
		ax, ay, aw, ah int
		bx, by, bw, bh int
		want           int
	}{
		{"sem sobreposicao", 0, 0, 100, 100, 200, 200, 50, 50, 0},
		{"encoste vertical (1px)", 0, 0, 100, 100, 100, 0, 50, 50, 0},
		{"parcial", 0, 0, 100, 100, 50, 50, 100, 100, 2500},
		{"contido", 0, 0, 100, 100, 10, 10, 20, 20, 400},
		{"cobre a janela inteira", 0, 0, 100, 100, 25, 25, 50, 50, 2500},
		{"dimensao invalida", 0, 0, 0, 100, 0, 0, 100, 100, 0},
		{"coordenadas negativas", -100, -100, 200, 200, -50, -50, 10, 10, 100},
	}
	for _, c := range cases {
		if got := RectOverlapArea(c.ax, c.ay, c.aw, c.ah, c.bx, c.by, c.bw, c.bh); got != c.want {
			t.Errorf("%s: RectOverlapArea = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestOverlapsBlockedWindow(t *testing.T) {
	// Janela bloqueada grande: 1000x800 em (200,100).
	win := WindowInfo{X: 200, Y: 100, Width: 1000, Height: 800, Blocked: true}
	cases := []struct {
		name string
		sel  Selection
		want bool
	}{
		{"area contida na janela", Selection{X: 300, Y: 200, Width: 200, Height: 200}, true},
		{"contato de borda irrelevante", Selection{X: 100, Y: 100, Width: 101, Height: 101}, false},
		{"fatia pequena de janela grande (acima de 1%)", Selection{X: 200, Y: 100, Width: 40, Height: 800}, true},
		{"totalmente fora", Selection{X: 5000, Y: 5000, Width: 100, Height: 100}, false},
		{"cobre tudo", Selection{X: 0, Y: 0, Width: 4000, Height: 3000}, true},
	}
	for _, c := range cases {
		if got := OverlapsBlockedWindow(c.sel, win); got != c.want {
			t.Errorf("%s: OverlapsBlockedWindow = %v, want %v", c.name, got, c.want)
		}
	}

	// Janela pequena (20x20): a tolerância mínima de 400 px² impede que um
	// encoste irrelevante bloqueie a captura, mas a cobertura total bloqueia.
	small := WindowInfo{X: 0, Y: 0, Width: 20, Height: 20, Blocked: true}
	if OverlapsBlockedWindow(Selection{X: 19, Y: 19, Width: 5, Height: 5}, small) {
		t.Errorf("encoste de 1x1 px nao deveria bloquear")
	}
	if !OverlapsBlockedWindow(Selection{X: 0, Y: 0, Width: 20, Height: 20}, small) {
		t.Errorf("janela pequena totalmente coberta deveria bloquear")
	}

	// Janela gigante (8000x4000): o limite satura em 16 000 px², então uma faixa
	// de 200x100 px (20 000 px²) de um app bloqueado continua bloqueando.
	huge := WindowInfo{X: 0, Y: 0, Width: 8000, Height: 4000, Blocked: true}
	if !OverlapsBlockedWindow(Selection{X: 100, Y: 100, Width: 200, Height: 100}, huge) {
		t.Errorf("faixa de 20 000 px² em janela gigante deveria bloquear")
	}
	if OverlapsBlockedWindow(Selection{X: 100, Y: 100, Width: 100, Height: 3}, huge) {
		t.Errorf("encoste de 300 px² nao deveria bloquear")
	}
}

func TestFirstBlockedOverlap(t *testing.T) {
	policy := Policy{BlockedProcesses: []string{"keepass"}}
	blocked := WindowInfo{Handle: 7, ProcessName: "KeePassXC.exe", X: 100, Y: 100, Width: 400, Height: 300}
	marked := WindowInfo{Handle: 8, ProcessName: "banco.exe", X: 600, Y: 100, Width: 400, Height: 300, Blocked: true}
	self := WindowInfo{Handle: 9, ProcessName: "keepass-helper.exe", X: 100, Y: 100, Width: 400, Height: 300, IsSelf: true}
	minimized := WindowInfo{Handle: 10, ProcessName: "keepass-helper.exe", X: 100, Y: 100, Width: 400, Height: 300, Minimized: true}
	empty := WindowInfo{Handle: 11, ProcessName: "keepass-helper.exe"}
	free := WindowInfo{Handle: 12, ProcessName: "notepad.exe", X: 100, Y: 600, Width: 800, Height: 600}
	wins := []WindowInfo{self, minimized, empty, free, blocked, marked}

	// Seleção só sobre a janela livre (y ≥ 600): nada bloqueado é encoberto.
	if hit, ok := FirstBlockedOverlap(policy, wins, Selection{X: 200, Y: 700, Width: 200, Height: 200}); ok {
		t.Fatalf("selecao sobre janela livre nao deveria bloquear (hit=%q)", hit.ProcessName)
	}
	// Seleção cobrindo a janela da blocklist: bloqueia pelo nome do processo.
	if hit, ok := FirstBlockedOverlap(policy, wins, Selection{X: 200, Y: 200, Width: 100, Height: 100}); !ok || hit.Handle != 7 {
		t.Fatalf("selecao sobre keepass deveria bloquear (ok=%v hit=%d)", ok, hit.Handle)
	}
	// Seleção sobre a janela marcada com Blocked=true (política vazia).
	if hit, ok := FirstBlockedOverlap(Policy{}, wins, Selection{X: 700, Y: 200, Width: 100, Height: 100}); !ok || hit.Handle != 8 {
		t.Fatalf("flag Blocked deveria bloquear (ok=%v hit=%d)", ok, hit.Handle)
	}
	// Janela do próprio agente, minimizada e sem dimensão nunca bloqueiam,
	// mesmo quando a seleção cobre exatamente a área delas.
	sel := Selection{X: 100, Y: 100, Width: 400, Height: 300}
	selfPolicy := Policy{BlockedProcesses: []string{"keepass-helper"}}
	if hit, ok := FirstBlockedOverlap(selfPolicy, []WindowInfo{self}, sel); ok {
		t.Fatalf("janela do proprio agente nao deveria bloquear (hit=%q)", hit.ProcessName)
	}
	if hit, ok := FirstBlockedOverlap(selfPolicy, []WindowInfo{minimized}, sel); ok {
		t.Fatalf("janela minimizada nao deveria bloquear (hit=%q)", hit.ProcessName)
	}
	if hit, ok := FirstBlockedOverlap(selfPolicy, []WindowInfo{empty}, sel); ok {
		t.Fatalf("janela sem dimensao nao deveria bloquear (hit=%q)", hit.ProcessName)
	}
}
