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
	procGetWindowRect   = user32.NewProc("GetWindowRect")
	procPrintWindow     = user32.NewProc("PrintWindow")

	mascaraRefreshMu   sync.Mutex
	mascaraRefreshStop chan struct{}
	// mascaraRefreshAtivo permite desligar o refresh (testes/diagnóstico).
	mascaraRefreshAtivo = true
)

// iniciarRefreshMascaras liga o loop de atualização (idempotente).
func iniciarRefreshMascaras() {
	if !mascaraRefreshAtivo {
		return
	}
	mascaraRefreshMu.Lock()
	defer mascaraRefreshMu.Unlock()
	if mascaraRefreshStop != nil {
		return
	}
	stop := make(chan struct{})
	mascaraRefreshStop = stop
	go func() {
		ticker := time.NewTicker(mascaraRefreshIntervalo)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if !atualizarMascarasAoVivo() {
					mascaraRefreshMu.Lock()
					if mascaraRefreshStop == stop {
						mascaraRefreshStop = nil
					}
					mascaraRefreshMu.Unlock()
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
func atualizarMascarasAoVivo() bool {
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
		atualizarMascaraAoVivo(m)
	}
	return true
}

// atualizarMascaraAoVivo tenta substituir os pixels de UMA máscara pelo conteúdo
// atual da janela que está atrás do cartão. Falha silenciosa: mantém a máscara.
func atualizarMascaraAoVivo(m *mascaraRegiao) {
	centroX := m.rect.X + m.rect.W/2
	centroY := m.rect.Y + m.rect.H/2
	janela := janelaSobPonto(centroX, centroY)
	if janela == 0 {
		logMascaraRefreshFalha("nenhuma janela sob o cartão")
		return
	}
	janelaRect, pixels, ok := imprimirJanela(janela)
	if !ok {
		logMascaraRefreshFalha("PrintWindow recusado")
		return
	}
	inter := m.rect.Intersect(janelaRect)
	if inter.Empty() {
		return
	}
	// Recorta a interseção do bitmap impresso e valida antes de aceitar.
	recorte := recortarRegiao(pixels, janelaRect.W, inter.X-janelaRect.X, inter.Y-janelaRect.Y, inter.W, inter.H)
	if !regiaoUtil(recorte) {
		logMascaraRefreshFalha("bitmap vazio/preto")
		return
	}
	logMascaraRefreshSucesso()

	mascarasMu.Lock()
	atual := mascaras[m.hwnd]
	if atual == nil || !atual.ativa || atual.rect != m.rect {
		mascarasMu.Unlock()
		return
	}
	// Escreve só a interseção: o resto da máscara (fora da janela impressa)
	// continua com o conteúdo congelado.
	destX := inter.X - atual.rect.X
	destY := inter.Y - atual.rect.Y
	for y := 0; y < inter.H; y++ {
		offDst := (destY+y)*atual.rect.W*4 + destX*4
		offSrc := y * inter.W * 4
		if offDst+inter.W*4 > len(atual.px) || offSrc+inter.W*4 > len(recorte) {
			break
		}
		copy(atual.px[offDst:offDst+inter.W*4], recorte[offSrc:offSrc+inter.W*4])
	}
	mascarasMu.Unlock()
}

// rectVazio indica que GetWindowRect falhou (o chamador trata como inválido).
// Recebe os três retornos de LazyProc.Call para poder ser usado direto.
func rectVazio(callReturn uintptr, _ uintptr, _ error) bool { return callReturn == 0 }

// janelaSobPonto devolve a primeira janela VISÍVEL na ordem Z que contém o ponto
// e NÃO é um dos cartões do agente (banner/aviso). 0 quando não encontra.
func janelaSobPonto(x, y int) uintptr {
	alvo := bannerCaptureTarget.Load()
	extras := capturaExtras()
	hwnd, _, _ := procGetTopWindow.Call(0)
	for hwnd != 0 {
		if visivel, _, _ := procIsWindowVisible.Call(hwnd); visivel != 0 {
			var r winui.Rect
			if !rectVazio(procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))) &&
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
func imprimirJanela(hwnd uintptr) (winui.Rect, []byte, bool) {
	var r winui.Rect
	if hwnd == 0 || rectVazio(procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))) {
		return winui.Rect{}, nil, false
	}
	if r.W <= 0 || r.H <= 0 || r.W > 16384 || r.H > 16384 {
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
	ok, _, _ := procPrintWindow.Call(hwnd, memDC, pwRenderFullContent)
	if ok == 0 {
		// Fallback: PrintWindow clássico (alguns apps antigos só respondem a ele).
		ok, _, _ = procPrintWindow.Call(hwnd, memDC, 0)
	}
	procGdiFlush.Call()
	out := make([]byte, r.W*r.H*4)
	if ok != 0 {
		copy(out, unsafe.Slice((*byte)(bits), len(out)))
	}
	procSelectObject.Call(memDC, prev)
	procDeleteObject.Call(hbm)
	if ok == 0 {
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
// recusado por app UWP/GPU) ou uniforme (render vazio). Amostra 1 a cada 97
// pixels para manter o custo baixo. Região legítima e uniforme (barra de título
// lisa) é rejeitada — nesse caso a máscara congelada já é o mesmo conteúdo.
func regiaoUtil(pixels []byte) bool {
	if len(pixels) < 4 {
		return false
	}
	minR, maxR := byte(0xFF), byte(0)
	naoPreto := false
	for i := 0; i+3 < len(pixels); i += 97 * 4 {
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
// estado (não polui o log a cada 2s).
var (
	mascaraRefreshLogMu   sync.Mutex
	mascaraRefreshLogErro bool
)

func logMascaraRefreshFalha(motivo string) {
	mascaraRefreshLogMu.Lock()
	primeiro := !mascaraRefreshLogErro
	mascaraRefreshLogErro = true
	mascaraRefreshLogMu.Unlock()
	if primeiro {
		log.Printf("[session-banner] refresh da máscara indisponível (%s) — mantendo o quadro congelado", motivo)
	}
}

func logMascaraRefreshSucesso() {
	mascaraRefreshLogMu.Lock()
	estavaComErro := mascaraRefreshLogErro
	mascaraRefreshLogErro = false
	mascaraRefreshLogMu.Unlock()
	if estavaComErro {
		log.Printf("[session-banner] refresh da máscara voltou a funcionar (conteúdo ao vivo atrás do cartão)")
	}
}
