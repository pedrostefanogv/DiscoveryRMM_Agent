//go:build windows

package screen

import (
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
)

// ── Banner local da sessão remota (informativo, CLICK-THROUGH) ──
//
// Cartão flutuante topmost no desktop do usuário acessado: operador + tempo de
// sessão. É 100% click-through (WS_EX_LAYERED|WS_EX_TRANSPARENT + HTTRANSPARENT
// no WM_NCHITTEST): o banner NUNCA captura o mouse.
//
// O motivo é direto: o banner é EXCLUÍDO da captura (WDA_EXCLUDEFROMCAPTURE),
// então o operador remoto vê o app por baixo dele e clica "através" do banner.
// Quando a janela capturava o clique (comportamento anterior), clicar em abas
// do navegador — que ficam justamente no topo da tela — simplesmente não
// funcionava, embora o operador não visse o banner.
//
// A ação "Desconectar" saiu do banner e passou para o ícone da bandeja
// (screen.SessionTray): o usuário local encerra a sessão sem que nenhuma janela
// fique por cima dos apps.
//
// Z-ORDER (bug "cartão atrás do menu Iniciar"): WS_EX_TOPMOST no CreateWindowEx
// posiciona a janela na banda topmost apenas NO MOMENTO em que ela é mostrada.
// O menu Iniciar, o Action Center e o UAC vivem em banda SUPERIOR: quando abrem,
// empurram o cartão para trás, e como o banner é WS_EX_NOACTIVATE ele nunca
// ganha foco para voltar sozinho. Por isso o topmost é REAFIRMADO a cada tique
// do timer de 1s (bannerTimerID) com SetWindowPos(HWND_TOPMOST,
// SWP_NOACTIVATE|SWP_NOMOVE|SWP_NOSIZE|SWP_NOOWNERZORDER) — sem roubar foco e
// sem tocar na geometria. Ver o bloco "Reafirmação do topmost" logo abaixo da
// lista de constantes para o racional completo e a limitação aceita (enquanto o
// menu Iniciar estiver aberto o cartão continua atrás, por definição do shell).
//
// O banner continua escondido da captura: o viewer e a gravação não podem vê-lo.
// Ordem de preferência (ver hideBannerFromCapture):
//
//  1. WDA_EXCLUDEFROMCAPTURE (Win10 2004+): a afinidade cobre TODAS as APIs de
//     captura que a respeitam (BitBlt, Windows Graphics Capture, apps externos).
//  2. Máscara de região (banner_patch_windows.go) + emenda do capturer GDI:
//     a máscara é OBRIGATÓRIA quando a captura é Desktop Duplication (DXGI
//     go-d3d), porque o DDA **ignora** a afinidade (medido em
//     banner_exclusion_capture_windows_test.go). Os pixels da área do cartão
//     são lidos ANTES do ShowWindow e o pipeline reescreve essa área em TODO
//     frame. Funciona mesmo em Win10/VM onde SetWindowDisplayAffinity devolve
//     ERROR_NOT_ENOUGH_MEMORY (8).
//  3. WDA_MONITOR (Win7+): o cartão aparece PRETO no stream — recuo de
//     segurança (garantirProtecaoSemMascara) quando não há máscara e o capturer
//     ignora o WDA; é preferível a expor o operador.
//  4. Nada disso: o banner é exibido sem exclusão em vez de simplesmente não
//     existir — o usuário local PRECISA saber que está sendo acessado.
//
// IMPORTANTE: quando WDA_EXCLUDEFROMCAPTURE é aceito, a afinidade NÃO pode ser
// tocada de novo. O bug do "retângulo preto no topo da tela remota" (2026-11-03)
// era exatamente isso: o degrau do WDA_MONITOR rodava depois do EXCLUDE ter
// dado certo e rebaixava a janela para MONITOR (preto em toda captura).
//
// A ÚNICA exceção a essa regra é o DESCARTE da máscara em tempo de execução
// (reaplicarProtecaoAposDescarteMascara): quando a máscara de região é jogada
// fora e o capturer ignora o WDA (Desktop Duplication), manter o EXCLUDE "puro"
// deixaria o cartão VISÍVEL no stream — a afinidade deixa de proteger na prática
// e o recuo para MONITOR (preto) é o mal menor. Ver o bloco "Recuo de afinidade
// no DESCARTE da máscara" no fim deste arquivo.

const (
	// wdaNone/WdaMonitor/WdaExcludeFromCapture são os valores de
	// SetWindowDisplayAffinity usados para esconder o banner da captura:
	// WDA_NONE só funciona com WDA_MONITOR já aplicado (antes exige WDA_NONE);
	// WDA_MONITOR (Win7+) pinta a janela de preto no stream e serve de degrau
	// intermediário para o WDA_EXCLUDEFROMCAPTURE (Win10 2004+).
	wdaNone               = 0x00000000
	wdaMonitor            = 0x00000001
	wdaExcludeFromCapture = 0x00000011

	bannerClassName = "DiscoveryRmmSessionBanner"
	// Medidas de projeto em 96 DPI (100%). O cartão real é escalado pelo DPI
	// efetivo do monitor compartilhado (ver NewSessionBanner/paintMetrics):
	// sem isso, em telas a 125%/150% o banner (e as fontes) ficava
	// proporcionalmente menor que o restante da interface.
	bannerWidth     = 620
	bannerHeight    = 68
	bannerRadius    = 14
	bannerFontPx    = 16
	bannerSubFontPx = 13
	bannerTimerID   = 1
	bannerRefreshMS = 1000
	// bannerAlpha: cartão levemente translúcido sem perder contraste.
	bannerAlpha = 242
)

