//go:build windows

package screen

import (
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"unsafe"

	"discovery/app/core/winui"
)

// ── Emenda das janelas do agente na captura GDI (só na tela física) ──
//
// Problema real (Win10/VM, DESKTOP-DH608V7): o Windows recusa TODOS os valores
// de SetWindowDisplayAffinity para a janela do banner com
// ERROR_NOT_ENOUGH_MEMORY (8) — não há compositor/GDI que isole a janela da
// captura. Sem afinidade, o cartão "Sessão remota em andamento" vazava para o
// stream do operador, embora a intenção seja que ele exista apenas na tela
// FÍSICA do usuário acessado. No Win11 o mesmo vale para o aviso da bandeja:
// ele é desenhado pelo explorer e não tem HWND nosso para receber afinidade
// (por isso o aviso virou janela própria, session_tray_balloon_windows.go).
//
// Solução: o próprio pipeline de captura contorna a área de TODAS as janelas
// registradas do agente (banner + aviso da bandeja, via registerBannerCapture/
// registerCapturaExtra).
//
//  1. Antes de cada BitBlt, o capturer pergunta a cada janela qual retângulo
//     está de fato visível (winui.VisibleRect — respeita WS_VISIBLE, o recorte
//     do cliente e a janela dona).
//  2. Dois modos de montagem do quadro (planoBannerMulti):
//     • Modo conservador (padrão, janela NÃO atravessa as duas bordas laterais):
//     um único BitBlt do quadro inteiro; em seguida a área de cada janela é
//     SOBRESCRITA com o quadro anterior (histórico de 1 quadro). O cartão nunca
//     chega ao viewer e a captura custa exatamente o mesmo que a original.
//     • Modo faixas (alguma janela atravessa as duas bordas laterais): copia só
//     as faixas acima/abaixo do intervalo de linhas coberto e completa o resto
//     com o quadro anterior. Aí o GDI copia menos pixels, mas o resultado
//     visível é o mesmo.
//  3. O buraco é sempre preenchido com o quadro anterior, nunca com preto: o
//     player continua recebendo 100% dos pixels (a área vem do passado) e não
//     há salto visível.
//
// A tela física NÃO é alterada em momento algum: nada de esconder/reexibir o
// cartão por frame (não pisca, não rouba foco, o SetTimer de refresh continua
// válido). A emenda não mexe no contrato de cor: as faixas vêm da mesma DIB
// section 32bpp (BGRA canônico) que o pipeline já consome.

var (
	// capturerAtual guarda o capturer GDI que está capturando agora (weak
	// referência: o pipeline é o dono do objeto). Serve para o banner saber se
	// vale a pena escolher a emenda em vez de depender de SetWindowDisplayAffinity.
	capturerAtualMu sync.Mutex
	capturerAtual   Capturer

	// bannerCaptureTarget guarda o HWND do banner ativo (0 = nenhum).
	bannerCaptureTarget atomic.Uintptr
	// capturaExtrasMu/capturaExtras guardam outras janelas do agente que também
	// precisam ficar fora do stream — hoje o aviso da bandeja
	// (session_tray_balloon). São cartões desenhados por NÓS justamente para
	// poderem ser emendados: o balloon do explorer não tem HWND nosso e vazava
	// para o stream no Win11.
	capturaExtrasMu sync.Mutex
	janelasExtras   []uintptr
	// bannerCapturePrimed liga a emenda: só quando já existe um quadro anterior
	// completo (o primeiro frame depois de registrar o banner é sempre inteiro).
	bannerCapturePrimed atomic.Bool
	// emendaConhecida guarda a capacidade do ÚLTIMO capturer criado no processo.
	// O banner nasce ANTES do capturer da sessão (manager.CreateSession), então
	// no instante da consulta o registry pode estar vazio; sem esta memória o
	// Win10 cairia sempre na escada do SetWindowDisplayAffinity (que falha com
	// errno=8) e o log diria "sem exclusão" mesmo com a emenda funcionando.
	emendaConhecida atomic.Bool

	// bannerCaptureMu serializa banner e captura: register/unregister não podem
	// trocar o alvo no meio de um AcquireNextFrame (o capturer tem um único dono,
	// mas o banner nasce e morre na thread da UI da sessão).
	bannerCaptureMu sync.Mutex
)

