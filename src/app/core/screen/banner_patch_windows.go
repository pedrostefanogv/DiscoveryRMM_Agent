//go:build windows

package screen

import (
	"fmt"
	"log"
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
	// bannerModoDecidido indica que o banner ativo já escolheu como se esconder
	// da captura. O listener de troca de capturer só age depois disso — antes,
	// quem decide é o próprio NewSessionBanner (evita aplicar afinidade no meio
	// da criação da janela).
	bannerModoDecidido atomic.Bool

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
		// Sem banner ativo não há modo a reavaliar na troca de capturer.
		bannerModoDecidido.Store(false)
	}
	bannerCaptureMu.Unlock()
	removerCapturaExtra(hwnd)
	removerMascara(hwnd)
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

// ── Máscara de região: o quadro limpo anterior ao cartão ──
//
// A emenda do GDI (acima) só existe porque o capturer GDI monta o quadro a
// partir do BitBlt e pode preencher o buraco com o quadro anterior. O DXGI
// (Desktop Duplication) não dá esse controle: o frame chega pronto do
// compositor e, com WDA_EXCLUDEFROMCAPTURE/WDA_MONITOR, a área do cartão vem
// PRETA — o operador via um retângulo preto no topo da tela (regressão de
// 2026-11-03, máquina com GPU onde o capturer é go-d3d/DXGI).
//
// Solução: guardar, ANTES de o cartão aparecer, os pixels da área dele na tela
// física (BitBlt de uma DIB pequena) e reescrever essa área em TODO frame, de
// QUALQUER capturer. O cartão continua 100% visível na tela física e o stream
// recebe o conteúdo que estava ali antes dele — sem preto e sem o cartão.
//
// O conteúdo da região fica congelado enquanto o cartão existir (mesmo
// comportamento da emenda do GDI). Ao fechar o cartão a máscara é descartada e
// a área volta a ser capturada ao vivo.

// mascaraRegiao guarda os pixels da área de uma janela do agente, lidos antes
// de ela ser exibida. `ativa` controla se a máscara entra no frame: capturar os
// pixels é inofensivo, mas aplicar só faz sentido quando NENHUMA afinidade
// (WDA_*) cuida da exclusão — com EXCLUDEFROMCAPTURE o compositor já tira o
// cartão do frame e reescrever a área criaria um artefato congelado.
type mascaraRegiao struct {
	hwnd  uintptr
	rect  winui.Rect // coordenadas físicas do desktop virtual
	px    []byte     // BGRA top-down, rect.W*4 por linha
	ativa bool
}

var (
	mascarasMu sync.Mutex
	mascaras   = map[uintptr]*mascaraRegiao{}
	// mascaraVisivel diz se o cartão ainda está na tela. Var para os testes
	// poderem usar HWNDs sintéticos (sem janela real).
	mascaraVisivel = func(hwnd uintptr) bool {
		_, ok := winui.VisibleRect(hwnd)
		return ok
	}
)

// capturarMascaraRegiao lê os pixels da área da janela na tela física e guarda
// como máscara. DEVE ser chamada com a janela ainda NÃO visível (antes de
// ShowWindow): é isso que garante que a máscara contenha o conteúdo de baixo,
// não o próprio cartão.
func capturarMascaraRegiao(hwnd uintptr) error {
	rect, ok := winui.WindowRect(hwnd)
	if !ok {
		return fmt.Errorf("janela sem retângulo válido")
	}
	// Defensivo: área absurda é sinal de handle reciclado.
	if rect.W > 32768 || rect.H > 32768 {
		return fmt.Errorf("área da janela absurda (%dx%d)", rect.W, rect.H)
	}
	px, err := lerPixelsTela(rect)
	if err != nil {
		return err
	}
	mascarasMu.Lock()
	mascaras[hwnd] = &mascaraRegiao{hwnd: hwnd, rect: rect, px: px}
	mascarasMu.Unlock()
	return nil
}