// ── Reafirmação do topmost (bug do cartão ATRÁS do menu Iniciar) ──
//
// WS_EX_TOPMOST no CreateWindowEx NÃO é uma promessa permanente: ele coloca a
// janela na banda "topmost" da ordem Z **no momento em que ela é mostrada**.
// A partir daí o gerenciador de janelas do shell pode reordenar a banda inteira
// — o menu Iniciar, o Action Center (Win+A), o seletor de tarefas, o UAC e o
// flyout de rede são janelas de banda SUPERIOR (acima de qualquer topmost) e,
// quando abrem, empurram o cartão para trás. Como o banner tem
// WS_EX_NOACTIVATE, ele nunca ganha foco e portanto nunca é "trazido para
// frente" de novo pelo próprio Windows: ficava atrás até a sessão terminar.
//
// Correção: reafirmar a posição na banda topmost de tempos em tempos, com
// SetWindowPos(HWND_TOPMOST, SWP_NOACTIVATE|SWP_NOMOVE|SWP_NOSIZE|
// SWP_NOOWNERZORDER). O HWND_TOPMOST reinsere a janela no TOPO da banda
// topmost — não é um simples "restaurar" da posição anterior.
//
// Por que no TIMER de 1s (bannerRefreshMS) e não em WM_WINDOWPOSCHANGED:
//
//  1. WM_WINDOWPOSCHANGED chega também para as mudanças que NÓS provocamos
//     (MoveResizeWindow na criação, SetLayeredAlpha, SetRoundedRegion). Reagir
//     ali significaria chamar SetWindowPos de dentro do processamento do
//     próprio SetWindowPos — reentrância desnecessária e um vetor de laço.
//  2. O timer já existe para repintar o relógio, então a reafirmação não custa
//     thread, timer nem wakeup novo: é um SetWindowPos por segundo.
//  3. O período de 1s é curto o bastante para o cartão reaparecer assim que o
//     menu Iniciar/UAC fecha, e longo o bastante para não disputar a ordem Z a
//     cada frame — disputar a ordem Z a cada frame é o que produz piscada.
//
// LIMITAÇÃO CONHECIDA E ACEITA: enquanto o menu Iniciar (ou o UAC seguro, ou o
// Action Center) estiver ABERTO, o cartão continua atrás — essas janelas são de
// banda superior e nenhum topmost fica acima delas. O objetivo desta correção é
// o cartão VOLTAR ao topo assim que elas fecharem, e não sobrepor o menu.
const (
	// bannerTopmostMotivoAtivacao/bannerTopmostMotivoReafirmacao nomeiam, no log
	// e nos testes, de onde veio a reafirmação: a criação (mostrar a janela) ou
	// o timer periódico.
	bannerTopmostMotivoAtivacao    = "ativação"
	bannerTopmostMotivoReafirmacao = "reafirmação periódica"
)

// bannerTopmostLogado evita repetir a mesma linha de log a cada segundo. Sem
// isso o arquivo de log da sessão ganharia ~3600 linhas por hora de sessão com
// uma mensagem idêntica — o sintoma de campo (cartão atrás do menu Iniciar)
// ficaria enterrado justamente no log que deveria prová-lo.
var bannerTopmostLogado sync.Map

// reafirmarBannerTopmost devolve o cartão ao topo da banda topmost sem mexer em
// posição, tamanho, foco ou ordem entre janelas do mesmo dono.
//
// SWP_NOACTIVATE é obrigatório: o banner é WS_EX_NOACTIVATE por design (ele não
// pode roubar o foco de teclado de quem o usuário local está usando) e um
// SetWindowPos sem essa flag ativaria a janela, quebrando o comportamento.
// SWP_NOMOVE|SWP_NOSIZE preservam a geometria já escalada pelo DPI.
// SWP_NOOWNERZORDER mantém a posição relativa ao dono (o cartão não tem dono,
// mas a flag documenta a intenção e evita reordenação surpresa no futuro).
//
// A função é um ponto de injeção: bannerTopmostSetWindowPos pode ser trocada nos
// testes para contar as chamadas SEM abrir janela real — o efeito da
// reafirmação (a ordem Z efetiva) não é observável de forma determinística em
// CI, então o contrato testável é "reafirmou, com estes argumentos, e só quando
// precisava".
func reafirmarBannerTopmost(hwnd uintptr, motivo string) {
	if hwnd == 0 {
		return
	}
	bannerTopmostSetWindowPos(hwnd, hwndTopmost, 0, 0, 0, 0,
		swpNecessariasParaNaoQuebrarClickThrough)
	logTopmostUmaVez(hwnd, motivo)
}

// logTopmostUmaVez registra a primeira reafirmação de cada janela. O log serve
// para provar em campo que a reafirmação está rodando (o usuário relata "atrás
// do menu Iniciar" e o log mostra se o timer chegou a disparar), sem inundar o
// arquivo a cada segundo.
func logTopmostUmaVez(hwnd uintptr, motivo string) {
	if _, jaLogado := bannerTopmostLogado.LoadOrStore(hwnd, struct{}{}); jaLogado {
		return
	}
	log.Printf("[session-banner] topmost reafirmado (%s, hwnd=%d)", motivo, hwnd)
}

// esquecerLogTopmost libera a marca de log do HWND. O WM_DESTROY precisa disso:
// o Windows recicla valores de HWND e, sem o esquecimento, o banner de uma
// sessão seguinte poderia herdar a marca e nunca logar sua reafirmação.
func esquecerLogTopmost(hwnd uintptr) {
	bannerTopmostLogado.Delete(hwnd)
}

// Constantes do SetWindowPos de reafirmação do topmost. HWND_TOPMOST é -1 e o
// uintptr precisa ser o valor com sinal estendido (^uintptr(0)), exatamente
// como winui.EnsureTopMost já faz — passar 0xFFFFFFFF sem estender em 64 bits
// seria lido pelo Windows como um HWND inválido.
const (
	hwndTopmost      = ^uintptr(0) // HWND_TOPMOST (-1)
	swpNoSize        = 0x0001      // SWP_NOSIZE
	swpNoMove        = 0x0002      // SWP_NOMOVE
	swpNoActivate    = 0x0010      // SWP_NOACTIVATE
	swpNoOwnerZOrder = 0x0200      // SWP_NOOWNERZORDER
	// swpNecessariasParaNaoQuebrarClickThrough documenta, em um único lugar, as
	// flags que a reafirmação NÃO pode perder: sem SWP_NOACTIVATE o banner rouba
	// foco (ele é WS_EX_NOACTIVATE por design); sem SWP_NOMOVE|SWP_NOSIZE a
	// reafirmação moveria a janela por cima da geometria já escalada pelo DPI.
	// É a máscara verificada pelo teste de regressão.
	swpNecessariasParaNaoQuebrarClickThrough = swpNoSize | swpNoMove | swpNoActivate | swpNoOwnerZOrder
)

// bannerTopmostSetWindowPos é o ponto de injeção dos testes da reafirmação do
// topmost: a ordem Z efetiva não é observável de forma determinística em CI,
// então o que se testa é "o banner pediu a reafirmação, com os argumentos que
// não roubam foco nem mexem na geometria".
//
// A assinatura acompanha a do SetWindowPos nativo (e não a de
// winui.EnsureTopMost) justamente para o teste poder AFIRMAR as flags: se o
// wrapper de conveniência escondesse os argumentos, um SWP_NOACTIVATE removido
// por engano passaria despercebido — e é exatamente essa flag que impede o
// banner de roubar o foco.
var bannerTopmostSetWindowPos = setWindowPosTopmostReal

