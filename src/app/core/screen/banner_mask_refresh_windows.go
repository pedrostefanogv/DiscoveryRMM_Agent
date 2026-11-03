//go:build windows

package screen

import (
	"log"
	"sync"
	"time"
	"unsafe"

	"discovery/app/core/winui"
)

// ── Refresh da máscara com o conteúdo AO VIVO atrás do cartão ──
//
// A máscara (banner_patch_windows.go) é lida uma única vez, antes de o cartão
// aparecer: o stream recebe o conteúdo daquele instante congelado. Para o caso
// comum (barra de título, papel de parede, app parado) isso é invisível, mas um
// vídeo/animação sob o cartão ficaria parado no stream.
//
// Este arquivo atualiza a máscara periodicamente com PrintWindow da janela que
// está ATRÁS do cartão. PrintWindow(PW_RENDERFULLCONTENT) renderiza o conteúdo
// PRÓPRIO da janela (sem as janelas por cima), que é exatamente o que o operador
// deveria ver naquela faixa.
//
// Regras de segurança:
//   - nunca segura mascarasMu durante o PrintWindow (um app travado não pode
//     congelar o pipeline de captura);
//   - resultado suspeito (tudo preto/tudo igual) é DESCARTADO e a máscara
//     anterior é mantida — alguns apps (UWP/GPU) devolvem bitmap vazio;
//   - só atualiza máscara ATIVA e de janela visível;
//   - o loop se encerra sozinho quando não há mais máscara ativa.

const (
	// pwRenderFullContent faz o PrintWindow usar o conteúdo composto pelo DWM
	// (necessário para apps modernos; sem ele muitos devolvem branco).
	pwRenderFullContent = 0x00000002
	// gwHwndNext percorre a ordem Z (GetWindow).
	gwHwndNext = 2
	// mascaraRefreshIntervalo é o período de atualização. 2s é suficiente para
	// conteúdo animado e mantém o custo (PrintWindow de uma janela) baixo.
	mascaraRefreshIntervalo = 2 * time.Second
)

var (
	procGetTopWindow    = user32.NewProc("GetTopWindow")
	procGetWindow       = user32.NewProc("GetWindow")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
	procIsHungAppWindow = user32.NewProc("IsHungAppWindow")
	procPrintWindow     = user32.NewProc("PrintWindow")

	mascaraRefreshMu   sync.Mutex
	mascaraRefreshStop chan struct{}
	// mascaraRefreshAtivo permite desligar o refresh (testes/diagnóstico).
	mascaraRefreshAtivo = true
)

// mascaraRefreshMaxPixels limita o tamanho da janela impressa (12 MP ≈ 48 MB).
// Janela maior que isso (8K) não compensa o custo: mantém a máscara congelada.
const mascaraRefreshMaxPixels = 12_000_000

// iniciarRefreshMascaras liga o loop de atualização (idempotente).
func iniciarRefreshMascaras() {
	mascaraRefreshMu.Lock()
	defer mascaraRefreshMu.Unlock()
	if !mascaraRefreshAtivo || mascaraRefreshStop != nil {
		return
	}
	stop := make(chan struct{})
	mascaraRefreshStop = stop
	go func() {
		// Buffer do PrintWindow é DA GERAÇÃO: quando o loop é reiniciado com
		// outro ainda preso em PrintWindow, os dois não compartilham memória.
		var buf []byte
		ticker := time.NewTicker(mascaraRefreshIntervalo)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if !atualizarMascarasAoVivo(&buf) {
					mascaraRefreshMu.Lock()
					encerrou := mascaraRefreshStop == stop
					if encerrou {
						mascaraRefreshStop = nil
					}
					mascaraRefreshMu.Unlock()
					if encerrou {
						// Corrida de reativação: uma máscara pode ter sido ativada
						// entre a checagem e o encerramento (ativarMascara viu o
						// stop ainda não-nulo e não abriu um loop novo). Se ainda
						// há máscara ativa, reabre.
						if RegionMaskAvailable() {
							iniciarRefreshMascaras()
						}
					}
					return
				}
			}
		}
	}()
}