// registerBannerCapture marca hwnd como a janela a contornar na captura.
//
// O histórico NÃO é invalidado aqui de propósito: os cartões do agente (banner e
// aviso da bandeja) são mostrados depois e o invalidador é o
// registerCapturaExtra, chamado na hora de exibi-los. Invalidar no registro
// faria todo quadro depois de uma recriação de capturer cair no caminho caro.
func registerBannerCapture(hwnd uintptr) {
	bannerCaptureMu.Lock()
	bannerCaptureTarget.Store(hwnd)
	bannerCaptureMu.Unlock()
}

// unregisterBannerCapture desmarca hwnd (janela destruída / fim da sessão).
// Aceita tanto o banner quanto qualquer janela extra registrada.
func unregisterBannerCapture(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	bannerCaptureMu.Lock()
	if bannerCaptureTarget.Load() == hwnd {
		bannerCaptureTarget.Store(0)
		bannerCapturePrimed.Store(false)
	}
	bannerCaptureMu.Unlock()
	removerCapturaExtra(hwnd)
}

// registerCapturaExtra registra uma janela do agente (além do banner) cuja área
// precisa sair do stream — hoje o aviso da bandeja.
//
// NÃO invalida o histórico de propósito: o quadro anterior foi capturado ANTES
// da janela aparecer, então ele é justamente o conteúdo limpo que a emenda
// precisa para cobrir a área desde o primeiro quadro (sem flash de 1 frame).
// Invalidar faria o quadro seguinte ir inteiro (com a janela) para o stream.
func registerCapturaExtra(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	capturaExtrasMu.Lock()
	for _, atual := range janelasExtras {
		if atual == hwnd {
			capturaExtrasMu.Unlock()
			return
		}
	}
	janelasExtras = append(janelasExtras, hwnd)
	capturaExtrasMu.Unlock()
}

// removerCapturaExtra tira a janela da lista de exclusão (idempotente).
func removerCapturaExtra(hwnd uintptr) {
	capturaExtrasMu.Lock()
	defer capturaExtrasMu.Unlock()
	for i, atual := range janelasExtras {
		if atual == hwnd {
			janelasExtras = append(janelasExtras[:i], janelasExtras[i+1:]...)
			return
		}
	}
}

// capturaExtras devolve uma cópia da lista de janelas extras (fora do lock).
func capturaExtras() []uintptr {
	capturaExtrasMu.Lock()
	defer capturaExtrasMu.Unlock()
	if len(janelasExtras) == 0 {
		return nil
	}
	out := make([]uintptr, len(janelasExtras))
	copy(out, janelasExtras)
	return out
}

// CaptureExclusionSupported informa se o caminho de captura ATUAL sabe manter o
// banner fora do stream sem SetWindowDisplayAffinity. Nesse caso o banner
// prefere esse degrau: fica íntegro na tela física (o operador não vê o cartão,
// e o usuário local continua sabendo que está sendo acessado).
//
// Não basta haver um capturer construído: o pipeline pode descartar e recriar o
// capturer no meio da sessão (ver incidente GDI/DXGI em capturer_gdi.go), por
// isso a resposta considera apenas o capturer ATIVO no momento da consulta.
func CaptureExclusionSupported() bool {
	capturerAtualMu.Lock()
	atual := capturerAtual
	capturerAtualMu.Unlock()
	if atual != nil && !capturerNulo(atual) {
		return capturerSupportsBannerPatching(atual)
	}
	// Sem capturer ativo agora (banner criado antes do capturer da sessão):
	// usa a capacidade do último capturer do processo.
	return emendaConhecida.Load()
}