// setWindowPosTopmostReal aplica a reafirmação com as QUATRO flags de uma vez.
//
// Por que não delegar para winui.EnsureTopMost (que faz quase o mesmo): aquele
// wrapper não passa SWP_NOOWNERZORDER. Delegar deixaria o teste afirmando uma
// flag que o caminho de produção nunca envia — teste verde com comportamento
// diferente, o pior tipo de cobertura. Com o P/Invoke local o binário envia
// exatamente a máscara que o teste valida (swpNecessariasParaNaoQuebrarClickThrough),
// e a diferença fica visível em um único lugar.
//
// O P/Invoke é o mesmo user32.SetWindowPos já resolvido no pacote; não há cópia
// de DLL nem de convenção de HWND_TOPMOST.
func setWindowPosTopmostReal(hwnd, insertAfter, x, y, cx, cy uintptr, flags uintptr) {
	procSetWindowPos.Call(hwnd, insertAfter, x, y, cx, cy, flags)
}

var (
	procSetWindowDisplayAffinity = user32DLL.NewProc("SetWindowDisplayAffinity")
	procGetWindowDisplayAffinity = user32DLL.NewProc("GetWindowDisplayAffinity")
	// procSetWindowPos é a reafirmação do topmost (ver bloco "Reafirmação do
	// topmost"). Local de propósito: winui.EnsureTopMost não passa
	// SWP_NOOWNERZORDER e delegar faria o teste afirmar uma flag que o binário
	// não envia.
	procSetWindowPos = user32DLL.NewProc("SetWindowPos")

	// affinitySet/affinityGet são os pontos de injeção dos testes: a escada de
	// afinidade é a parte mais delicada do banner (o bug do retângulo preto
	// nasceu dela) e precisa de teste de regressão sem janela real.
	affinitySet = func(hwnd uintptr, value uint32) (bool, error) {
		ok, _, callErr := procSetWindowDisplayAffinity.Call(hwnd, uintptr(value))
		if ok == 0 {
			return false, fmt.Errorf("%v (errno=%d)", describeErr(callErr), errnoOf(callErr))
		}
		return true, nil
	}
	affinityGet = func(hwnd uintptr) (uint32, bool) {
		var value uint32
		if hwnd == 0 {
			return 0, false
		}
		if ok, _, _ := procGetWindowDisplayAffinity.Call(hwnd, uintptr(unsafe.Pointer(&value))); ok == 0 {
			// Falha ≠ WDA_NONE: quem chama precisa distinguir "sem afinidade" de
			// "não consegui ler" (o listener não deve reaplicar às cegas).
			return 0, false
		}
		return value, true
	}
)

// bannerCaptureMode descreve como o banner se esconde da captura de tela.
type bannerCaptureMode int

const (
	// bannerCaptureExcluded: WDA_EXCLUDEFROMCAPTURE — o viewer não vê o
	// cartão (Win10 2004+). Cenário ideal.
	bannerCaptureExcluded bannerCaptureMode = iota
	// bannerCaptureBlack: WDA_MONITOR — o cartão aparece PRETO no stream
	// (Win7+). Não vaza o texto, mas ocupa a área.
	bannerCaptureBlack
	// bannerCaptureRegiao: o banner fica na tela física e a CAPTURA contorna a
	// área dele (capturer GDI com emenda por região). Nada do cartão chega ao
	// viewer. Usado quando SetWindowDisplayAffinity é recusado.
	bannerCaptureRegiao
	// bannerCaptureVisible: nenhuma afinidade nem emenda aceita; o cartão
	// aparece no stream. Último recurso, ainda melhor do que não avisar o
	// usuário.
	bannerCaptureVisible
)

// bannerCaptureModeName descreve o modo obtido no log da sessão.
func bannerCaptureModeName(mode bannerCaptureMode) string {
	switch mode {
	case bannerCaptureExcluded:
		return "WDA_EXCLUDEFROMCAPTURE"
	case bannerCaptureBlack:
		return "WDA_MONITOR (preto no stream)"
	case bannerCaptureRegiao:
		return "captura com emenda de região"
	default:
		return "sem exclusão"
	}
}

// hideBannerFromCapture esconde o banner da captura pela melhor técnica que o
// sistema aceitar e devolve o modo obtido + erro (não-nil quando NADA protegeu
// o stream: o banner ficará visível para o operador).
//
// Por que não "tudo ou nada": o WDA_EXCLUDEFROMCAPTURE depende do compositor
// isolar o conteúdo da janela e pode falhar com ERROR_NOT_ENOUGH_MEMORY (8) em
// Win10/VM — e nessa máquina NENHUM valor de afinidade é aceito (ver relato no
// topo do arquivo). Antes disso, a falha cancelava o banner inteiro — o usuário
// acessado ficava sem qualquer aviso. Nunca deixa a sessão sem indicador.
func hideBannerFromCapture(hwnd uintptr) (bannerCaptureMode, error) {
	// ORDEM CORRIGIDA (2026-11-03, 2ª rodada): WDA_EXCLUDEFROMCAPTURE PRIMEIRO.
	//
	// Até o commit eb607ad (02/11) a exclusão nativa era o único mecanismo; a
	// emenda por região foi promovida a "degrau 1" para contornar o Win10/VM que
	// recusa o WDA por completo. Efeito colateral: em máquina onde a emenda/
	// máscara NÃO consegue cobrir a área (capturer DXGI, falha de captura da
	// máscara, VisibleRect indisponível), o banner deixava de ser excluído pelo
	// compositor e passava a APARECER no viewer. Era exatamente a regressão
	// relatada ("antes não aparecia; começou depois dos ajustes de Win10").
	//
	// Agora o caminho nativo volta a ser preferido: ele é o único que o
	// compositor garante em QUALQUER captura (DXGI, BitBlt, gravação), sem
	// depender de o pipeline reescrever a região. A emenda/máscara continua como
	// degrau seguinte, para as máquinas que recusam o WDA.
	ok, excludeErr := affinitySet(hwnd, wdaExcludeFromCapture)
	if ok {
		return bannerCaptureExcluded, nil
	}
	if excludeErr == nil {
		excludeErr = fmt.Errorf("WDA_EXCLUDEFROMCAPTURE recusado pelo Windows")
	}

	// Degrau 2: o próprio pipeline de captura contorna a área do banner. A
	// checagem é POR JANELA (mascaraDisponivel) — a máscara do aviso da bandeja
	// não cobre a área do banner.
	if mascaraDisponivel(hwnd) || emendaDoCapturerDisponivel() {
		return bannerCaptureRegiao, nil
	}
	// Degrau 3: WDA_MONITOR (o cartão aparece preto no stream). A API só troca
	// para MONITOR a partir de WDA_NONE.
	affinitySet(hwnd, wdaNone)
	if ok, _ := affinitySet(hwnd, wdaMonitor); ok {
		return bannerCaptureBlack, nil
	}
	// Nem afinidade nem emenda: o cartão vai aparecer no stream, mas o usuário
	// acessado PRECISA saber que a sessão existe. Melhor avisar (com o erro no
	// log) do que sumir com o banner.
	return bannerCaptureVisible, excludeErr
}

