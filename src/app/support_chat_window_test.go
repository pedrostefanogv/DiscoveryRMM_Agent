package app

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Guarda de regressão do PAINEL do chat de suporte.
//
// REQUISITO: a janela do chat é um painel de tamanho fixo. Antes ela abria como
// uma janela comum — com botão de fechar (que encerrava a conversa por um
// caminho diferente do botão "Encerrar") e com maximizar/redimensionar, o que
// deixava o chat ocupar a tela inteira. Agora o fechamento é consequência de
// encerrar a conversa e o tamanho é travado.
func TestSupportChatWindowOptions_PainelFixo(t *testing.T) {
	opts := supportChatWindowOptions(supportChatWindowWidth, supportChatWindowHeight)

	if !opts.DisableResize {
		t.Fatal("a janela do chat não pode ser redimensionável (DisableResize)")
	}
	if opts.CloseButtonState != application.ButtonHidden {
		t.Fatalf("a janela do chat não deve ter botão de fechar; esperado ButtonHidden, obtido %v", opts.CloseButtonState)
	}
	if opts.MaximiseButtonState == application.ButtonEnabled {
		t.Fatalf("a janela do chat não pode ser maximizável; obtido %v", opts.MaximiseButtonState)
	}
	if opts.Width != supportChatWindowWidth || opts.Height != supportChatWindowHeight {
		t.Fatalf("tamanho inicial inesperado: %dx%d (esperado %dx%d)",
			opts.Width, opts.Height, supportChatWindowWidth, supportChatWindowHeight)
	}
	// Teto explícito: nenhum caminho (duplo clique no título, atalho do sistema)
	// pode ampliar a janela além do tamanho com que ela nasceu.
	if opts.MaxWidth != opts.Width || opts.MaxHeight != opts.Height {
		t.Fatalf("a janela do chat precisa ter teto %dx%d; obtido %dx%d",
			opts.Width, opts.Height, opts.MaxWidth, opts.MaxHeight)
	}
	// Piso igual ao teto: a janela não encolhe nem cresce.
	if opts.MinWidth != opts.Width || opts.MinHeight != opts.Height {
		t.Fatalf("a janela do chat precisa ter piso %dx%d; obtido %dx%d",
			opts.Width, opts.Height, opts.MinWidth, opts.MinHeight)
	}
	if opts.URL != "/chat-window.html" {
		t.Fatalf("URL inesperada: %q", opts.URL)
	}
	if opts.Name != supportChatWindowName {
		t.Fatalf("nome inesperado: %q", opts.Name)
	}
}

// REQUISITO: a janela fixa NÃO pode nascer maior que a área de trabalho. Como
// ela não pode ser redimensionada nem maximizada, uma tela pequena (ou escala
// de DPI alta) deixaria o rodapé do chat — campo de texto e "Enviar" — fora da
// tela, sem nenhum caminho de volta.
func TestSupportChatWindowSize_CabeNaAreaDeTrabalho(t *testing.T) {
	// Tela grande: tamanho nominal.
	big := &application.Screen{WorkArea: application.Rect{Width: 1920, Height: 1040}, ScaleFactor: 1}
	if w, h := supportChatWindowSize(big); w != supportChatWindowWidth || h != supportChatWindowHeight {
		t.Fatalf("tela grande deve manter o tamanho nominal; obtido %dx%d", w, h)
	}

	// 1080p a 150%: área de trabalho lógica ~1280x688 (menor que os 680 de altura).
	scaled := &application.Screen{WorkArea: application.Rect{Width: 1280, Height: 688}, ScaleFactor: 1.5}
	w, h := supportChatWindowSize(scaled)
	if h != 688-2*windowFitMargin {
		t.Fatalf("altura deveria ser limitada a %d; obtido %d", 688-2*windowFitMargin, h)
	}
	if w != supportChatWindowWidth {
		t.Fatalf("largura não deveria mudar em 1280 de área útil; obtido %d", w)
	}
	if h < 200 || w < 200 {
		t.Fatalf("clamp absurdo: %dx%d", w, h)
	}

	// Tela minúscula: nada de dimensão negativa ou zero.
	tiny := &application.Screen{WorkArea: application.Rect{Width: 40, Height: 30}, ScaleFactor: 2}
	w, h = supportChatWindowSize(tiny)
	if w <= 0 || h <= 0 {
		t.Fatalf("clamp não pode zerar/negativar o tamanho; obtido %dx%d", w, h)
	}

	// Sem ScreenManager (startup) ou work area inválida: tamanho nominal.
	if w, h = supportChatWindowSize(nil); w != supportChatWindowWidth || h != supportChatWindowHeight {
		t.Fatalf("sem screen deve manter o tamanho nominal; obtido %dx%d", w, h)
	}
	empty := &application.Screen{WorkArea: application.Rect{}}
	if w, h = supportChatWindowSize(empty); w != supportChatWindowWidth || h != supportChatWindowHeight {
		t.Fatalf("work area inválida deve manter o tamanho nominal; obtido %dx%d", w, h)
	}
}

// REQUISITO (definido pelo usuário em produção): o convite do suporte precisa
// aparecer POR CIMA de todos os aplicativos. A janela do chat flutua
// ("sempre no topo") apenas enquanto o convite está pendente de resposta;
// durante a conversa ela volta ao comportamento normal, porque uma janela
// sempre no topo atrapalharia o trabalho (e cobriria o app que o suporte
// precisa ver).
func TestSupportChatShouldFloat_ApenasDuranteOConvite(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"waiting", true},
		{" active ", false},
		{"active", false},
		{"ended", false},
		{"none", false},
		{"", false},
		{"desconhecido", false},
	}

	for _, tc := range cases {
		if got := supportChatShouldFloat(tc.status); got != tc.want {
			t.Fatalf("supportChatShouldFloat(%q) = %v; esperado %v", tc.status, got, tc.want)
		}
	}
}