// SetCapturerAtual registra o capturer que o pipeline está usando (aceita nil
// quando ele é descartado). Vem do registry de capturers para não acoplar
// banner e pipeline.
func SetCapturerAtual(c Capturer) {
	if c != nil && !capturerNulo(c) {
		// Memoriza a capacidade antes de publicar: o banner pode consultar em
		// outra goroutine logo depois.
		emendaConhecida.Store(capturerSupportsBannerPatching(c))
	}
	capturerAtualMu.Lock()
	capturerAtual = c
	capturerAtualMu.Unlock()
}

// atualCapturerEh informa se c ainda é o capturer publicado como ativo.
func atualCapturerEh(c Capturer) bool {
	capturerAtualMu.Lock()
	defer capturerAtualMu.Unlock()
	return capturerAtual == c
}

// capturerSupportsBannerPatching reconhece os capturers que implementam a emenda
// por região. Hoje só o GDI: o DXGI duplica a saída do desktop e ignora tanto a
// emenda quanto, em muitos drivers, o WDA_MONITOR.
func capturerSupportsBannerPatching(c Capturer) bool {
	if c == nil || capturerNulo(c) {
		return false
	}
	if supp, ok := c.(interface{ SupportsBannerPatch() bool }); ok {
		return supp.SupportsBannerPatch()
	}
	return false
}

// capturerNulo detecta um valor NÃO-nulo de interface carregando um ponteiro
// nil (ex.: var c *gdiCapturer = nil devolvido como Capturer). Sem esta checagem
// o type assertion acima funcionaria e o método rodaria com receiver nil.
//
// O registry só recebe capturers construídos, mas Close() de um deles pode
// zerar o registro em corrida com a criação do banner; um panic dentro do
// wndproc derrubaria o processo responsável pela sessão inteira.
func capturerNulo(c Capturer) bool {
	return reflect.ValueOf(c).Kind() == reflect.Ptr && reflect.ValueOf(c).IsNil()
}

// bannerRectScreen devolve o retângulo visível do banner em coordenadas de tela
// física, relativo ao monitor capturado.
//
// Só devolve ok quando o banner está INTEIRO dentro da área capturada: um banner
// parcialmente fora exigiria emendar duas regiões e o ganho não justifica o
// risco de artefato (não há artefato nenhum, mas o teste fica mais claro).
func bannerRectScreen(banner uintptr, offX, offY, width, height int) (winui.Rect, bool) {
	if banner == 0 || width <= 0 || height <= 0 {
		return winui.Rect{}, false
	}
	visivel, ok := winui.VisibleRect(banner)
	if !ok || visivel.Empty() {
		return winui.Rect{}, false
	}
	// Defensivo: um HWND reciclado pelo Windows nunca cobre a tela com essas
	// dimensões; área absurda é sinal de handle reaproveitado.
	if visivel.W > 32768 || visivel.H > 32768 {
		return winui.Rect{}, false
	}

	rel := winui.Rect{X: visivel.X - offX, Y: visivel.Y - offY, W: visivel.W, H: visivel.H}
	area := winui.Rect{X: 0, Y: 0, W: width, H: height}
	if rel.Empty() || !area.ContainsRect(rel) {
		return winui.Rect{}, false
	}
	return rel, true
}

// bannerPlan é o plano de captura de um quadro com janelas do agente em tela.
type bannerPlan struct {
	// copiar: regiões a capturar da tela (modo faixas: as faixas acima/abaixo;
	// modo conservador: o quadro inteiro).
	copiar  [2]winui.Rect
	nCopiar int
	// buracos: áreas do quadro a preencher com o histórico (uma por janela no
	// modo conservador, uma faixa de linhas no modo faixas). Sempre disjuntos ou
	// sobrepostos — preencher duas vezes da mesma origem é inofensivo.
	buracos  [4]winui.Rect
	nBuracos int
}