// applyDisplayAffinity aplica SetWindowDisplayAffinity na ordem de preferência
// e devolve o modo EFETIVO obtido + erro acumulado.
//
// REGRA DE OURO: quando WDA_EXCLUDEFROMCAPTURE é aceito, a afinidade NÃO pode
// ser tocada de novo. Qualquer chamada seguinte (WDA_NONE/WDA_MONITOR) desfaz a
// exclusão e a janela passa a ser pintada de PRETO na captura — era exatamente
// esse o bug do "retângulo preto no topo da tela remota" (2026-11-03): o degrau
// do WDA_MONITOR rodava mesmo depois de o EXCLUDE ter dado certo, e o modo
// reportado no log mentia ("WDA_EXCLUDEFROMCAPTURE" com a janela em MONITOR).
func applyDisplayAffinity(hwnd uintptr) (bannerCaptureMode, error) {
	// Degrau 1: WDA_EXCLUDEFROMCAPTURE (Win10 2004+). Sucesso = fim.
	ok, excludeErr := affinitySet(hwnd, wdaExcludeFromCapture)
	if ok {
		return bannerCaptureExcluded, nil
	}
	if excludeErr == nil {
		excludeErr = fmt.Errorf("WDA_EXCLUDEFROMCAPTURE recusado pelo Windows")
	}
	// Degrau 2: WDA_MONITOR (Win7+). A API só troca para MONITOR a partir de
	// WDA_NONE (o EXCLUDE recusado pode ter deixado estado pendente).
	affinitySet(hwnd, wdaNone)
	if ok, _ := affinitySet(hwnd, wdaMonitor); ok {
		return bannerCaptureBlack, nil
	}
	return bannerCaptureVisible, excludeErr
}

// mascaraAtivaPara decide se a máscara de região deve entrar no frame.
//
// A máscara entra SEMPRE que foi capturada, inclusive no modo
// WDA_EXCLUDEFROMCAPTURE. Motivo medido em 2026-11-03 (teste
// TestJanelaExcluidaApareceNaDuplicacao): o **Desktop Duplication (DXGI) NÃO
// respeita SetWindowDisplayAffinity** — uma janela com EXCLUDEFROMCAPTURE
// continua inteira no frame do DDA. Como o pipeline desta versão usa DDA
// (go-d3d), desativar a máscara porque "o compositor já removeu o cartão" era
// exatamente o que deixava o banner aparecer no visualizador.
//
// No caminho GDI a máscara não é aplicada de qualquer forma
// (PatchFrameBannerIfNeeded ignora quem tem emenda própria), então ativar
// sempre não gera artefato lá. Onde o EXCLUDE é honrado por outra API de
// captura, a área mascarada fica com o quadro congelado — efeito aceitável e
// invisível na prática (o conteúdo é o que estava atrás do cartão).
func mascaraAtivaPara(_ bannerCaptureMode, maskErr error) bool {
	return maskErr == nil
}

// garantirProtecaoSemMascara cobre a lacuna que sobrou da correção de
// 2026-11-03: com WDA_EXCLUDEFROMCAPTURE aceito mas SEM máscara capturada, o
// Desktop Duplication (que ignora a afinidade) mostraria o cartão INTEIRO — com
// o nome do operador no stream. Nesse caso, e quando o capturer também não sabe
// emendar, é preferível rebaixar para WDA_MONITOR (área preta): protege a
// informação, mesmo com o custo visual já conhecido.
//
// Devolve o modo efetivo (o mesmo, ou bannerCaptureBlack após o recuo).
func garantirProtecaoSemMascara(hwnd uintptr, mode bannerCaptureMode, maskErr error) bannerCaptureMode {
	if maskErr == nil || mode != bannerCaptureExcluded {
		return mode
	}
	if emendaDoCapturerDisponivel() {
		// GDI contorna a região no BitBlt: o cartão sai do stream sem afinidade.
		return mode
	}
	// A API só troca para MONITOR a partir de WDA_NONE.
	affinitySet(hwnd, wdaNone)
	if ok, _ := affinitySet(hwnd, wdaMonitor); ok {
		log.Printf("[session-banner] AVISO máscara de região indisponível e capturer ignora o WDA: cartão será PRETO no stream (melhor que expor o operador)")
		return bannerCaptureBlack
	}
	log.Printf("[session-banner] AVISO sem máscara e sem afinidade: o cartão pode aparecer no stream (%v)", maskErr)
	return bannerCaptureVisible
}

// displayAffinityAtual lê a afinidade EFETIVA da janela (diagnóstico em campo:
// o log precisa mostrar o que o Windows realmente aplicou, não o que pedimos).
func displayAffinityAtual(hwnd uintptr) (uint32, bool) {
	return affinityGet(hwnd)
}

// afinidadeParaLog formata a afinidade efetiva; "?" quando a leitura falhou.
func afinidadeParaLog(hwnd uintptr) string {
	value, ok := displayAffinityAtual(hwnd)
	if !ok {
		return "?"
	}
	return fmt.Sprintf("%d", value)
}

// BannerOptions descreve o banner a criar. Não há botão: a desconexão é feita
// pelo ícone da bandeja (SessionTray), para não bloquear cliques do app em uso.
type BannerOptions struct {
	Operator        string
	Text            string
	AllowDisconnect bool
	MonitorIndex    int
}

// bannerFonts agrupa as fontes do banner (liberadas no WM_DESTROY).
type bannerFonts struct {
	primary   uintptr
	secondary uintptr
}

func (f bannerFonts) release() {
	winui.DeleteGDIObject(f.primary)
	winui.DeleteGDIObject(f.secondary)
}

// SessionBanner é o cartão flutuante exibido durante a sessão de tela.
type SessionBanner struct {
	mu       sync.Mutex
	hwnd     uintptr
	start    time.Time
	baseText string
	operator string
	allow    bool
	closed   bool
	ready    chan error
	doneCh   chan struct{}

	// Estado de desenho (somente thread da janela).
	theme winui.Theme
	fonts bannerFonts

	// Geometria efetiva em pixels físicos, já escalada pelo DPI do monitor.
	// width/height/radius são preenchidos em NewSessionBanner; scale 0 (ex.:
	// testes que pintam sem criar janela) cai no layout de 96 DPI.
	width  int
	height int
	radius int
	scale  float64
}

