//go:build windows

package screen

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
)

// ── Prova de ponta a ponta: a MÁSCARA tira o cartão do frame capturado ──
//
// Contexto medido em 2026-11-03: o Desktop Duplication (DXGI/go-d3d) **ignora**
// WDA_EXCLUDEFROMCAPTURE — uma janela excluída continua inteira no frame do DDA.
// Por isso a exclusão da janela pelo Windows não basta nesta versão: o que tira
// o banner do visualizador é a máscara de região (capturada antes de o cartão
// aparecer e reescrita no frame pelo pipeline).
//
// Estes testes criam uma janela MAGENTA, capturam a tela pelo capturer real do
// pipeline e verificam:
//  1. controle: sem nenhum tratamento a janela APARECE no frame;
//  2. com a máscara ativa (mesmo caminho do banner) a área some do frame.

const (
	magentaTeste = 0x00FF00FF // RGB(255,0,255)
	testLargura  = 220
	testAltura   = 90
)

var (
	exclusionClassOnce sync.Once
	exclusionClassErr  error
)

func exclusionWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == winui.MsgEraseBkgnd || msg == winui.MsgPaint {
		hdc, paint := winui.BeginPaint(hwnd)
		if hdc != 0 {
			winui.FillRect(hdc, winui.Rect{X: 0, Y: 0, W: testLargura, H: testAltura}, winui.Color(magentaTeste))
		}
		winui.EndPaint(hwnd, &paint)
		return 0
	}
	return winui.DefWindowProc(hwnd, msg, wparam, lparam)
}

// janelaMagenta cria a janela de teste OCULTA (sem ShowWindow) e PRENDE a
// goroutine do teste a uma thread do SO.
//
// ── POR QUE O LockOSThread É OBRIGATÓRIO AQUI (flake real, 1 em 3 na suíte) ──
//
// Uma janela Win32 pertence à THREAD que a criou: só ela pode bombear as
// mensagens dela. CreateWindow roda na goroutine do teste, e uma goroutine do Go
// NÃO tem thread fixa — depois de um time.Sleep (ou de qualquer ponto de
// preempção) o runtime pode movê-la para outra thread do SO.
//
// Medido: com um sleep de 300ms entre a criação e o ShowWindow, o GetCurrentThreadId
// mudou de 25480 para 31012. O ShowWindow seguinte entrega WM_SHOWWINDOW/WM_PAINT
// de forma SINCRONA para a thread DONA da janela — que agora é outra e não está
// bombeando mensagens — e a chamada BLOQUEIA PARA SEMPRE.
//
// Era exatamente esse o flake: o teste do controle (sem sleep antes do ShowWindow)
// nunca travava; o TestMascaraRemoveCartaoDaCaptura (com sleep de 300ms para o DWM
// assentar) travava às vezes, conforme o runtime mantivesse ou não a goroutine na
// mesma thread. Um panic de timeout de 25s, não uma falha de asserção.
//
// LockOSThread no helper resolve na raiz para TODOS os testes que usam a janela:
// a goroutine fica na mesma thread do início ao fim, então ShowWindow/UpdateWindow/
// DestroyWindow sempre rodam na thread dona. O UnlockOSThread no Cleanup evita
// vazar a thread presa (o Go a descarta ao terminar a goroutine, mas devolvê-la
// explicitamente mantém o pool sadio entre testes).
func janelaMagenta(t *testing.T, x, y int) uintptr {
	t.Helper()
	// Precisa vir ANTES do CreateWindow: é a thread no momento da criação que
	// passa a ser a dona da janela.
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	exclusionClassOnce.Do(func() {
		exclusionClassErr = winui.RegisterWindowClass(
			"DiscoveryExclusionCaptureTest",
			windows.NewCallback(exclusionWndProc),
			0,
		)
	})
	if exclusionClassErr != nil {
		t.Skipf("registro da classe falhou: %v", exclusionClassErr)
	}
	hwnd, err := winui.CreateWindow(
		"DiscoveryExclusionCaptureTest", "exclusion test",
		winui.ExStyleTopmost|winui.ExStyleToolWindow|winui.ExStyleNoActivate,
		winui.WinStylePopup,
		x, y, testLargura, testAltura,
	)
	if err != nil {
		t.Skipf("criação da janela falhou: %v", err)
	}
	return hwnd
}