// planoBanner calcula como capturar o quadro contornando UMA janela do agente.
// É o caso de uma janela só; o pipeline usa planoBannerMulti, que generaliza
// esta mesma lógica (e é o que os testes cobrem).
//
//	modo A (faixas, banner atravessando as duas bordas laterais): copia só a faixa
//	    acima e a abaixo do cartão; o buraco é a coluna de linhas do banner.
//	    Aqui copiado + buraco == área do quadro (verificado no teste).
//	modo B (conservador, caso real do cartão flutuante): copia o quadro inteiro e
//	    trata o retângulo do banner como buraco, que será sobrescrito pelo quadro
//	    anterior. O buraco está DENTRO do que foi copiado, então a soma acima não
//	    vale — copiado já é o quadro inteiro.
//
// devolve ok=false quando não há interseção (nada a esconder) ou quando o banner
// cobre o monitor inteiro.
func planoBanner(frame, banner winui.Rect) (bannerPlan, bool) {
	return planoBannerMulti(frame, []winui.Rect{banner})
}

// regioesForaDaCaptura devolve, em coordenadas do quadro, todas as áreas do
// agente que precisam sair do stream: o banner e as janelas extras (aviso da
// bandeja). Retângulos vazios/fora do monitor são descartados.
//
// Duas janelas sobrepostas entram como dois retângulos: o plano já lida com
// buracos que se sobrepõem (modo B copia o quadro inteiro e recorta cada um).
func (c *gdiCapturer) regioesForaDaCaptura(alvo uintptr) []winui.Rect {
	var out []winui.Rect
	if alvo != 0 {
		if r, ok := bannerRectScreen(alvo, c.offsetX, c.offsetY, c.width, c.height); ok {
			out = append(out, r)
		}
	}
	for _, extra := range capturaExtras() {
		if extra == alvo {
			continue
		}
		if r, ok := bannerRectScreen(extra, c.offsetX, c.offsetY, c.width, c.height); ok {
			out = append(out, r)
		}
	}
	return out
}

// captureFrameAvoidingBanner captura o monitor inteiro exceto as áreas das
// janelas do agente (banner e aviso da bandeja), compondo o restante com o
// quadro anterior. A DIB fica 100% preenchida.
//
// O chamador já passou por ensureDIB (memDC e dibBits válidos).
func (c *gdiCapturer) captureFrameAvoidingBanner(screenDC uintptr) error {
	frame := winui.Rect{X: 0, Y: 0, W: c.width, H: c.height}

	bannerCaptureMu.Lock()
	defer bannerCaptureMu.Unlock()

	alvo := bannerCaptureTarget.Load()
	if alvo == 0 && len(capturaExtras()) == 0 {
		// Nenhuma janela do agente em tela: caminho original, sem custo extra.
		return c.bitbltFullFrame(screenDC)
	}
	if !bannerCapturePrimed.Load() || c.semBannerHistorico() {
		// Primeira captura depois de uma janela aparecer (ou histórico de outro
		// tamanho): quadro completo, que também alimenta o histórico.
		return c.bitbltFullFrame(screenDC)
	}

	regioes := c.regioesForaDaCaptura(alvo)
	if len(regioes) == 0 {
		// Janelas invisíveis agora (minimizadas, tela bloqueada, monitor
		// trocado): nada a esconder neste quadro.
		return c.bitbltFullFrame(screenDC)
	}

	plano, ok := planoBannerMulti(frame, regioes)
	if !ok {
		// Janelas cobrindo o monitor inteiro ou fora dele: não há caminho sem
		// elas. Mantém o histórico atualizado para a próxima emenda.
		return c.bitbltFullFrame(screenDC)
	}

	procGdiFlush.Call()
	for i := 0; i < plano.nCopiar; i++ {
		r := plano.copiar[i]
		if err := c.bitbltRegion(screenDC, r, r.X, r.Y); err != nil {
			return err
		}
	}
	procGdiFlush.Call()

	for i := 0; i < plano.nBuracos; i++ {
		c.fillBannerHole(plano.buracos[i])
	}
	c.rememberFrame()
	return nil
}