// bannerState liga o HWND ao estado Go (o wndproc só recebe uintptrs).
type bannerState struct {
	banner *SessionBanner
}

var bannerStates sync.Map

var (
	bannerClassOnce sync.Once
	bannerClassErr  error
)

func ensureBannerClass() error {
	bannerClassOnce.Do(func() {
		// Sem CS_DROPSHADOW: a sombra do sistema era desenhada deslocada
		// (direita/abaixo) e deixava um borrão escuro atrás do cartão no
		// desktop do usuário acessado. O cartão já tem borda própria.
		bannerClassErr = winui.RegisterWindowClass(
			bannerClassName,
			windows.NewCallback(bannerWndProc),
			0,
		)
	})
	return bannerClassErr
}

// bannerThemeLoader lê o tema do usuário; var para testes de pré-visualização.
var bannerThemeLoader = winui.DetectTheme

func bannerWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	raw, ok := bannerStates.Load(hwnd)
	if !ok {
		return winui.DefWindowProc(hwnd, msg, wparam, lparam)
	}
	state, _ := raw.(*bannerState)
	banner := state.banner

	switch msg {
	case winui.MsgEraseBkgnd:
		// Toda a pintura é feita em WM_PAINT (double buffer).
		return 1
	case winui.MsgPaint:
		hdc, paint := winui.BeginPaint(hwnd)
		banner.paint(hdc)
		winui.EndPaint(hwnd, &paint)
		return 0
	case winui.MsgPrintClient:
		banner.paint(wparam)
		return 0
	case winui.MsgNCHitTest:
		// Cinto e suspensório do WS_EX_TRANSPARENT: qualquer hit-test que ainda
		// chegue aqui atravessa para a janela de baixo.
		return ^uintptr(0) // HTTRANSPARENT
	case winui.MsgTimer:
		// Reafirmação do topmost a cada tique (ver bloco "Reafirmação do
		// topmost" no topo do arquivo): é o que devolve o cartão ao topo depois
		// que o menu Iniciar/Action Center/UAC fecham. Fora do WM_PAINT de
		// propósito — chamar SetWindowPos durante a pintura é efeito colateral
		// dentro do desenho.
		reafirmarBannerTopmost(hwnd, bannerTopmostMotivoReafirmacao)
		winui.Invalidate(hwnd)
		return 0
	case winui.MsgSettingChange:
		// O usuário trocou tema/accent com a sessão aberta: relê e repinta.
		banner.theme = bannerThemeLoader()
		winui.Invalidate(hwnd)
		return 0
	case winui.MsgClose:
		winui.DestroyWindow(hwnd)
		return 0
	case winui.MsgDestroy:
		unregisterBannerCapture(hwnd)
		esquecerLogTopmost(hwnd)
		banner.fonts.release()
		banner.fonts = bannerFonts{}
		bannerStates.Delete(hwnd)
		winui.PostQuitMessage(0)
		return 0
	}
	return winui.DefWindowProc(hwnd, msg, wparam, lparam)
}

// bannerScaleForDPI converte o DPI efetivo do monitor em fator de escala
// (96 DPI = 100%). Clampa em [1, 3]: não encolhe o cartão abaixo do projeto e
// evita tamanhos absurdos em configurações de acessibilidade extremas.
func bannerScaleForDPI(dpi int) float64 {
	return winui.ScaleForDPI(dpi)
}

// scalePx aplica o fator de escala a uma medida de projeto (96 DPI).
func scalePx(value int, scale float64) int {
	return winui.ScalePx(value, scale)
}

// paintMetrics devolve a geometria efetiva usada pelo desenho. Quando o
// SessionBanner é montado manualmente (testes de pintura), cai no layout de
// 96 DPI.
func (b *SessionBanner) paintMetrics() (width, height, radius int, scale float64) {
	scale = b.scale
	if scale <= 0 {
		scale = 1
	}
	width, height, radius = b.width, b.height, b.radius
	if width <= 0 {
		width = bannerWidth
	}
	if height <= 0 {
		height = bannerHeight
	}
	if radius <= 0 {
		radius = bannerRadius
	}
	return width, height, radius, scale
}