// capturarComTentativas tenta o capturer do pipeline (DXGI) algumas vezes: a
// duplicação devolve timeout quando nada mudou na tela. Sem frame, cai para o
// capturer GDI da região (outra API de captura, mesmo contrato de Frame).
func capturarComTentativas(t *testing.T) (*Frame, bool) {
	t.Helper()
	capturer, err := NewCapturerMode(0, false)
	if err == nil {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			frame, ferr := capturer.AcquireNextFrame()
			if ferr == nil && frame != nil {
				copia := &Frame{Data: append([]byte(nil), frame.Data...), Width: frame.Width, Height: frame.Height, Stride: frame.Stride, OriginX: frame.OriginX, OriginY: frame.OriginY}
				capturer.ReleaseFrame()
				capturer.Close()
				t.Logf("frame capturado por %q: %dx%d", capturer.Name(), copia.Width, copia.Height)
				return copia, true
			}
			time.Sleep(150 * time.Millisecond)
		}
		capturer.Close()
	}
	gdi, gerr := NewGDICapturerRegion(0, 0, 1920, 1080)
	if gerr != nil {
		t.Skipf("nenhum capturer disponível (DXGI timeout, GDI: %v)", gerr)
	}
	frame, ferr := gdi.AcquireNextFrame()
	if ferr != nil || frame == nil {
		gdi.Close()
		t.Skipf("captura falhou em todos os capturers: %v", ferr)
	}
	copia := &Frame{Data: append([]byte(nil), frame.Data...), Width: frame.Width, Height: frame.Height, Stride: frame.Stride, OriginX: frame.OriginX, OriginY: frame.OriginY}
	gdi.Close()
	t.Logf("frame capturado por GDI (fallback): %dx%d", copia.Width, copia.Height)
	return copia, true
}

// contarMagentaNaRegiao conta pixels RGB(255,0,255) apenas dentro da região da
// janela (coordenadas físicas de tela). Restringir à região evita que conteúdo
// residual de outro teste (DWM/DDA staging) falseie a contagem.
func contarMagentaNaRegiao(frame *Frame, x, y, w, h int) int {
	if frame == nil {
		return 0
	}
	relX, relY := x-frame.OriginX, y-frame.OriginY
	if relX < 0 || relY < 0 || relX+w > frame.Width || relY+h > frame.Height {
		return 0
	}
	stride := frame.Stride
	if stride <= 0 {
		stride = frame.Width * 4
	}
	magenta := 0
	for py := relY; py < relY+h; py++ {
		row := py * stride
		for px := relX; px < relX+w; px++ {
			off := row + px*4
			if off+3 >= len(frame.Data) {
				break
			}
			if frame.Data[off] == 0xFF && frame.Data[off+1] == 0x00 && frame.Data[off+2] == 0xFF {
				magenta++
			}
		}
	}
	return magenta
}

// contarMagenta conta pixels RGB(255,0,255) num frame BGRA top-down.
func contarMagenta(frame *Frame) int {
	if frame == nil {
		return 0
	}
	data := frame.Data
	stride := frame.Stride
	if stride <= 0 {
		stride = frame.Width * 4
	}
	magenta := 0
	for py := 0; py < frame.Height; py++ {
		row := py * stride
		for px := 0; px < frame.Width; px++ {
			off := row + px*4
			if off+3 >= len(data) {
				break
			}
			// BGRA: B=0xFF, G=0x00, R=0xFF
			if data[off] == 0xFF && data[off+1] == 0x00 && data[off+2] == 0xFF {
				magenta++
			}
		}
	}
	return magenta
}

// TestJanelaSemTratamentoApareceNaCaptura é o CONTROLE: valida o detector.
func TestJanelaSemTratamentoApareceNaCaptura(t *testing.T) {
	if testing.Short() {
		t.Skip("teste de captura real")
	}
	hwnd := janelaMagenta(t, 60, 60)
	defer winui.DestroyWindow(hwnd)
	t.Cleanup(func() { SetCapturerAtual(nil) })

	winui.ShowWindow(hwnd, winui.SwShowNormal)
	winui.UpdateWindow(hwnd)
	time.Sleep(400 * time.Millisecond)

	frame, ok := capturarComTentativas(t)
	if !ok {
		return
	}
	if magenta := contarMagentaNaRegiao(frame, 60, 60, testLargura, testAltura); magenta == 0 {
		t.Fatalf("controle inválido: a janela sem tratamento não apareceu na captura")
	}
}