// REQUISITO: o painel do chat nasce no monitor onde o usuário está trabalhando
// (cursor) — com dois monitores o convite podia nascer no monitor "errado" e
// passar despercebido.
func TestSupportChatPlacementScreen_PrefereCursor(t *testing.T) {
	primary := &application.Screen{
		IsPrimary:      true,
		Bounds:         application.Rect{X: 0, Y: 0, Width: 1920, Height: 1080},
		PhysicalBounds: application.Rect{X: 0, Y: 0, Width: 1920, Height: 1080},
	}
	secondary := &application.Screen{
		Bounds:         application.Rect{X: 1920, Y: 0, Width: 1280, Height: 1024},
		PhysicalBounds: application.Rect{X: 1920, Y: 0, Width: 1280, Height: 1024},
	}
	screens := []*application.Screen{primary, secondary}

	// Cursor no segundo monitor: a janela nasce lá.
	cursor := application.Point{X: 2400, Y: 400}
	if got := supportChatPlacementScreen(screens, &cursor, primary); got != secondary {
		t.Fatalf("cursor no monitor secundário deveria escolher o secundário; obtido %+v", got)
	}

	// Cursor no monitor primário.
	cursor = application.Point{X: 100, Y: 100}
	if got := supportChatPlacementScreen(screens, &cursor, secondary); got != primary {
		t.Fatalf("cursor no monitor primário deveria escolher o primário; obtido %+v", got)
	}

	// Sem cursor (GetCursorPos falhou): usa a tela preferida (janela principal).
	if got := supportChatPlacementScreen(screens, nil, secondary); got != secondary {
		t.Fatalf("sem cursor deveria usar a tela preferida; obtido %+v", got)
	}

	// Sem cursor e sem preferida: primária.
	if got := supportChatPlacementScreen(screens, nil, nil); got != primary {
		t.Fatalf("sem cursor/preferida deveria usar a primária; obtido %+v", got)
	}

	// Cursor fora de qualquer tela (config estranha): cai na preferida.
	cursor = application.Point{X: -5000, Y: -5000}
	if got := supportChatPlacementScreen(screens, &cursor, primary); got != primary {
		t.Fatalf("cursor fora das telas deveria cair na preferida; obtido %+v", got)
	}

	// Nenhuma tela conhecida: nil (o chamador mantém o comportamento padrão).
	if got := supportChatPlacementScreen(nil, &cursor, nil); got != nil {
		t.Fatalf("sem telas o resultado deve ser nil; obtido %+v", got)
	}
}

// REQUISITO: além de escolher a tela, a janela precisa NASCER já centralizada
// nela e já "sempre no topo" quando é o convite. Criar sem AlwaysOnTop deixava
// um intervalo em que ela aparecia atrás do app em uso.
func TestSupportChatWindowOptionsOn_PosicionaECentraliza(t *testing.T) {
	screen := &application.Screen{
		IsPrimary: true,
		WorkArea:  application.Rect{X: 1920, Y: 0, Width: 1280, Height: 1024},
	}

	opts := supportChatWindowOptionsOn(500, 600, screen, true)

	if !opts.AlwaysOnTop {
		t.Fatal("o convite precisa nascer sempre no topo (AlwaysOnTop)")
	}
	if opts.Screen != screen {
		t.Fatal("a tela escolhida precisa ir nas opções (Screen)")
	}
	if opts.InitialPosition != application.WindowXY {
		t.Fatalf("posição inicial deveria ser WindowXY; obtido %v", opts.InitialPosition)
	}
	// X/Y relativos à WorkArea da tela escolhida.
	if wantX := (1280 - 500) / 2; opts.X != wantX {
		t.Fatalf("X deveria ser %d; obtido %d", wantX, opts.X)
	}
	if wantY := (1024 - 600) / 2; opts.Y != wantY {
		t.Fatalf("Y deveria ser %d; obtido %d", wantY, opts.Y)
	}

	// Conversa normal (sem convite): janela sem AlwaysOnTop.
	if opts := supportChatWindowOptionsOn(500, 600, screen, false); opts.AlwaysOnTop {
		t.Fatal("sem convite pendente a janela não deve ser AlwaysOnTop")
	}

	// Sem tela (ScreenManager indisponível): mantém as opções do painel.
	base := supportChatWindowOptions(500, 600)
	if opts := supportChatWindowOptionsOn(500, 600, nil, true); opts.Screen != nil ||
		opts.Width != base.Width || opts.Height != base.Height || !opts.DisableResize {
		t.Fatal("sem tela as opções do painel precisam ser preservadas")
	}

	// Área de trabalho inválida: não posiciona (nem gera X/Y negativo).
	empty := &application.Screen{WorkArea: application.Rect{}}
	if opts := supportChatWindowOptionsOn(500, 600, empty, false); opts.Screen != nil || opts.X != 0 || opts.Y != 0 {
		t.Fatalf("work area inválida não deve posicionar a janela; obtido X=%d Y=%d", opts.X, opts.Y)
	}

	// Painel maior que a área de trabalho: X/Y nunca negativos.
	tiny := &application.Screen{WorkArea: application.Rect{Width: 200, Height: 200}}
	if opts := supportChatWindowOptionsOn(500, 600, tiny, false); opts.X < 0 || opts.Y < 0 {
		t.Fatalf("X/Y não podem ser negativos; obtido X=%d Y=%d", opts.X, opts.Y)
	}
}