// lerPixelsTela copia os pixels de uma região da tela física para um buffer
// BGRA top-down. Usa DIB section (pixels em memória de sistema) para não
// depender de GetDIBits, que já falhou em campo (ver capturer_gdi.go).
func lerPixelsTela(r winui.Rect) ([]byte, error) {
	if r.Empty() {
		return nil, fmt.Errorf("região vazia")
	}
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC falhou")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC falhou")
	}
	defer procDeleteDC.Call(memDC)

	// BITMAPINFO de 40 bytes, biHeight negativo = top-down (mesma ordem do
	// Frame entregue ao pipeline).
	var bi [40]byte
	bi[0] = 40
	*(*int32)(unsafe.Pointer(&bi[4])) = int32(r.W)
	*(*int32)(unsafe.Pointer(&bi[8])) = -int32(r.H)
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
		return nil, fmt.Errorf("CreateDIBSection falhou (%dx%d)", r.W, r.H)
	}
	prev, _, _ := procSelectObject.Call(memDC, hbm)
	ret, _, _ := procBitBlt.Call(
		memDC, 0, 0,
		uintptr(r.W), uintptr(r.H),
		screenDC, uintptr(r.X), uintptr(r.Y),
		SRCCOPY,
	)
	procGdiFlush.Call()
	out := make([]byte, r.W*r.H*4)
	if ret != 0 {
		copy(out, unsafe.Slice((*byte)(bits), len(out)))
	}
	procSelectObject.Call(memDC, prev)
	procDeleteObject.Call(hbm)
	if ret == 0 {
		return nil, fmt.Errorf("BitBlt da máscara falhou (%dx%d em %d,%d)", r.W, r.H, r.X, r.Y)
	}
	return out, nil
}

// removerMascara descarta a máscara de uma janela (idempotente).
func removerMascara(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	mascarasMu.Lock()
	delete(mascaras, hwnd)
	restantes := len(mascaras)
	mascarasMu.Unlock()
	if restantes == 0 {
		// Sem máscara não há o que atualizar: encerra o loop de refresh.
		pararRefreshMascaras()
	}
}

// RegionMaskAvailable informa se existe máscara pronta para reescrever a área
// de algum cartão do agente no frame. Quando existe, o banner NÃO precisa de
// SetWindowDisplayAffinity: o pipeline esconde a área sem o retângulo preto.
func RegionMaskAvailable() bool {
	mascarasMu.Lock()
	defer mascarasMu.Unlock()
	return len(mascaras) > 0
}

// ativarMascara liga a aplicação da máscara no frame (idempotente).
func ativarMascara(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	mascarasMu.Lock()
	ativou := false
	if m := mascaras[hwnd]; m != nil {
		m.ativa = true
		ativou = true
	}
	mascarasMu.Unlock()
	if ativou {
		// Conteúdo ao vivo atrás do cartão (PrintWindow), em vez de congelado.
		iniciarRefreshMascaras()
	}
}

// PatchFrameBanner reescreve, no frame, as áreas dos cartões do agente com a
// máscara capturada antes de eles aparecerem. É o equivalente DXGI da emenda do
// GDI: o cartão fica na tela física e o stream recebe o conteúdo de baixo.
//
// Só atua em frame BGRA 32bpp (o contrato do pipeline). Frame HDR
// (R16G16B16A16_FLOAT) é ignorado: a máscara é BGRA e converter aqui duplicaria
// o tone mapping. Em sessão HDR o pipeline chama PatchFrameBannerAt DEPOIS do
// tone mapping, passando a origem física guardada antes da conversão.
func PatchFrameBanner(frame *Frame) {
	if frame == nil {
		return
	}
	PatchFrameBannerAt(frame, frame.OriginX, frame.OriginY)
}