// TestMascaraRemoveCartaoDaCaptura reproduz o caminho do banner: máscara
// capturada ANTES de o cartão aparecer + frame reescrito pelo pipeline.
func TestMascaraRemoveCartaoDaCaptura(t *testing.T) {
	if testing.Short() {
		t.Skip("teste de captura real")
	}
	limparMascaras(t)
	// Posição longe do teste de controle (evita conteúdo residual do DWM).
	hwnd := janelaMagenta(t, 700, 420)
	defer winui.DestroyWindow(hwnd)
	t.Cleanup(func() { removerMascara(hwnd); SetCapturerAtual(nil) })

	// ── POR QUE O REFRESH PRECISA FICAR DESLIGADO AQUI ──
	//
	// Este teste mede a máscara CONGELADA no instante anterior ao cartão: ele
	// liga a máscara, mostra a janela magenta e então verifica que o patch a
	// remove do frame. Se o refresh ao vivo estiver rodando, ele faz exatamente
	// o que deveria fazer em produção — e isso DESTRÓI a premissa do teste:
	//
	//   - ativarMascara inicia o loop; com custo baixo o intervalo é
	//     mascaraRefreshIntervaloRapido (500ms);
	//   - o teste mostra a janela magenta e espera 400ms antes de capturar;
	//   - o refresh dispara DENTRO dessa janela, procura a janela atrás do
	//     cartão, ENCONTRA a janela magenta (ela está visível, contém o ponto e
	//     não é cartão do agente — não há como excluí-la), imprime MAGENTA e
	//     grava isso na máscara;
	//   - o PatchFrameBannerAt do teste copia MAGENTA para o frame e a asserção
	//     falha.
	//
	// O PRODUTO ESTÁ CERTO nesse ponto: compor com a janela que está atrás do
	// cartão é o propósito do refresh ao vivo. O defeito é do TESTE, que deixou
	// um refresh concorrente disputando a máscara com uma janela que é o próprio
	// objeto da medição. Daí desligar o refresh aqui, e não mexer na asserção:
	// com a máscara congelada antes do cartão, o patch DEVE remover o magenta.
	//
	// mascaraRefreshAtivo é o gancho documentado para "testes/diagnóstico" e é
	// lido por iniciarRefreshMascaras sob mascaraRefreshMu: desligá-lo ANTES de
	// ativarMascara impede o loop de nascer. O restore tem de rodar DEPOIS do
	// DestroyWindow (t.Cleanup roda depois do defer), senão o loop poderia
	// reabrir sobre uma janela já destruída.
	refreshAnterior := mascaraRefreshAtivo
	mascaraRefreshAtivo = false
	t.Cleanup(func() { mascaraRefreshAtivo = refreshAnterior })

	// Dá tempo do DWM assentar o estado anterior antes de ler a máscara.
	time.Sleep(300 * time.Millisecond)

	// 1) Máscara da área com a janela AINDA OCULTA (conteúdo de baixo).
	if err := capturarMascaraRegiao(hwnd); err != nil {
		t.Skipf("máscara indisponível neste ambiente: %v", err)
	}
	ativarMascara(hwnd)

	// 2) Cartão aparece: sem tratamento ele entra no frame.
	winui.ShowWindow(hwnd, winui.SwShowNormal)
	winui.UpdateWindow(hwnd)
	time.Sleep(400 * time.Millisecond)

	frame, ok := capturarComTentativas(t)
	if !ok {
		return
	}
	antes := contarMagentaNaRegiao(frame, 700, 420, testLargura, testAltura)
	if antes == 0 {
		t.Skip("a janela de teste não apareceu na captura (área coberta?) — não é possível validar a máscara")
	}

	mascarasMu.Lock()
	m := mascaras[hwnd]
	if m != nil {
		t.Logf("mascara: rect=%+v ativa=%v px=%d", m.rect, m.ativa, len(m.px))
	} else {
		t.Logf("mascara: AUSENTE no registry")
	}
	mascarasMu.Unlock()
	t.Logf("frame: %dx%d origin=(%d,%d) stride=%d", frame.Width, frame.Height, frame.OriginX, frame.OriginY, frame.Stride)

	// 3) O pipeline reescreve a área mascarada — o cartão sai do frame.
	PatchFrameBannerAt(frame, frame.OriginX, frame.OriginY)
	if depois := contarMagentaNaRegiao(frame, 700, 420, testLargura, testAltura); depois != 0 {
		t.Fatalf("a máscara não removeu o cartão da captura: %d -> %d pixels magenta", antes, depois)
	}
	t.Logf("cartão removido da captura pela máscara (%d pixels antes)", antes)
}