// NewSessionBanner cria e exibe o banner na thread dedicada. Retorna erro
// apenas se a janela não puder ser criada; quando a exclusão da captura é
// recusada pelo Windows, o banner é exibido mesmo assim (ver
// hideBannerFromCapture).
//
// O tamanho final (janela, raio, fontes e coordenadas de desenho) é escalado
// pelo DPI efetivo do monitor compartilhado — com PerMonitorV2 o Windows NÃO
// escala a janela sozinho. Mudanças de escala com a sessão já aberta não
// redimensionam o cartão (não há WM_DPICHANGED sendo tratado aqui).
func NewSessionBanner(opts BannerOptions) (*SessionBanner, error) {
	if err := ensureBannerClass(); err != nil {
		return nil, err
	}

	banner := &SessionBanner{
		start:    time.Now(),
		baseText: strings.TrimSpace(opts.Text),
		operator: strings.TrimSpace(opts.Operator),
		allow:    opts.AllowDisconnect,
		ready:    make(chan error, 1),
		doneCh:   make(chan struct{}),
	}

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(banner.doneCh)

		x, y := bannerPosition(opts.MonitorIndex)
		hwnd, err := winui.CreateWindow(
			bannerClassName,
			"Discovery RMM — sessão remota",
			winui.ExStyleTopmost|winui.ExStyleToolWindow|winui.ExStyleNoActivate|
				winui.ExStyleLayered|winui.ExStyleTransparent,
			winui.WinStylePopup|winui.WinStyleClipSiblings,
			x, y, bannerWidth, bannerHeight,
		)
		if err != nil {
			banner.ready <- err
			return
		}
		banner.mu.Lock()
		banner.hwnd = hwnd
		banner.mu.Unlock()
		bannerStates.Store(hwnd, &bannerState{banner: banner})
		// Registra o banner para o capturer GDI contornar a área dele quando o
		// Windows recusar SetWindowDisplayAffinity (Win10/VM). Precisa vir ANTES
		// de hideBannerFromCapture, que consulta CaptureExclusionSupported.
		registerBannerCapture(hwnd)

		// DPI efetivo do monitor compartilhado. Com manifest PerMonitorV2 o
		// Windows não escala a janela sozinho: sem isto o cartão ficava menor
		// que o resto da UI em telas a 125%/150%.
		dpi := winui.DpiForWindow(hwnd)
		scale := bannerScaleForDPI(dpi)
		width := scalePx(bannerWidth, scale)
		height := scalePx(bannerHeight, scale)
		radius := scalePx(bannerRadius, scale)
		banner.width, banner.height, banner.radius, banner.scale = width, height, radius, scale

		// A posição inicial usou a largura de projeto; centraliza de novo com a
		// largura efetiva (escala > 1 alarga o cartão).
		x, y = bannerPositionForSize(opts.MonitorIndex, width, scalePx(8, scale))
		winui.MoveResizeWindow(hwnd, x, y, width, height)

		// Transparência uniforme (a janela é layered por causa do click-through).
		winui.SetLayeredAlpha(hwnd, bannerAlpha)
		winui.SetRoundedRegion(hwnd, width, height, radius)

		// Máscara da região: os pixels da área do cartão são lidos AGORA, com a
		// janela ainda oculta (ShowWindow vem abaixo). É o que permite ao
		// pipeline reescrever a área em qualquer capturer (DXGI inclusive) sem
		// SetWindowDisplayAffinity — que no Desktop Duplication deixa um
		// retângulo PRETO no stream (regressão de 2026-11-03).
		maskErr := capturarMascaraRegiao(hwnd)

		// Fora da captura (degradando quando o Windows não suporta). O banner é
		// exibido mesmo no pior caso: sem aviso o usuário acessado não sabe que
		// há uma sessão aberta (regressão observada no Win10).
		// A decisão vem DEPOIS da máscara: com máscara pronta o modo é sempre
		// "emenda de região" e nenhuma afinidade é aplicada (nada de preto).
		captureMode, captureErr := hideBannerFromCapture(hwnd)
		// Recuo de segurança: EXCLUDE aceito + máscara indisponível + capturer
		// que ignora o WDA (DXGI/DDA) mostraria o cartão inteiro no viewer.
		if captureMode == bannerCaptureExcluded && maskErr != nil {
			captureMode = garantirProtecaoSemMascara(hwnd, captureMode, maskErr)
		}
		// Critério POR JANELA: a emenda/máscara do aviso da bandeja não protege
		// a área do banner (antes o log podia dizer "emenda de região" olhando
		// só para a capacidade global do processo).
		emenda := mascaraDisponivel(hwnd) || emendaDoCapturerDisponivel()
		// A máscara entra em ação sempre que a captura da região deu certo —
		// inclusive com WDA_EXCLUDEFROMCAPTURE, porque o Desktop Duplication
		// (capturer usado nesta versão) ignora a afinidade e mostraria o cartão.
		if mascaraAtivaPara(captureMode, maskErr) {
			ativarMascara(hwnd)
		} else {
			removerMascara(hwnd)
		}
		// A partir daqui o listener de troca de capturer pode reavaliar o modo
		// (ver reavaliarBannerNoCapturer).
		bannerModoDecidido.Store(true)
		switch {
		case captureMode == bannerCaptureRegiao && maskErr == nil:
			log.Printf("[session-banner] banner fora da captura via %s (máscara de região pronta, afinidade=%s)",
				bannerCaptureModeName(captureMode), afinidadeParaLog(hwnd))
		case captureMode == bannerCaptureRegiao:
			log.Printf("[session-banner] banner fora da captura via %s (máscara indisponível: %v)", bannerCaptureModeName(captureMode), maskErr)
		case captureMode == bannerCaptureExcluded && maskErr == nil:
			// Caminho normal: a afinidade cobre as APIs que a respeitam e a
			// máscara cobre o Desktop Duplication, que ignora o WDA.
			log.Printf("[session-banner] banner fora da captura via %s + máscara de região (afinidade efetiva=%s)",
				bannerCaptureModeName(captureMode), afinidadeParaLog(hwnd))
		case captureMode == bannerCaptureExcluded:
			log.Printf("[session-banner] banner fora da captura via %s (afinidade efetiva=%s)",
				bannerCaptureModeName(captureMode), afinidadeParaLog(hwnd))
		case maskErr == nil:
			// Afinidade degradada (MONITOR = preto) ou ausente: a máscara é o
			// que mantém o stream limpo.
			log.Printf("[session-banner] banner fora da captura via %s + máscara de região (afinidade efetiva=%s)",
				bannerCaptureModeName(captureMode), afinidadeParaLog(hwnd))
		case emenda:
			log.Printf("[session-banner] banner fora da captura via captura com emenda de região (afinidade recusada: %v)", captureErr)
		default:
			log.Printf("[session-banner] AVISO exclusão da captura indisponível, banner exibido sem exclusão: %v", captureErr)
		}

		banner.theme = bannerThemeLoader()
		banner.fonts = bannerFonts{
			primary:   winui.CreateFont(scalePx(bannerFontPx, scale), 600),
			secondary: winui.CreateFont(scalePx(bannerSubFontPx, scale), 400),
		}

		winui.ShowWindow(hwnd, winui.SwShowNormal)
		winui.UpdateWindow(hwnd)
		// Reafirma o topmost já na exibição: o ShowWindow entra na banda topmost
		// com o estilo do CreateWindowEx, mas explicitamente aqui o cartão volta
		// ao topo da banda mesmo que algo tenha subido entre a criação e o
		// show (o CreateWindowEx acima acontece antes de máscara/afinidade, e
		// nesse intervalo o shell pode abrir um flyout).
		reafirmarBannerTopmost(hwnd, bannerTopmostMotivoAtivacao)
		winui.SetTimer(hwnd, bannerTimerID, bannerRefreshMS)
		banner.ready <- nil

		var msg winui.Msg
		for winui.NextMessage(&msg) > 0 {
			winui.TranslateMessage(&msg)
			winui.DispatchMessage(&msg)
		}
	}()

	if err := <-banner.ready; err != nil {
		return nil, err
	}
	return banner, nil
}

// bannerPosition centraliza o banner no topo do monitor que está sendo
// compartilhado (monitorIndex do capturer) usando a largura de projeto.
func bannerPosition(monitorIndex int) (x, y int) {
	return bannerPositionForWidth(monitorIndex, bannerWidth)
}

// bannerPositionForWidth é a posição real do banner com a largura efetiva
// (escalada pelo DPI), mantendo a margem superior de projeto (8px @96 DPI).
func bannerPositionForWidth(monitorIndex, width int) (x, y int) {
	return bannerPositionForSize(monitorIndex, width, 8)
}