// planoBannerMulti é o planoBanner para VÁRIAS janelas: modo faixas quando
// alguma atravessa as duas bordas laterais (aí as faixas copiadas já excluem
// todas, desde que se recorte pelo retângulo mais alto/mais baixo); modo
// conservador caso contrário, com um buraco por janela.
func planoBannerMulti(frame winui.Rect, regioes []winui.Rect) (bannerPlan, bool) {
	var plano bannerPlan
	var validas []winui.Rect
	for _, regiao := range regioes {
		inter := frame.Intersect(regiao)
		if inter.Empty() {
			continue
		}
		cobreTudo := inter.X <= frame.X && inter.Right() >= frame.Right() &&
			inter.Y <= frame.Y && inter.Bottom() >= frame.Bottom()
		if cobreTudo {
			return bannerPlan{}, false
		}
		validas = append(validas, inter)
	}
	if len(validas) == 0 {
		return bannerPlan{}, false
	}
	// Mais janelas do que buracos disponíveis: funde todas em um único retângulo
	// (união). Preferimos esconder uma área maior do que deixar uma janela vazar
	// silenciosamente por falta de espaço no plano.
	if len(validas) > len(plano.buracos) {
		uniao := validas[0]
		for _, inter := range validas[1:] {
			x1, y1 := min(uniao.X, inter.X), min(uniao.Y, inter.Y)
			x2, y2 := max(uniao.Right(), inter.Right()), max(uniao.Bottom(), inter.Bottom())
			uniao = winui.Rect{X: x1, Y: y1, W: x2 - x1, H: y2 - y1}
		}
		if uniao.X <= frame.X && uniao.Right() >= frame.Right() &&
			uniao.Y <= frame.Y && uniao.Bottom() >= frame.Bottom() {
			return bannerPlan{}, false
		}
		validas = []winui.Rect{uniao}
	}

	// Modo faixas: qualquer janela atravessando as DUAS bordas laterais obriga a
	// cortar as linhas inteiras (as faixas acima/abaixo do topo mais alto e da
	// base mais baixa).
	atravessa := false
	for _, inter := range validas {
		if inter.X <= frame.X && inter.Right() >= frame.Right() {
			atravessa = true
			break
		}
	}
	if atravessa {
		topo, base := validas[0].Y, validas[0].Bottom()
		for _, inter := range validas[1:] {
			if inter.Y < topo {
				topo = inter.Y
			}
			if inter.Bottom() > base {
				base = inter.Bottom()
			}
		}
		acima := winui.Rect{X: frame.X, Y: frame.Y, W: frame.W, H: topo - frame.Y}
		abaixo := winui.Rect{X: frame.X, Y: base, W: frame.W, H: frame.Bottom() - base}
		if acima.H > 0 {
			plano.copiar[plano.nCopiar] = acima
			plano.nCopiar++
		}
		if abaixo.H > 0 {
			plano.copiar[plano.nCopiar] = abaixo
			plano.nCopiar++
		}
		if plano.nCopiar == 0 {
			return bannerPlan{}, false
		}
		plano.buracos[0] = winui.Rect{
			X: 0,
			Y: topo - frame.Y,
			W: frame.W,
			H: base - topo,
		}
		plano.nBuracos = 1
		return plano, true
	}

	// Modo conservador: um único BitBlt do quadro inteiro e cada janela vira um
	// buraco recortado no quadro.
	plano.copiar[0] = frame
	plano.nCopiar = 1
	for _, inter := range validas {
		if plano.nBuracos == len(plano.buracos) {
			break
		}
		plano.buracos[plano.nBuracos] = winui.Rect{
			X: inter.X - frame.X,
			Y: inter.Y - frame.Y,
			W: inter.W,
			H: inter.H,
		}
		plano.nBuracos++
	}
	return plano, true
}