// PatchFrameBannerAt é PatchFrameBanner com a origem física EXPLÍCITA. Existe
// porque o tone mapping HDR (ToneMapHDRToSDR) recria o Frame e zera
// OriginX/OriginY: sem passar a origem original, a máscara cairia no canto
// errado em monitor secundário.
func PatchFrameBannerAt(frame *Frame, originX, originY int) {
	if frame == nil || frame.Width <= 0 || frame.Height <= 0 || len(frame.Data) == 0 {
		return
	}
	if frame.ColorSpace != 0 {
		return
	}
	stride := frame.Stride
	if stride <= 0 {
		stride = frame.Width * 4
	}
	if stride < frame.Width*4 {
		return
	}
	altura := len(frame.Data) / stride
	if altura > frame.Height {
		altura = frame.Height
	}
	if altura <= 0 {
		return
	}

	mascarasMu.Lock()
	lista := make([]*mascaraRegiao, 0, len(mascaras))
	for _, m := range mascaras {
		if m.ativa {
			lista = append(lista, m)
		}
	}
	mascarasMu.Unlock()
	if len(lista) == 0 {
		return
	}

	quadro := winui.Rect{X: originX, Y: originY, W: frame.Width, H: altura}
	for _, m := range lista {
		// Cartão oculto (fim de sessão, desktop bloqueado, janela escondida):
		// não reescreve nada — a área deve voltar a ser capturada ao vivo.
		if !mascaraVisivel(m.hwnd) {
			continue
		}
		inter := quadro.Intersect(m.rect)
		if inter.Empty() {
			continue
		}
		mx := inter.X - m.rect.X
		my := inter.Y - m.rect.Y
		fx := inter.X - originX
		fy := inter.Y - originY
		bytesLinha := inter.W * 4
		for y := 0; y < inter.H; y++ {
			offM := (my+y)*m.rect.W*4 + mx*4
			offF := (fy+y)*stride + fx*4
			if offM+bytesLinha > len(m.px) || offF+bytesLinha > len(frame.Data) {
				break
			}
			copy(frame.Data[offF:offF+bytesLinha], m.px[offM:offM+bytesLinha])
		}
	}
}

// PatchFrameBannerIfNeeded aplica a máscara apenas quando o capturer NÃO faz a
// emenda por conta própria. Hoje só o GDI contorna a região durante o BitBlt
// (SupportsBannerPatch); DXGI (go-d3d e manual) entrega o frame pronto e precisa
// desta reescrita.
func PatchFrameBannerIfNeeded(c Capturer, frame *Frame) {
	if frame == nil {
		return
	}
	PatchFrameBannerIfNeededAt(c, frame, frame.OriginX, frame.OriginY)
}

// PatchFrameBannerIfNeededAt é PatchFrameBannerIfNeeded com origem explícita
// (usado depois do tone mapping HDR, que zera OriginX/OriginY).
func PatchFrameBannerIfNeededAt(c Capturer, frame *Frame, originX, originY int) {
	if c != nil && capturerSupportsBannerPatching(c) {
		return
	}
	PatchFrameBannerAt(frame, originX, originY)
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
	// Máscara de região pronta: o pipeline reescreve a área do cartão em
	// QUALQUER capturer (ver PatchFrameBannerIfNeeded). É o caminho preferido
	// porque não depende de SetWindowDisplayAffinity — que no DXGI deixa a área
	// PRETA no stream.
	if RegionMaskAvailable() {
		return true
	}
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

	// O capturer pode trocar no meio da sessão (fallback GDI↔DXGI, troca de
	// desktop, recriação por falha). O modo de exclusão do banner foi decidido
	// no nascimento; reavaliar aqui evita o cartão vazar VISÍVEL quando o
	// capturer novo não sabe emendar e nenhuma afinidade foi aplicada.
	reavaliarBannerNoCapturer()
}