// pararRefreshMascaras encerra o loop (usado quando não há mais máscara).
func pararRefreshMascaras() {
	mascaraRefreshMu.Lock()
	stop := mascaraRefreshStop
	mascaraRefreshStop = nil
	mascaraRefreshMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// atualizarMascarasAoVivo atualiza todas as máscaras ativas e devolve true
// enquanto houver pelo menos uma (o loop usa isso para se encerrar).
func atualizarMascarasAoVivo(buf *[]byte) bool {
	mascarasMu.Lock()
	alvos := make([]*mascaraRegiao, 0, len(mascaras))
	for _, m := range mascaras {
		if m.ativa {
			alvos = append(alvos, m)
		}
	}
	mascarasMu.Unlock()
	if len(alvos) == 0 {
		return false
	}

	for _, m := range alvos {
		if !mascaraVisivel(m.hwnd) {
			continue
		}
		atualizarMascaraAoVivo(m, buf)
	}
	return true
}

// atualizarMascaraAoVivo tenta substituir os pixels de UMA máscara pelo conteúdo
// atual da janela que está atrás do cartão. Falha silenciosa: mantém a máscara.
func atualizarMascaraAoVivo(m *mascaraRegiao, buf *[]byte) {
	centroX := m.rect.X + m.rect.W/2
	centroY := m.rect.Y + m.rect.H/2
	janela := janelaSobPonto(centroX, centroY)
	if janela == 0 {
		logMascaraRefreshFalha(m.hwnd, "nenhuma janela sob o cartão")
		return
	}
	janelaRect, pixels, ok := imprimirJanela(janela, buf)
	if !ok {
		logMascaraRefreshFalha(m.hwnd, "PrintWindow recusado")
		return
	}
	inter := m.rect.Intersect(janelaRect)
	if inter.Empty() {
		return
	}
	// Recorta a interseção do bitmap impresso e valida antes de aceitar.
	recorte := recortarRegiao(pixels, janelaRect.W, inter.X-janelaRect.X, inter.Y-janelaRect.Y, inter.W, inter.H)
	if !regiaoUtil(recorte) {
		logMascaraRefreshFalha(m.hwnd, "bitmap vazio/preto")
		return
	}
	logMascaraRefreshSucesso(m.hwnd)

	mascarasMu.Lock()
	atual := mascaras[m.hwnd]
	if atual == nil || !atual.ativa || atual.rect != m.rect {
		mascarasMu.Unlock()
		return
	}
	// COPY-ON-WRITE: monta um buffer novo e troca o slice sob lock. O patcher
	// (PatchFrameBanner) lê o slice antigo sem lock — trocar o conteúdo no lugar
	// seria uma corrida de dados (pixels rasgados no stream).
	novo := make([]byte, len(atual.px))
	copy(novo, atual.px)
	// Escreve só a interseção: o resto da máscara (fora da janela impressa)
	// continua com o conteúdo congelado.
	destX := inter.X - atual.rect.X
	destY := inter.Y - atual.rect.Y
	for y := 0; y < inter.H; y++ {
		offDst := (destY+y)*atual.rect.W*4 + destX*4
		offSrc := y * inter.W * 4
		if offDst+inter.W*4 > len(novo) || offSrc+inter.W*4 > len(recorte) {
			break
		}
		copy(novo[offDst:offDst+inter.W*4], recorte[offSrc:offSrc+inter.W*4])
	}
	atual.px = novo
	mascarasMu.Unlock()
}

// janelaSobPonto devolve a primeira janela VISÍVEL na ordem Z que contém o ponto
// e NÃO é um dos cartões do agente (banner/aviso). 0 quando não encontra.
func janelaSobPonto(x, y int) uintptr {
	alvo := bannerCaptureTarget.Load()
	extras := capturaExtras()
	hwnd, _, _ := procGetTopWindow.Call(0)
	for hwnd != 0 {
		if visivel, _, _ := procIsWindowVisible.Call(hwnd); visivel != 0 {
			if r, ok := winui.WindowRect(hwnd); ok &&
				r.Contains(x, y) && !ehCartaoDoAgente(hwnd, alvo, extras) {
				return hwnd
			}
		}
		hwnd, _, _ = procGetWindow.Call(hwnd, gwHwndNext)
	}
	return 0
}

// ehCartaoDoAgente informa se hwnd é o banner ou um aviso da bandeja.
func ehCartaoDoAgente(hwnd, alvo uintptr, extras []uintptr) bool {
	if hwnd == 0 {
		return false
	}
	if hwnd == alvo {
		return true
	}
	for _, extra := range extras {
		if hwnd == extra {
			return true
		}
	}
	return false
}

// imprimirJanela renderiza a janela num DIB BGRA top-down e devolve o retângulo
// da janela + os pixels. ok=false quando o Windows recusa ou a janela é grande
// demais para o custo valer a pena.
func imprimirJanela(hwnd uintptr, buf *[]byte) (winui.Rect, []byte, bool) {
	r, ok := winui.WindowRect(hwnd)
	if hwnd == 0 || !ok {
		return winui.Rect{}, nil, false
	}
	if r.W <= 0 || r.H <= 0 || r.W*r.H > mascaraRefreshMaxPixels {
		return winui.Rect{}, nil, false
	}
	// App travado (não responde a mensagens): PrintWindow bloquearia a goroutine
	// de refresh para sempre. Melhor manter a máscara congelada.
	if travada, _, _ := procIsHungAppWindow.Call(hwnd); travada != 0 {
		return winui.Rect{}, nil, false
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return winui.Rect{}, nil, false
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return winui.Rect{}, nil, false
	}
	defer procDeleteDC.Call(memDC)

	var bi [40]byte
	bi[0] = 40
	*(*int32)(unsafe.Pointer(&bi[4])) = int32(r.W)
	*(*int32)(unsafe.Pointer(&bi[8])) = -int32(r.H) // top-down
	*(*uint16)(unsafe.Pointer(&bi[12])) = 1
	*(*uint16)(unsafe.Pointer(&bi[14])) = 32
	*(*uint32)(unsafe.Pointer(&bi[16])) = BI_RGB

	var bits unsafe.Pointer
	hbm, _, _ := procCreateDIBSection.Call(
		screenDC,
		uintptr(unsafe.Pointer(&bi[0])),
		DIB_RGB_COLORS,
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if hbm == 0 || bits == nil {
		return winui.Rect{}, nil, false
	}
	prev, _, _ := procSelectObject.Call(memDC, hbm)
	impresso, _, _ := procPrintWindow.Call(hwnd, memDC, pwRenderFullContent)
	if impresso == 0 {
		// Fallback: PrintWindow clássico (alguns apps antigos só respondem a ele).
		impresso, _, _ = procPrintWindow.Call(hwnd, memDC, 0)
	}
	procGdiFlush.Call()
	tamanho := r.W * r.H * 4
	if cap(*buf) < tamanho {
		*buf = make([]byte, tamanho)
	}
	out := (*buf)[:tamanho]
	if impresso != 0 {
		copy(out, unsafe.Slice((*byte)(bits), tamanho))
	}
	procSelectObject.Call(memDC, prev)
	procDeleteObject.Call(hbm)
	if impresso == 0 {
		return winui.Rect{}, nil, false
	}
	return r, out, true
}

// recortarRegiao extrai um retângulo de um bitmap BGRA top-down.
func recortarRegiao(pixels []byte, largura, x, y, w, h int) []byte {
	if largura <= 0 || w <= 0 || h <= 0 || x < 0 || y < 0 {
		return nil
	}
	out := make([]byte, w*h*4)
	for linha := 0; linha < h; linha++ {
		offSrc := (y+linha)*largura*4 + x*4
		offDst := linha * w * 4
		if offSrc+w*4 > len(pixels) || offDst+w*4 > len(out) {
			return nil
		}
		copy(out[offDst:offDst+w*4], pixels[offSrc:offSrc+w*4])
	}
	return out
}

// regiaoUtil descarta resultados suspeitos: bitmap todo preto (PrintWindow
// recusado por app UWP/GPU) ou uniforme (render vazio). Região legítima e
// uniforme (barra de título lisa) é rejeitada — nesse caso a máscara congelada
// já é o mesmo conteúdo.
//
// A amostragem é proporcional ao tamanho (até ~1024 amostras): com passo fixo de
// 97 pixels uma interseção pequena (ex.: 80 px de largura) era amostrada em 1
// ponto só e SEMPRE rejeitada, desligando o refresh justamente no caso comum de
// janela estreita sob o cartão.
func regiaoUtil(pixels []byte) bool {
	if len(pixels) < 4 {
		return false
	}
	passoPixels := (len(pixels) / 4) / 1024
	if passoPixels < 1 {
		passoPixels = 1
	}
	minR, maxR := byte(0xFF), byte(0)
	naoPreto := false
	for i := 0; i+3 < len(pixels); i += passoPixels * 4 {
		r := pixels[i+2]
		if r < minR {
			minR = r
		}
		if r > maxR {
			maxR = r
		}
		if pixels[i] != 0 || pixels[i+1] != 0 || pixels[i+2] != 0 {
			naoPreto = true
		}
	}
	return naoPreto && maxR-minR >= 2
}

// logMascaraRefresh registra o resultado do refresh uma única vez por mudança de
// estado E POR JANELA (o banner pode falhar e o aviso da bandeja funcionar; um
// estado global faria as duas linhas se alternarem para sempre).
var (
	mascaraRefreshLogMu   sync.Mutex
	mascaraRefreshLogErro = map[uintptr]bool{}
)

func logMascaraRefreshFalha(hwnd uintptr, motivo string) {
	mascaraRefreshLogMu.Lock()
	primeiro := !mascaraRefreshLogErro[hwnd]
	mascaraRefreshLogErro[hwnd] = true
	mascaraRefreshLogMu.Unlock()
	if primeiro {
		log.Printf("[session-banner] refresh da máscara indisponível (%s) — mantendo o quadro congelado", motivo)
	}
}

func logMascaraRefreshSucesso(hwnd uintptr) {
	mascaraRefreshLogMu.Lock()
	estavaComErro := mascaraRefreshLogErro[hwnd]
	delete(mascaraRefreshLogErro, hwnd)
	mascaraRefreshLogMu.Unlock()
	if estavaComErro {
		log.Printf("[session-banner] refresh da máscara voltou a funcionar (conteúdo ao vivo atrás do cartão)")
	}
}