// fillBannerHole preenche a área do banner com o quadro anterior (memcpy: a DIB
// está em memória de sistema, top-down, stride = largura*4).
func (c *gdiCapturer) fillBannerHole(buraco winui.Rect) {
	if buraco.Empty() || c.bannerPrev == nil || c.dibBits == nil {
		return
	}
	stride := c.width * 4
	rowBytes := buraco.W * 4
	dst := unsafe.Slice((*byte)(c.dibBits), c.width*c.height*4)
	for y := 0; y < buraco.H; y++ {
		off := (buraco.Y+y)*stride + buraco.X*4
		// Histórico é quadro completo, com o próprio buraco já "limpo" da versão
		// anterior — mas o último registro pode ser do quadro atual (parcial),
		// então copiamos dele a cada preenchimento (ver rememberFrame).
		if off+rowBytes > len(dst) || off+rowBytes > len(c.bannerPrev) {
			return
		}
		copy(dst[off:off+rowBytes], c.bannerPrev[off:off+rowBytes])
	}
}

// semBannerHistorico indica que não há histórico reutilizável do tamanho atual.
func (c *gdiCapturer) semBannerHistorico() bool {
	necessario := c.width * c.height * 4
	return c.bannerPrev == nil || len(c.bannerPrev) != necessario
}

// ensureBannerHistorico garante o histórico de um quadro do tamanho atual. O
// buffer é alocado 1× por geometria (fora do caminho por frame).
func (c *gdiCapturer) ensureBannerHistorico() {
	necessario := c.width * c.height * 4
	if necessario <= 0 || (c.bannerPrev != nil && len(c.bannerPrev) == necessario) {
		return
	}
	c.bannerPrev = make([]byte, necessario)
	bannerCapturePrimed.Store(false)
}

// bitbltFullFrame copia o monitor inteiro para a DIB.
func (c *gdiCapturer) bitbltFullFrame(screenDC uintptr) error {
	if err := c.bitbltRegion(
		screenDC,
		winui.Rect{X: 0, Y: 0, W: c.width, H: c.height},
		0, 0,
	); err != nil {
		return err
	}
	procGdiFlush.Call()
	c.rememberFrame()
	return nil
}

// capturaAtiva informa se existe alguma janela do agente para esconder (banner
// ou aviso da bandeja). O histórico só é mantido nesse caso.
func capturaAtiva() bool {
	if bannerCaptureTarget.Load() != 0 {
		return true
	}
	return len(capturaExtras()) != 0
}

// rememberFrame guarda a DIB atual como quadro anterior da emenda. Chamado
// sempre com bannerCaptureMu tomado.
//
// A condição é "existe janela registrada", não "existe banner": uma sessão com
// o aviso da bandeja em tela e sem banner (banner fechado antes do aviso) também
// precisa de histórico — sem ele a emenda nunca liga e o aviso vaza para o
// stream em todos os quadros.
func (c *gdiCapturer) rememberFrame() {
	c.ensureBannerHistorico()
	if c.bannerPrev == nil || c.dibBits == nil || !capturaAtiva() {
		return
	}
	origem := unsafe.Slice((*byte)(c.dibBits), len(c.bannerPrev))
	copy(c.bannerPrev, origem)
	bannerCapturePrimed.Store(true)
}

// bitbltRegion copia uma região do monitor para a mesma posição da DIB.
func (c *gdiCapturer) bitbltRegion(screenDC uintptr, r winui.Rect, dstX, dstY int) error {
	if r.W <= 0 || r.H <= 0 {
		return nil
	}
	ret, _, _ := procBitBlt.Call(
		c.memDC,
		uintptr(dstX), uintptr(dstY),
		uintptr(r.W), uintptr(r.H),
		screenDC,
		uintptr(c.offsetX+r.X), uintptr(c.offsetY+r.Y),
		SRCCOPY,
	)
	if ret == 0 {
		return fmt.Errorf("BitBlt falhou (%dx%d em %d,%d)", r.W, r.H, r.X, r.Y)
	}
	return nil
}

// SupportsBannerPatch indica que este capturer mantém o banner fora do stream
// pela emenda por região (ver cabeçalho deste arquivo).
func (c *gdiCapturer) SupportsBannerPatch() bool { return true }
