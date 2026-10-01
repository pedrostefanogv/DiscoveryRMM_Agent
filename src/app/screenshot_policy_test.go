package app

import (
	"strings"
	"testing"

	"discovery/app/core/screenshot"
)

func boolPtrPolicyTest(v bool) *bool { return &v }

// TestScreenshotSelectionForRequestModes garante que a checagem geométrica da
// blocklist recebe o retângulo certo em cada modo automático da IA.
func TestScreenshotSelectionForRequestModes(t *testing.T) {
	sel, ok := screenshotSelectionForRequest(screenshot.Request{Mode: "region", X: 10, Y: 20, Width: 100, Height: 50})
	if !ok || sel.X != 10 || sel.Y != 20 || sel.Width != 100 || sel.Height != 50 {
		t.Fatalf("region = %+v ok=%v", sel, ok)
	}
	if _, ok := screenshotSelectionForRequest(screenshot.Request{Mode: "region", Width: 0, Height: 50}); ok {
		t.Fatal("region sem dimensoes nao deveria gerar selecao")
	}

	full, ok := screenshotSelectionForRequest(screenshot.Request{Mode: "full"})
	if !ok || full.Width <= 0 || full.Height <= 0 {
		t.Fatalf("full deveria cobrir o desktop virtual: %+v ok=%v", full, ok)
	}
	for _, m := range screenshot.ListMonitors() {
		if m.X < full.X || m.Y < full.Y || m.X+m.Width > full.X+full.Width || m.Y+m.Height > full.Y+full.Height {
			t.Fatalf("monitor %d fora do retangulo de tela inteira: %+v vs %+v", m.Index, m, full)
		}
	}

	// Índice de monitor fora da faixa é CLAMPADO para o primário (mesma regra do
	// captureMonitorFormat), e não ignorado: antes disso a checagem da blocklist
	// era pulada e a captura ainda acontecia — só que no monitor primário, com o
	// usuário autorizando "monitor 10000".
	mons := screenshot.ListMonitors()
	if len(mons) > 0 {
		clamped, ok := screenshotSelectionForRequest(screenshot.Request{Mode: "monitor", MonitorIndex: 9999})
		if !ok {
			t.Fatal("monitor fora da faixa deveria ser clampado para o primario")
		}
		if clamped.X != mons[0].X || clamped.Y != mons[0].Y || clamped.Width != mons[0].Width || clamped.Height != mons[0].Height {
			t.Fatalf("clamp do monitor = %+v, want primario %+v", clamped, mons[0])
		}
		negative, ok := screenshotSelectionForRequest(screenshot.Request{Mode: "monitor", MonitorIndex: -3})
		if !ok || negative.X != mons[0].X {
			t.Fatalf("indice negativo deveria cair no primario: %+v ok=%v", negative, ok)
		}
		valid, ok := screenshotSelectionForRequest(screenshot.Request{Mode: "monitor", MonitorIndex: len(mons) - 1})
		if !ok || valid.Width != mons[len(mons)-1].Width {
			t.Fatalf("ultimo monitor valido = %+v ok=%v", valid, ok)
		}
	}
	if _, ok := screenshotSelectionForRequest(screenshot.Request{Mode: "window", WindowHandle: 1}); ok {
		t.Fatal("modo janela nao usa selecao geometrica")
	}
}

// TestApplyScreenshotPolicyBlocksRegionOverBlockedWindow cobre o buraco que
// existia: a blocklist só era aplicada a mode=window, então a IA podia pedir
// region/tela inteira e fotografar um app bloqueado sem passar pelo overlay.
func TestApplyScreenshotPolicyBlocksRegionOverBlockedWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("enumera janelas reais")
	}
	wins, err := screenshot.ListWindowsWithOptions(true)
	if err != nil || len(wins) == 0 {
		t.Skipf("nenhuma janela disponivel: %v", err)
	}
	var target *screenshot.WindowInfo
	for i := range wins {
		if !wins[i].IsSelf && !wins[i].Minimized && wins[i].Width > 50 && wins[i].Height > 50 && strings.TrimSpace(wins[i].ProcessName) != "" {
			target = &wins[i]
			break
		}
	}
	if target == nil {
		t.Skip("nenhuma janela alvo elegivel")
	}
	app := &App{}
	inside := screenshot.Request{
		Mode:   "region",
		X:      target.X + 10,
		Y:      target.Y + 10,
		Width:  60,
		Height: 60,
	}

	// Processo de destino na blocklist: a região que cobre a janela é recusada.
	blocked := screenshot.Policy{
		BlockedProcesses: []string{target.ProcessName},
		AllowFullScreen:  boolPtrPolicyTest(true),
	}
	req := inside
	if err := app.applyScreenshotPolicy(&req, blocked); err == nil {
		t.Fatalf("regiao sobre %q deveria ser bloqueada", target.ProcessName)
	} else if !strings.Contains(err.Error(), "privacidade") {
		t.Fatalf("erro inesperado: %v", err)
	}

	// Blocklist com processo inexistente: a mesma região passa.
	free := screenshot.Policy{
		BlockedProcesses: []string{"processo-que-nao-existe-zzz"},
		AllowFullScreen:  boolPtrPolicyTest(true),
	}
	req = inside
	if err := app.applyScreenshotPolicy(&req, free); err != nil {
		t.Fatalf("regiao sem processo bloqueado deveria passar: %v", err)
	}

	// requireWindow continua recusando região, com mensagem própria.
	strict := screenshot.Policy{RequireWindow: boolPtrPolicyTest(true)}
	req = inside
	if err := app.applyScreenshotPolicy(&req, strict); err == nil {
		t.Fatal("requireWindow deveria recusar region")
	}
}