// bannerPositionForSize centraliza o banner no topo do monitor com largura e
// margem superior em pixels físicos. Sem geometria, usa a área útil do
// monitor primário.
func bannerPositionForSize(monitorIndex, width, topMargin int) (x, y int) {
	monitors, err := GetMonitors()
	if err != nil || len(monitors) == 0 {
		area := winui.WorkArea()
		return area.X + (area.W-width)/2, area.Y + topMargin
	}
	index := monitorIndex
	if index < 0 || index >= len(monitors) {
		index = 0
	}
	target := monitors[index]
	x = target.X + (target.Width-width)/2
	// Monitor mais estreito que o banner: não sai pela esquerda.
	if x < target.X {
		x = target.X
	}
	return x, target.Y + topMargin
}

// primaryText é a linha principal do banner (texto do servidor ou default).
// Quando o template do servidor não traz o operador, o nome é anexado aqui (e
// não na linha de baixo) para o texto de encerramento caber inteiro.
func (b *SessionBanner) primaryText() string {
	text := b.baseText
	if text == "" {
		text = "Sessão remota em andamento"
	}
	if b.operator != "" && !strings.Contains(strings.ToLower(text), strings.ToLower(b.operator)) {
		text += " — " + b.operator
	}
	return text
}

// secondaryText mostra o tempo decorrido e, quando a política permite, como
// encerrar a sessão — com a mesma instrução que o usuário encontra na bandeja.
func (b *SessionBanner) secondaryText() string {
	elapsed := time.Since(b.start)
	clock := fmt.Sprintf("%02d:%02d", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
	if !b.allow {
		return "Conectado há " + clock
	}
	return clock + "  ·  Para encerrar, clique no ícone do agente na bandeja do sistema"
}

// paint desenha o cartão inteiro no DC (memória quando disponível). Todas as
// medidas de projeto (96 DPI) passam por sw(), então o layout acompanha a
// escala do monitor (janela/fontes já vêm escaladas de NewSessionBanner).
func (b *SessionBanner) paint(screenDC uintptr) {
	width, height, radius, scale := b.paintMetrics()
	sw := func(value int) int { return scalePx(value, scale) }

	memDC, bitmap, oldBitmap := winui.BufferedPaint(screenDC, width, height)
	hdc := memDC
	if hdc == 0 {
		hdc = screenDC
	} else {
		defer winui.FlushBufferedPaint(screenDC, memDC, bitmap, oldBitmap, width, height)
	}

	theme := b.theme
	full := winui.Rect{W: width, H: height}
	// O bitmap do double buffer nasce preto: pintar o cliente inteiro com a
	// cor do cartão evita pixels escuros nas bordas/cantos (a região
	// arredondada da janela recorta o contorno).
	winui.FillRect(hdc, full, theme.Card)

	// Borda 1px desenhada 1px para dentro: com o traço exatamente no limite,
	// metade da caneta ficava fora da janela e a linha inferior/direita saía
	// falhada. O raio acompanha o recorte da janela (radius-1).
	inset := sw(1)
	card := winui.Rect{X: inset, Y: inset, W: width - 2*inset, H: height - 2*inset}
	winui.StrokeRoundRectAA(hdc, card, radius-inset, sw(1), theme.Border)

	// Faixa de destaque à esquerda: pílula arredondada com margem vertical.
	// Antes ela ia de ponta a ponta e era recortada pelo canto arredondado,
	// deixando degraus de accent (pixels "serrilhados") nos cantos.
	accentMargin := sw(10)
	winui.FillRoundRectAA(hdc,
		winui.Rect{X: sw(3), Y: accentMargin, W: sw(4), H: height - 2*accentMargin},
		sw(2), theme.Accent)

	// Indicador de sessão ativa.
	winui.FillEllipseAA(hdc, winui.Rect{X: sw(24), Y: sw(28), W: sw(12), H: sw(12)}, theme.Accent)

	textLeft := sw(48)
	textWidth := width - textLeft - sw(24)
	winui.DrawTextSingle(hdc, b.primaryText(),
		winui.Rect{X: textLeft, Y: sw(12), W: textWidth, H: sw(22)}, b.fonts.primary, theme.Text, winui.AlignLeft)
	winui.DrawTextSingle(hdc, b.secondaryText(),
		winui.Rect{X: textLeft, Y: sw(37), W: textWidth, H: sw(18)}, b.fonts.secondary, theme.TextMuted, winui.AlignLeft)
}

// RequestClose pede o encerramento da janela sem bloquear (idempotente): a
// destruição roda na thread dona da janela, então aqui só é postado WM_CLOSE.
//
// É o método usado pelo manager de sessões: Close() pode esperar até 2s pela
// thread da janela e o closeSessionLocked roda com o mutex do manager preso.
func (b *SessionBanner) RequestClose() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	hwnd := b.hwnd
	b.mu.Unlock()

	if hwnd != 0 {
		// O retorno era IGNORADO. Se a fila de mensagens da thread da janela
		// estiver cheia/saturada o PostMessage falha e o WM_CLOSE nunca chega:
		// a janela fica viva para sempre, o WM_DESTROY nunca roda, a máscara
		// nunca é removida (unregisterBannerCapture) e o cartão permanece
		// congelado na região do topo — o "fantasma agarrado" relatado em campo.
		if !winui.PostMessageOK(hwnd, winui.MsgClose, 0, 0) {
			log.Printf("[session-banner] PostMessage(WM_CLOSE) falhou — forçando DestroyWindow")
			winui.DestroyWindow(hwnd)
		}
	}
}

// Close encerra a janela e espera a thread dona destruí-la (até 2s). Use
// RequestClose quando o chamador não puder bloquear (ex.: segurando um mutex).
//
// Watchdog: se a thread da janela não destruir em bannerCloseTimeout, força a
// destruição. Sem isso uma thread bloqueada (PrintWindow de um app travado,
// UAC, troca de desktop) deixaria a janela viva E a máscara registrada — a
// área do cartão continuaria sendo reescrita com pixels mortos na sessão
// seguinte. Forçar DestroyWindow é seguro: se a janela já morreu, a chamada é
// no-op; se ainda existe, o WM_DESTROY dispara o unregister normalmente.
func (b *SessionBanner) Close() {
	if b == nil {
		return
	}
	b.RequestClose()
	if b.doneCh == nil {
		// Banner montado manualmente (sem thread de janela): nada a aguardar.
		return
	}
	select {
	case <-b.doneCh:
		return
	case <-time.After(bannerCloseTimeout):
	}

	// A thread da janela não respondeu: garante o encerramento e a remoção da
	// máscara sem esperar por ela.
	b.mu.Lock()
	hwnd := b.hwnd
	b.mu.Unlock()
	if hwnd == 0 {
		return
	}
	if _, visivel := winui.VisibleRect(hwnd); visivel {
		log.Printf("[session-banner] janela não destruiu em %v — forçando DestroyWindow", bannerCloseTimeout)
		winui.DestroyWindow(hwnd)
	}
	// Independente do DestroyWindow: a máscara não pode sobreviver ao banner.
	unregisterBannerCapture(hwnd)
	// Espera curta pelo WM_DESTROY do DestroyWindow forçado.
	select {
	case <-b.doneCh:
	case <-time.After(500 * time.Millisecond):
		log.Printf("[session-banner] thread da janela ainda não encerrou após destruição forçada")
	}
}