// reavaliarBannerNoCapturer reaplica a escada de afinidade quando o capturer
// ativo muda e o banner ficaria sem proteção.
//
// Só age quando TODAS as condições valem:
//   - o banner já decidiu o modo (bannerModoDecidido);
//   - não existe máscara de região (a máscara vale para qualquer capturer);
//   - o capturer novo NÃO emenda por conta própria (DXGI);
//   - a janela está sem afinidade (WDA_NONE) — se já há EXCLUDE/MONITOR, nada a
//     fazer (e reaplicar EXCLUDE depois de MONITOR não é permitido pela API).
func reavaliarBannerNoCapturer() {
	if !bannerModoDecidido.Load() {
		return
	}
	hwnd := bannerCaptureTarget.Load()
	if hwnd == 0 || RegionMaskAvailable() {
		return
	}
	capturerAtualMu.Lock()
	atual := capturerAtual
	capturerAtualMu.Unlock()
	if capturerSupportsBannerPatching(atual) {
		return
	}
	if displayAffinityAtual(hwnd) != wdaNone {
		return
	}
	mode, err := applyDisplayAffinity(hwnd)
	if err != nil {
		log.Printf("[session-banner] capturer trocado (%s) e sem máscara: afinidade indisponível (%v) — cartão pode aparecer no stream",
			capturerName(atual), err)
		return
	}
	log.Printf("[session-banner] capturer trocado (%s): afinidade reaplicada (%s, efetiva=%d)",
		capturerName(atual), bannerCaptureModeName(mode), displayAffinityAtual(hwnd))
}

// capturerName devolve o nome do capturer para log (vazio quando nil).
func capturerName(c Capturer) string {
	if c == nil || capturerNulo(c) {
		return "nenhum"
	}
	return c.Name()
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
		// tamanho): quadro completo. A máscara é aplicada ANTES de o quadro
		// virar histórico — sem isso o cartão entrava no histórico e a emenda o
		// repetia para sempre (vazamento de 1 quadro que, na prática, era
		// permanente quando o banner nascia antes do capturer).
		return c.bitbltFullFrameComMascara(screenDC)
	}

	regioes := c.regioesForaDaCaptura(alvo)
	if len(regioes) == 0 {
		// Janelas invisíveis agora (minimizadas, tela bloqueada, monitor
		// trocado): nada a esconder neste quadro.
		return c.bitbltFullFrameComMascara(screenDC)
	}

	plano, ok := planoBannerMulti(frame, regioes)
	if !ok {
		// Janelas cobrindo o monitor inteiro ou fora dele: não há caminho sem
		// elas. Mantém o histórico atualizado para a próxima emenda.
		return c.bitbltFullFrameComMascara(screenDC)
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
	// Cinto e suspensório: a máscara (quando ativa) garante a área do cartão
	// limpa mesmo que o histórico esteja contaminado por um quadro antigo.
	c.aplicarMascaraNoDIB()
	c.rememberFrame()
	return nil
}

// aplicarMascaraNoDIB reescreve as áreas mascaradas direto na DIB do capturer.
// Chamado com bannerCaptureMu tomado (mesma ordem de lock do unregister).
func (c *gdiCapturer) aplicarMascaraNoDIB() {
	if c.dibBits == nil || c.width <= 0 || c.height <= 0 {
		return
	}
	frame := &Frame{
		Data:    unsafe.Slice((*byte)(c.dibBits), c.width*c.height*4),
		Width:   c.width,
		Height:  c.height,
		Stride:  c.width * 4,
		OriginX: c.offsetX,
		OriginY: c.offsetY,
	}
	PatchFrameBanner(frame)
}

// bitbltFullFrameComMascara copia o quadro inteiro, aplica a máscara e só então
// registra o histórico — o cartão nunca entra no histórico da emenda.
func (c *gdiCapturer) bitbltFullFrameComMascara(screenDC uintptr) error {
	if err := c.bitbltRegion(
		screenDC,
		winui.Rect{X: 0, Y: 0, W: c.width, H: c.height},
		0, 0,
	); err != nil {
		return err
	}
	procGdiFlush.Call()
	c.aplicarMascaraNoDIB()
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