// bannerCloseTimeout é o tempo que Close() espera pela thread da janela antes de
// forçar a destruição. 2s era o valor histórico do select; mantido.
const bannerCloseTimeout = 2 * time.Second

// ── Recuo de afinidade no DESCARTE da máscara (P0 de privacidade) ──
//
// A máscara de região é o que mantém a área do cartão fora do stream quando o
// capturer não emenda (Desktop Duplication/DXGI, o capturer DESTA versão). Ela é
// DESCARTADA em dois caminhos de banner_mask_refresh_windows.go: cobertura
// parcial da faixa e mascaraFalhasParaDescartar falhas consecutivas (~6s). O
// descarte é deliberado — uma máscara meio-atualizada é fantasma permanente.
//
// O PROBLEMA que este gancho fecha: removerMascara só apaga a máscara do
// registry e marca a trava de recaptura. Ele NÃO mexe em
// SetWindowDisplayAffinity. Depois do descarte a área do cartão volta a ser
// capturada AO VIVO; se a afinidade também não protege — e o Desktop Duplication
// IGNORA o WDA (medido em banner_exclusion_capture_windows_test.go) — o CARTÃO
// APARECE NO STREAM com o nome do operador.
//
// Isso contraria a REGRA DE OURO deste arquivo: prefira CONGELAR (preto) a
// VAZAR. O comentário do descarte afirmava ser "igual ao recuo de
// garantirProtecaoSemMascara", mas nenhum caminho de runtime chamava aquela
// função — a proteção era prometida no comentário e inexistente no código.
//
// A correção reusa a escada que já existe: garantirProtecaoSemMascara rebaixa
// WDA_EXCLUDEFROMCAPTURE -> WDA_MONITOR (área PRETA no stream). Preto não
// entrega o nome do operador; cartão visível entrega.
//
// POR QUE O CASO GDI NÃO É DEGRADADO: com emenda própria o capturer contorna a
// região do cartão no próprio BitBlt — o cartão não vaza sem afinidade nenhuma.
// Aplicar WDA_MONITOR ali só pintaria a área de preto de graça, então a função
// sai antes.
//
// POR QUE O GANCHO FICA AQUI E NÃO NO ARQUIVO DO DESCARTE: removerMascara vive em
// banner_patch_windows.go (escopo do mask-dev). O ponto de extensão é a var
// aoDescartarMascara daquele arquivo; a POLÍTICA (o que fazer ao descartar) é
// deste arquivo, que é quem conhece a escada de afinidade e o modo do banner.
// Ver ligarRecuoAposDescarteMascara abaixo para o preenchimento da var.

// reaplicarProtecaoAposDescarteMascara é o recuo de afinidade chamado pelo
// descarte da máscara. Devolve o modo efetivo (útil para teste e log).
//
// Guardas, na ordem:
//
//  1. hwnd == 0: nada a proteger.
//  2. hwnd != bannerCaptureTarget.Load(): o alvo mudou (cartão recriado, sessão
//     encerrada) ou o HWND é de OUTRA janela — hoje o aviso da bandeja
//     (session_tray_balloon) também usa máscara. Aplicar afinidade num handle
//     antigo ou no balloon seria efeito colateral em janela alheia, e o recuo
//     do banner é decisão do banner. Mesma guarda que reavaliarBannerNoCapturer
//     já usa para não agir sobre handle velho.
//  3. emendaDoCapturerDisponivel(): GDI contorna a região sozinho. Não degradar.
//
// Sem máscara, o "maskErr" que alimenta garantirProtecaoSemMascara é sintético:
// a função só usa esse valor para decidir se há proteção de região, e a esta
// altura a resposta é NÃO por construção — a máscara acabou de ser descartada.
// Passar o erro real da captura original seria enganoso (ele pode ser nil, de uma
// captura que funcionou no passado), então o erro sintético documenta o fato
// presente: "esta janela está sem máscara".
func reaplicarProtecaoAposDescarteMascara(hwnd uintptr) bannerCaptureMode {
	if hwnd == 0 {
		return bannerCaptureVisible
	}
	if hwnd != bannerCaptureTarget.Load() {
		// Handle antigo ou janela que não é o banner ativo (ex.: o aviso da
		// bandeja): não é nossa decisão mexer na afinidade dela.
		return bannerCaptureVisible
	}
	if emendaDoCapturerDisponivel() {
		// O capturer contorna a região por conta própria (GDI): o cartão não
		// vaza e rebaixar para MONITOR degradaria a imagem sem motivo.
		log.Printf("[session-banner] máscara descartada, mas o capturer emenda a região (GDI): afinidade mantida, sem recuo")
		return bannerCaptureExcluded
	}
	// errRecuoSemMascara é o "maskErr" sintético do descarte: ver o comentário da
	// função. garantirProtecaoSemMascara só o usa para saber se há região
	// protegendo — e aqui não há mais.
	modo := garantirProtecaoSemMascara(hwnd, bannerCaptureExcluded, errRecuoSemMascara)
	log.Printf("[session-banner] máscara descartada: recuo de afinidade aplicado, modo efetivo %s (afinidade=%s)",
		bannerCaptureModeName(modo), afinidadeParaLog(hwnd))
	return modo
}

// errRecuoSemMascara documenta o estado do descarte para garantirProtecaoSemMascara.
var errRecuoSemMascara = fmt.Errorf("máscara de região descartada em tempo de execução")

// ligarRecuoAposDescarteMascara registra o recuo no ponto de extensão do descarte
// (aoDescartarMascara, definido em banner_patch_windows.go).
//
// Preenchido em init() e não numa var literal porque o alvo é uma var de OUTRO
// arquivo: numa var literal a ordem de inicialização entre arquivos do pacote
// passaria a importar (a var do outro arquivo poderia ser zerada DEPOIS da
// nossa), e um hook silenciosamente nil é exatamente o bug que esta task
// conserta — proteção prometida e ausente. init() roda depois de TODAS as
// inicializações de var do pacote, então o valor nunca é sobrescrito.
func ligarRecuoAposDescarteMascara() {
	aoDescartarMascara = reaplicarProtecaoAposDescarteMascara
}

func init() {
	ligarRecuoAposDescarteMascara()
}
