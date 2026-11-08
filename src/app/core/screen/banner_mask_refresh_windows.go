//go:build windows

package screen

import (
	"log"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
)

// ── Refresh da máscara com o conteúdo AO VIVO atrás do cartão ──
//
// A máscara (banner_patch_windows.go) é lida uma única vez, antes de o cartão
// aparecer: o stream recebe o conteúdo daquele instante congelado. Para o caso
// comum (barra de título, papel de parede, app parado) isso é invisível, mas um
// vídeo/animação sob o cartão ficaria parado no stream.
//
// Este arquivo atualiza a máscara periodicamente com PrintWindow das janelas que
// estão ATRÁS do cartão. PrintWindow(PW_RENDERFULLCONTENT) renderiza o conteúdo
// PRÓPRIO da janela (sem as janelas por cima), que é exatamente o que o operador
// deveria ver naquela faixa.
//
// ── As duas falhas de projeto que deixavam o fantasma "agarrado" ──
//
// (1) A FAIXA INTEIRA DO CARTÃO PRECISA SER ATUALIZADA, NÃO SÓ A INTERSEÇÃO.
//     A versão anterior escolhia UMA janela (a primeira da ordem Z sob o ponto
//     CENTRAL do cartão) e escrevia apenas a interseção entre o retângulo do
//     cartão e o da janela. Quando a janela de trás cobria só PARTE do cartão —
//     que é o caso normal, porque o cartão é centralizado no topo e uma janela
//     de tamanho médio cobre o centro e deixa de fora as duas pontas — as faixas
//     laterais nunca eram escritas por ninguém, em refresh nenhum. Elas ficavam
//     com os pixels do instante da criação do cartão PARA SEMPRE: é literalmente
//     o "retângulo morto agarrado" relatado em campo, e era permanente POR
//     CONSTRUÇÃO, não por falha de captura.
//
//     Correção: percorrer a ordem Z e COMPOR a faixa do cartão com as janelas
//     que a cobrem, em ordem de cima para baixo. Cada janela contribui apenas
//     com a área que ainda não foi coberta por uma janela mais alta, e janelas
//     de banda superior (WS_EX_TOPMOST) são puladas porque na captura de tela
//     real elas apareceriam SOBRE o cartão — o cartão não é a janela mais alta
//     do desktop (o banner é topmost, mas o menu Iniciar/UAC também é, e fica
//     acima dele).
//
// (2) O DESKTOP NÃO É UMA FONTE VÁLIDA E A ÁREA NÃO PODE FICAR PELA METADE.
//     Progman/WorkerW são as janelas de papel de parede: cobrem o monitor
//     inteiro e o PrintWindow delas devolve o wallpaper — estático. Aceitar isso
//     como "conteúdo ao vivo" congelava a faixa legitimamente e para sempre (e
//     ainda por cima é inútil: a máscara guarda justamente uma cópia do
//     wallpaper). Elas são puladas por serem topmost (Progman é topmost).
//
//     Se depois de compor tudo ainda sobrar área da faixa do cartão, a máscara
//     ficou PARCIAL: uma parte dela seria atualizada e a outra não, e uma
//     máscara meio-atualizada é indistinguível de um fantasma. Nesse caso a
//     máscara é DESCARTADA no mesmo ciclo (não em 3 falhas × 2s) e a área volta
//     a ser capturada ao vivo pelo pipeline — o cartão reaparece no stream por 1
//     quadro, exatamente como no recuo já existente de
//     garantirProtecaoSemMascara e no descarte por falhas consecutivas.
//     Congelar para sempre é pior.
//
// Regras de segurança:
//   - o cartão do agente NUNCA é usado como fonte (nunca vaza para o stream);
//   - janelas topmost não entram na composição (o cartão pode estar atrás delas);
//   - nunca segura mascarasMu durante o PrintWindow (um app travado não pode
//     congelar o pipeline de captura);
//   - resultado suspeito (tudo preto) é DESCARTADO e a máscara anterior é
//     mantida — alguns apps (UWP/GPU) devolvem bitmap vazio;
//   - só atualiza máscara ATIVA e de janela visível;
//   - o loop se encerra sozinho quando não há mais máscara ativa.

const (
	// pwRenderFullContent faz o PrintWindow usar o conteúdo composto pelo DWM
	// (necessário para apps modernos; sem ele muitos devolvem branco).
	pwRenderFullContent = 0x00000002
	// gwHwndNext percorre a ordem Z (GetWindow).
	gwHwndNext = 2
	// gwlExStyle lê os estilos estendidos (WS_EX_TOPMOST = 0x8).
	gwlExStyle = -20
	// wsExTopmost marca a janela na banda TOPMOST da ordem Z.
	wsExTopmost = 0x00000008
	// gaRoot sobe até a janela top-level (GetAncestor).
	gaRoot = 2
)

// ── Ritmo do refresh (adaptativo) ──
//
// O refresh custa GDI (um DC + uma DIB section + N PrintWindow + N BitBlt) e roda
// numa goroutine própria, SEM tocar no caminho por frame do pipeline. O que o
// operador percebe é o ATRASO: com o intervalo fixo de 2s, uma animação sob o
// cartão chegava ao viewer até 2s velha — o "agarrado" relatado em campo.
//
// O intervalo agora é adaptativo e OLHA PARA O CUSTO MEDIDO do último ciclo, não
// para um limiar de mudança de conteúdo (que exigiria comparar pixels e/ou
// converter o DIB para um formato em que o Go consiga ler com sanidade — o
// contrato de ordem de canais do DIB é justamente o que o pipeline normaliza em
// tempo de execução, ver channel_order.go; um falso "mudou" a cada ciclo
// transformaria a economia em custo puro):
//
//   - ciclo barato (poucas janelas impressas)  → 500ms: o atraso cai de 2s para
//     no máximo ~0,5s, que é o que se percebe como "ao vivo";
//   - ciclo caro (várias janelas/áreas grandes) → volta ao intervalo antigo de
//     2s: o custo continua limitado por construção, como antes;
//   - nenhuma janela compôs nada (desktop puro) → 2s: não há conteúdo novo para
//     buscar, então não se paga por nada.
//
// Custo por ciclo no caso real do cartão (620×68): a faixa tem ~42 mil pixels
// (~168 KB), o BitBlt/PrintWindow de uma janela de tamanho médio custa ~1-2 ms de
// CPU e a composição por trechos é um memcpy proporcional à área (o buffer da
// faixa é reaproveitado entre ciclos). Com 2 janelas impressas a 500ms isso dá
// ~1% de UM núcleo. O caso patológico (janela que cobre a faixa inteira vinda de
// um monitor 4K) é limitado pelo teto de pixels e empurra o intervalo para 2s —
// ou seja, o teto de custo é o mesmo de antes desta mudança.
const (
	// mascaraRefreshIntervaloRapido/mascaraRefreshIntervaloLento são os dois
	// regimes do ritmo adaptativo.
	mascaraRefreshIntervaloRapido = 500 * time.Millisecond
	mascaraRefreshIntervaloLento  = 2 * time.Second
	// mascaraRefreshCustoBarato é o custo (em pixels impressos no ciclo) abaixo
	// do qual vale a pena ir para o regime rápido. 4 MP ≈ 16 MB de BitBlt por
	// ciclo ≈ 2 ms de memcpy: barato o bastante para rodar a 500ms.
	mascaraRefreshCustoBarato = 4_000_000
	// mascaraRefreshMaxJanelas limita quantas janelas da ordem Z podem compor a
	// faixa num ciclo. Cartão no topo da tela: 6 é folgado para o caso real e
	// mantém o pior caso (N PrintWindows por ciclo) sob controle.
	mascaraRefreshMaxJanelas = 6
)

var (
	procGetTopWindow    = user32.NewProc("GetTopWindow")
	procGetWindow       = user32.NewProc("GetWindow")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
	procIsHungAppWindow = user32.NewProc("IsHungAppWindow")
	procPrintWindow     = user32.NewProc("PrintWindow")
	procGetWindowLong   = user32.NewProc("GetWindowLongPtrW")
	procGetAncestor     = user32.NewProc("GetAncestor")
	procGetClassName    = user32.NewProc("GetClassNameW")

	mascaraRefreshMu   sync.Mutex
	mascaraRefreshStop chan struct{}
	// mascaraRefreshAtivo permite desligar o refresh (testes/diagnóstico).
	mascaraRefreshAtivo = true
)

// mascaraRefreshMaxPixels limita o tamanho da janela impressa (12 MP ≈ 48 MB).
// Janela maior que isso (8K) não compensa o custo: ela é PULADA na composição —
// o mesmo comportamento de antes (máscara congelada), só que agora o buraco é
// contabilizado e, se ficar parcial, a máscara é descartada em vez de virar
// fantasma.
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
		// Dois buffers, de donos distintos (ver imprimirJanela):
		//   bufFaixa — a faixa composta (tamanho da região do cartão);
		//   bufJanela — o DIB da janela impressa (tamanho da maior janela).
		// São SEPARADOS de propósito: imprimir uma janela não pode destruir a
		// composição já feita (bug real: com buffer único, o PrintWindow de uma
		// janela sobrescrevia o início da faixa — conteúdo riscado/branco).
		// Cada buffer é DA GERAÇÃO: quando o loop é reiniciado com outro ainda
		// preso em PrintWindow, os dois não compartilham memória.
		var bufFaixa, bufJanela []byte
		intervalo := mascaraRefreshIntervaloRapido
		timer := time.NewTimer(intervalo)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-timer.C:
				ativo, custo := atualizarMascarasAoVivo(&bufFaixa, &bufJanela)
				if !ativo {
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
				// time.Timer com Reset (e não Ticker): o intervalo muda de
				// ciclo para ciclo conforme o custo medido.
				intervalo = mascaraRefreshIntervaloAdaptativo(custo)
				timer.Reset(intervalo)
			}
		}
	}()
}

// mascaraRefreshIntervaloAdaptativo escolhe o próximo intervalo a partir do custo
// (pixels impressos) do ciclo que acabou de rodar. Ver o bloco de comentário
// acima das constantes para o porquê de cada regime.
func mascaraRefreshIntervaloAdaptativo(custo int) time.Duration {
	if custo <= 0 || custo > mascaraRefreshCustoBarato {
		return mascaraRefreshIntervaloLento
	}
	return mascaraRefreshIntervaloRapido
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

// atualizarMascarasAoVivo atualiza todas as máscaras ativas e devolve
// (ativo, custo): ativo=true enquanto houver pelo menos uma máscara (o loop usa
// isso para se encerrar) e custo = pixels impressos no ciclo, que alimenta o
// ritmo adaptativo. bufFaixa é o tampão da faixa composta; bufJanela é o DIB
// das janelas impressas — buffers separados, ver o comentário no loop.
func atualizarMascarasAoVivo(bufFaixa, bufJanela *[]byte) (bool, int) {
	mascarasMu.Lock()
	alvos := make([]*mascaraRegiao, 0, len(mascaras))
	for _, m := range mascaras {
		if m.ativa {
			alvos = append(alvos, m)
		}
	}
	mascarasMu.Unlock()
	if len(alvos) == 0 {
		return false, 0
	}

	custo := 0
	for _, m := range alvos {
		if !mascaraVisivel(m.hwnd) {
			continue
		}
		custo += atualizarMascaraAoVivo(m, bufFaixa, bufJanela)
	}
	return true, custo
}

// mascaraFalhasParaDescartar é o número de refreshes consecutivos sem conteúdo
// válido a partir do qual a máscara é DESCARTADA. Serve para falhas de captura
// (app travado, PrintWindow recusado, bitmap preto) — o caso "máscara PARCIAL"
// NÃO espera por este contador: ver atualizarMascaraAoVivo. Com o ritmo
// adaptativo, 3 falhas podem levar 1,5s (regime rápido) ou 6s (regime lento);
// nos dois casos o retângulo morto não sobrevive.
const mascaraFalhasParaDescartar = 3

// atualizarMascaraAoVivo tenta substituir os pixels da faixa do cartão pelo
// conteúdo atual das janelas que estão atrás dele. Devolve o custo do ciclo
// (pixels efetivamente impressos), usado pelo ritmo adaptativo.
//
// Em falha de captura, mantém a máscara — até mascaraFalhasParaDescartar falhas
// consecutivas; depois disso descarta a máscara e devolve a área à captura ao
// vivo (ver registrarFalhaMascara).
func atualizarMascaraAoVivo(m *mascaraRegiao, bufFaixa, bufJanela *[]byte) int {
	// Snapshot do retângulo sob lock: o resto da função trabalha com ele e o
	// commit confere no fim se a máscara ainda é a mesma (compare-and-swap).
	mascarasMu.Lock()
	atual := mascaras[m.hwnd]
	if atual == nil || !atual.ativa {
		mascarasMu.Unlock()
		return 0
	}
	rect := atual.rect
	pxAntigo := atual.px
	mascarasMu.Unlock()
	if rect.Empty() || len(pxAntigo) != rect.W*rect.H*4 {
		// Retângulo degenerado ou buffer de tamanho inesperado (handle
		// reciclado): não há como compor com segurança.
		registrarFalhaMascara(m, "retângulo da máscara inválido")
		return 0
	}

	// Camadas de janelas que compõem a faixa, de cima para baixo na ordem Z.
	// Inclui os cartões do agente (banner/aviso), marcados com cartao=true: eles
	// nunca podem ser fonte (vazariam o cartão), mas precisam ser PULADOS — e não
	// tratados como "a primeira janela que contém o ponto", que era o bug
	// original. O papel de parede (Progman/WorkerW) entra como ÚLTIMA camada.
	camadas := camadasSobreRegiao(rect)
	if len(camadas) == 0 {
		registrarFalhaMascara(m, "nenhuma janela sob o cartão")
		return 0
	}

	// Tampão do tamanho EXATO da faixa do cartão: o refresh passa a atualizar a
	// máscara INTEIRA, não a interseção com uma janela escolhida.
	//
	// O teto de pixels vale aqui só como defesa contra um retângulo absurdo
	// (handle reciclado): a faixa é limitada por mascaraMaxPixels na criação da
	// máscara.
	if rect.W*rect.H > mascaraMaxPixels {
		registrarFalhaMascara(m, "faixa do cartão acima do teto de pixels")
		return 0
	}
	// Reaproveita o tampão entre ciclos quando o tamanho é o mesmo. O buffer de
	// impressão das janelas (bufJanela) é OUTRO tampão, de dono separado: nunca
	// pode aliasar a faixa (ver comentário no loop do refresh).
	if cap(*bufFaixa) < len(pxAntigo) {
		*bufFaixa = make([]byte, len(pxAntigo))
	}
	faixa := (*bufFaixa)[:len(pxAntigo)]

	coberto, custo, algumaFonte := comporCamadasNaFaixa(camadas, rect, faixa, bufJanela)

	if !algumaFonte {
		registrarFalhaMascara(m, "nenhuma janela sob o cartão pôde ser impressa")
		return custo
	}
	// A faixa INTEIRA está coberta? Se não, a máscara ficaria meio-atualizada:
	// metade com o conteúdo de agora e metade com os pixels da criação do
	// cartão. Isso não é "congelar" — é fantasma permanente, exatamente o que
	// estamos corrigindo. Descartar devolve a área à captura ao vivo no próximo
	// frame (o cartão reaparece por 1 quadro, igual ao recuo já existente em
	// garantirProtecaoSemMascara).
	if !regiaoCoberta(coberto, rect) {
		log.Printf("[session-banner] máscara PARCIAL (as janelas de trás não cobrem toda a faixa do cartão) — descartando a máscara e voltando à captura ao vivo")
		removerMascara(m.hwnd)
		return custo
	}
	// Validação: um app UWP/GPU pode devolver bitmap todo preto SEM falhar.
	// Comparar com o conteúdo ANTERIOR da área é o que impede o refresh de
	// aceitar um recorte morto — preto só é aceito quando a área já era preta
	// (vídeo/tema escuro), nunca quando ele APAGARIA conteúdo vivo.
	if !regiaoUtil(faixa, pxAntigo) {
		registrarFalhaMascara(m, "bitmap vazio/preto")
		return custo
	}
	m.falhasConsecutivas = 0
	logMascaraRefreshSucesso(m.hwnd)

	// Composição idêntica à máscara atual: nada a commitar. Evita o
	// copy-on-write (alocar + trocar sob lock) a cada ciclo quando o conteúdo
	// de trás está parado — o caso comum (wallpaper sob o cartão). Sem o skip,
	// o GC pagaria uma alocação + cópia por ciclo por máscara. A comparação
	// ignora o canal ALFA: a máscara nasce de BitBlt (alfa definido) e a
	// composição vem de PrintWindow (alfa frequentemente diferente) — só o RGB
	// é conteúdo; comparar alfa faria o skip nunca disparar.
	if faixaIgualRGB(faixa, pxAntigo) {
		return custo
	}

	mascarasMu.Lock()
	// COPY-ON-WRITE: monta um buffer novo e troca o slice sob lock. O patcher
	// (PatchFrameBanner) lê o slice antigo sem lock — trocar o conteúdo no lugar
	// seria uma corrida de dados (pixels rasgados no stream).
	novo := make([]byte, len(faixa))
	copy(novo, faixa)
	// Compare-and-swap: entre o snapshot e agora o banner pode ter sido
	// reposicionado (troca de monitor/DPI) ou recriado. Gravar a faixa no
	// retângulo novo pintaria lixo — e lixo na área do cartão é vazamento.
	if atual := mascaras[m.hwnd]; atual != nil && atual.ativa && atual.rect == rect {
		atual.px = novo
	}
	mascarasMu.Unlock()
	return custo
}

// comporCamadasNaFaixa imprime as camadas (na ordem recebida: de cima para
// baixo) e compõe cada uma na faixa, respeitando o que as janelas mais altas já
// cobriram. Devolve a área coberta, o custo do ciclo (para o ritmo adaptativo —
// papel de parede não conta, é trabalho estático) e se alguma janela compôs
// pixels.
//
// bufJanela é o DIB de impressão das janelas e fica SEPARADO da faixa de
// propósito: um PrintWindow não pode destruir a composição já feita.
func comporCamadasNaFaixa(camadas []camadaJanela, rect winui.Rect, faixa []byte, bufJanela *[]byte) (coberto []winui.Rect, custo int, algumaFonte bool) {
	// Quantas camadas de verdade (não-cartão, não-wallpaper) serão impressas:
	// limitado por mascaraRefreshMaxJanelas. O papel de parede NÃO consome o
	// orçamento: ele é a fonte de fundo que garante a cobertura — se 6 janelas
	// normais o expulsassem do ciclo, a faixa voltaria a ficar parcial e a
	// máscara seria descartada exatamente no caso que o fallback existe para
	// evitar (desktop limpo).
	uteis := 0
	for _, c := range camadas {
		if !c.cartao && !c.papelDeParede {
			uteis++
			if uteis >= mascaraRefreshMaxJanelas {
				break
			}
		}
	}
	if uteis == 0 && !temPapelDeParede(camadas) {
		// Só os cartões do agente estão sob a faixa (o usuário abriu o menu
		// Iniciar, trocou de desktop virtual E não há papel de parede): não há
		// fonte ao vivo. A máscara fica como está — é o comportamento
		// conservador de sempre.
		return nil, 0, false
	}

	// Área que já foi preenchida por uma janela mais alta. Uma janela mais baixa
	// só contribui com o que sobrar — é isso que reproduz o que o operador veria
	// se o cartão não existisse.
	coberto = make([]winui.Rect, 0, len(camadas))
	for _, c := range camadas {
		if c.cartao {
			// Cartão do agente: NUNCA é fonte (vazaria o cartão para o stream).
			//
			// E ele NÃO interrompe a varredura nem conta como área coberta: o
			// cartão é TOPMOST, então na ordem Z ele aparece ANTES das janelas
			// de verdade. Se ele "cobrisse" ou quebrasse o laço aqui, nenhuma
			// janela seria impressa e a máscara seria descartada em TODO ciclo —
			// o refresh morreria por completo. A fonte é justamente o que está
			// ATRÁS dele: pula e segue compondo.
			continue
		}
		if !c.papelDeParede {
			if uteis <= 0 {
				// Orçamento de janelas vivas esgotado: pula as janelas normais
				// restantes SEM quebrar o laço — o papel de parede vem no FIM da
				// lista e precisa ser alcançado (ele é o fundo, não concorre).
				continue
			}
			uteis--
		}
		janelaRect, pixels, ok, _ := imprimirJanela(c.hwnd, bufJanela)
		if !ok {
			continue
		}
		if !c.papelDeParede {
			// Papel de parede não conta no custo: é estático e imprimir a tela
			// inteira a cada ciclo empurraria o refresh para o regime lento sem
			// motivo (não há janela "viva" nenhuma). O skip de commit igual
			// (faixaIgualRGB) já evita o trabalho de commit; aqui evitamos pagar
			// o ciclo rápido por um conteúdo que não muda.
			custo += janelaRect.W * janelaRect.H
		}
		if comporJanelaNaFaixa(faixa, rect, janelaRect, pixels, coberto) {
			algumaFonte = true
		}
		coberto = append(coberto, janelaRect)
	}
	return coberto, custo, algumaFonte
}

// temPapelDeParede informa se a lista de camadas contém alguma fonte de fundo
// (Progman/WorkerW). Usado para o caso "só cartão sob a faixa" não falhar
// quando o wallpaper pode compor.
func temPapelDeParede(camadas []camadaJanela) bool {
	for _, c := range camadas {
		if c.papelDeParede {
			return true
		}
	}
	return false
}

// imprimirJanela é var (e não func) para os testes poderem injetar uma fonte
// de impressão determinística — é exatamente o que prova o contrato de buffers
// separados com o tampão da faixa (ver TestComporCamadasNaFaixaNaoAliasBufDaFaixa).
// O valor de produção é imprimirJanelaWin32; os testes restauram-no no cleanup.
var imprimirJanela = imprimirJanelaWin32

// imprimirJanelaWin32 renderiza a janela num DIB BGRA top-down e devolve o
// retângulo da janela + os pixels. ok=false quando o Windows recusa ou a janela
// é grande demais para o custo valer a pena; motivo diz QUAL foi a falha.
//
// O motivo existe porque uma versão anterior colapsava CINCO causas distintas
// num único bool e o chamador registrava sempre "PrintWindow recusado" — mesmo
// quando a falha real era janela travada, DC indisponível ou área absurda. Num
// diagnóstico de campo isso levou a investigar a API errada.
//
// IMPORTANTE: o DIB é criado com o tamanho da JANELA e o `buf` recebido é o
// tampão de impressão DEDICADO das janelas (bufJanela) — separado do tampão da
// faixa composta de propósito: imprimir uma janela NÃO pode destruir a
// composição já feita (com buffer único, janelas menores que a faixa
// sobrescreviam o início dela — conteúdo riscado/branco no stream). O bitmap
// devolvido é válido até a próxima chamada que use o mesmo `buf`.
func imprimirJanelaWin32(hwnd uintptr, buf *[]byte) (winui.Rect, []byte, bool, string) {
	r, ok := winui.WindowRect(hwnd)
	if hwnd == 0 || !ok {
		return winui.Rect{}, nil, false, "janela sem retângulo (destruída ou handle inválido)"
	}
	if r.W <= 0 || r.H <= 0 {
		return winui.Rect{}, nil, false, "janela com área inválida"
	}
	if r.W*r.H > mascaraRefreshMaxPixels {
		return winui.Rect{}, nil, false, "janela maior que o teto de pixels"
	}
	// App travado (não responde a mensagens): PrintWindow bloquearia a goroutine
	// de refresh para sempre. Melhor manter a máscara congelada.
	if travada, _, _ := procIsHungAppWindow.Call(hwnd); travada != 0 {
		return winui.Rect{}, nil, false, "app travado (não responde)"
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return winui.Rect{}, nil, false, "GetDC da tela falhou"
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return winui.Rect{}, nil, false, "CreateCompatibleDC falhou"
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
		return winui.Rect{}, nil, false, "CreateDIBSection falhou"
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
		return winui.Rect{}, nil, false, "PrintWindow recusado"
	}
	return r, out, true, ""
}

// regiaoUtil informa se a faixa capturada é CONTEÚDO VÁLIDO para substituir a
// máscara atual.
//
// Só descarta captura INVÁLIDA: faixa toda preta quando a máscara anterior NÃO
// era toda preta (PrintWindow recusado por app UWP/GPU devolve bitmap zerado sem
// falhar) ou buffer zerado. Região uniforme NÃO é inválida.
//
// A versão anterior exigia variação de cor (maxR-minR >= 2) e por isso rejeitava
// toda região uniforme — inclusive branco chapado de página em branco, cinza
// liso de barra de título e área escura lisa de papel de parede. O comentário
// original supunha que "região uniforme congelada já é o mesmo conteúdo", o que
// é FALSO quando a janela de trás mudou: ao fechar o navegador, a área sob o
// cartão vira papel de parede liso, o refresh é rejeitado e a máscara mantém
// para sempre os pixels da janela já fechada — o fantasma relatado em campo
// (faixa branca de 480x76 com resíduo da UI do navegador sobre o wallpaper).
//
// A amostragem é proporcional ao tamanho (até ~1024 amostras): com passo fixo de
// 97 pixels uma interseção pequena (ex.: 80 px de largura) era amostrada em 1
// ponto só e SEMPRE rejeitada, desligando o refresh justamente no caso comum de
// janela estreita sob o cartão.
//
// `anterior` é a máscara que está sendo substituída. Ele entra na decisão porque
// "tudo preto" deixou de ser rejeitado por si só: com o refresh compondo a faixa
// INTEIRA, uma faixa legitimamente preta (vídeo, tema escuro, app preto) tem de
// continuar aceita. O que não pode ser aceito é o preto APAGAR conteúdo que
// existia — é exatamente esse o sintoma do bitmap vazio devolvido sem falha.
// Sem `anterior` (máscara recém-capturada), tudo preto é rejeitado: o mesmo
// critério conservador da captura inicial.
func regiaoUtil(pixels, anterior []byte) bool {
	if len(pixels) < 4 {
		return false
	}
	if !regiaoTodaPreta(pixels) {
		return true
	}
	// Sem máscara anterior não há com o que comparar: mantém o critério
	// conservador da captura inicial (tudo preto = captura bloqueada).
	if len(anterior) == 0 {
		return false
	}
	// Preto só passa se a máscara anterior TAMBÉM era preta (nada foi apagado).
	return regiaoTodaPreta(anterior)
}

// registrarFalhaMascara contabiliza um refresh sem conteúdo válido. Enquanto o
// contador estiver abaixo do limite, mantém o quadro congelado (comportamento
// histórico). Ao atingir mascaraFalhasParaDescartar, DESCARTA a máscara: é melhor
// mostrar a área ao vivo (mesmo que o cartão do agente apareça nela) do que
// deixar um retângulo de conteúdo morto na tela indefinidamente.
//
// Sem isso o fantasma só desaparece quando alguma janela com conteúdo variado
// passa a ficar sob o cartão — ou seja, pode durar a sessão inteira.
//
// O descarte é imediato quando o contador zera por sucesso: uma máscara que
// voltou a funcionar não acumula falhas antigas (ver o teste de regressão do
// contador).
func registrarFalhaMascara(m *mascaraRegiao, motivo string) {
	m.falhasConsecutivas++
	if m.falhasConsecutivas < mascaraFalhasParaDescartar {
		logMascaraRefreshFalha(m.hwnd, motivo)
		return
	}
	// Descarta ANTES de logar: removerMascara pode encerrar o loop de refresh.
	hwnd := m.hwnd
	log.Printf("[session-banner] refresh da máscara falhou %d vezes seguidas (%s) — descartando a máscara e voltando à captura ao vivo", m.falhasConsecutivas, motivo)
	removerMascara(hwnd)
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

// camadaJanela é uma janela da ordem Z que pode compor a faixa do cartão.
type camadaJanela struct {
	hwnd   uintptr
	cartao bool // janela do agente (banner/aviso): NUNCA é fonte
	// papelDeParede marca Progman/WorkerW (fundo do desktop). São compostos
	// POR ÚLTIMO e não contam para o custo do ciclo (ver compose abaixo): são
	// o fallback que mantém a máscara válida quando nada mais cobre a faixa —
	// desktop limpo — em vez de deixá-la morrer por falhas consecutivas.
	papelDeParede bool
}

// camadasSobreRegiao devolve, de CIMA para baixo, as janelas VISÍVEIS que
// intersectam a região do cartão, incluindo as janelas do agente (marcadas com
// cartao=true, para serem puladas sem confundir a busca).
//
// Percorre a ordem Z com GetTopWindow/GetWindow(GW_HWNDNEXT), que devolve as
// janelas TOP-LEVEL em ordem Z (de cima para baixo).
//
// Janelas TOPMOST não entram na composição. Motivo: o que a composição precisa
// reproduzir é o que o operador veria naquela faixa SE O CARTÃO NÃO EXISTISSE, e
// o cartão é topmost — mas NÃO é o topmost mais alto (o menu Iniciar, o Action
// Center e o UAC ficam acima de qualquer topmost). Se uma dessas janelas está
// aberta sobre a faixa, ela apareceria no stream no lugar do cartão, e o que
// está ATRÁS do cartão não é o que o operador veria ali. Pulando a banda
// topmost inteira, essa área sobra na contabilidade de cobertura — e o chamador
// descarta a máscara em vez de deixar um pedaço congelado.
//
// EXCEÇÃO — o papel de parede: Progman/WorkerW são topmost, mas são o FUNDO do
// desktop (nada os cobre por baixo). Eles são coletados à parte e compostos
// POR ÚLTIMO, preenchendo só o que nenhuma janela cobriu. Sem esse fallback, um
// desktop limpo (só wallpaper atrás do cartão — o caso MAIS COMUM) não acha
// fonte nenhuma, o refresh falha 3× e a máscara é descartada: a área volta à
// captura ao vivo, o DDA ignora a afinidade e o CARTÃO APARECE no stream. Com o
// wallpaper na composição, a máscara fica com o conteúdo correto.
// camadasSobreRegiao é var (padrão de injeção do pacote): os testes do refresh
// injetam uma ordem Z determinística — sem isso o caminho dependente de janelas
// reais não é testável. Valor de produção: camadasSobreRegiaoWin32.
var camadasSobreRegiao = camadasSobreRegiaoWin32

// camadasSobreRegiaoWin32 é a implementação real (varredura da ordem Z).
func camadasSobreRegiaoWin32(regiao winui.Rect) []camadaJanela {
	alvo := bannerCaptureTarget.Load()
	extras := capturaExtras()
	// Teto de iteração: a ordem Z de um desktop real tem dezenas de janelas,
	// mas o que interessa está no topo. O limite evita varrer o desktop inteiro
	// a cada ciclo num ambiente com muitos handles.
	const maxIteracoes = 64
	var camadas []camadaJanela
	var papel []camadaJanela
	hwnd, _, _ := procGetTopWindow.Call(0)
	for i := 0; hwnd != 0 && i < maxIteracoes; i++ {
		if visivel, _, _ := procIsWindowVisible.Call(hwnd); visivel != 0 {
			if ehCartaoDoAgente(hwnd, alvo, extras) {
				// Cartão do agente: entra na lista marcado (nunca é fonte) para
				// que a composição pare ali — o que está ABAIXO do cartão não é
				// o que o operador veria na faixa.
				if janelaCobreRegiao(hwnd, regiao) {
					camadas = append(camadas, camadaJanela{hwnd: hwnd, cartao: true})
				}
			} else if eTopmost(hwnd) {
				// Banda topmost: o papel de parede é o único membro útil dela.
				if eJanelaPapelDeParede(hwnd) && janelaCobreRegiao(hwnd, regiao) {
					papel = append(papel, camadaJanela{hwnd: hwnd, papelDeParede: true})
				}
			} else if janelaCobreRegiao(hwnd, regiao) {
				camadas = append(camadas, camadaJanela{hwnd: hwnd})
			}
		}
		hwnd, _, _ = procGetWindow.Call(hwnd, gwHwndNext)
	}
	// Papel de parede entra NO FIM da ordem de composição: como camada mais
	// baixa, ele só preenche o que as janelas de verdade deixaram livre.
	return append(camadas, papel...)
}

// eJanelaPapelDeParede informa se hwnd é Progman/WorkerW (hospedeiros do papel
// de parede do desktop). São topmost na ordem Z, mas visualmente ficam ATRÁS de
// tudo — por isso são compostos por último como fonte de fundo.
func eJanelaPapelDeParede(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	if err := procGetClassName.Find(); err != nil {
		return false
	}
	var buf [16]uint16
	n, _, _ := procGetClassName.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	classe := windows.UTF16ToString(buf[:n])
	return classe == "Progman" || classe == "WorkerW"
}

// janelaCobreRegiao informa se alguma parte da janela (ou do seu top-level dono,
// no caso de popups/menus) cai dentro da região do cartão.
func janelaCobreRegiao(hwnd uintptr, regiao winui.Rect) bool {
	if hwnd == 0 {
		return false
	}
	if r, ok := winui.WindowRect(hwnd); ok && !r.Intersect(regiao).Empty() {
		return true
	}
	// Menus/popups são janelas WS_POPUP de um dono: o retângulo do dono é o que
	// o usuário vê como "a janela atrás". GetAncestor(GA_ROOT) sobe até ele.
	if raiz, _, _ := procGetAncestor.Call(hwnd, gaRoot); raiz != 0 && raiz != hwnd {
		if r, ok := winui.WindowRect(raiz); ok && !r.Intersect(regiao).Empty() {
			return true
		}
	}
	return false
}

// eTopmost informa se a janela está na banda TOPMOST (WS_EX_TOPMOST).
//
// GetWindowLongPtrW é resolvido em tempo de execução: em Windows de 32 bits a
// API se chama GetWindowLongW. Sem a resolução dinâmica o refresh morreria com
// "procedimento não encontrado" — e o pior lugar para morrer é justamente o
// mecanismo que impede o fantasma. Quando nenhuma das duas existe, devolve false
// (comportamento anterior a esta correção: a janela entra na composição).
func eTopmost(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	if err := procGetWindowLong.Find(); err != nil {
		return false
	}
	// GWL_EXSTYLE é -20 e uintptr é sem sinal: a constante negativa precisa passar
	// por uma variável (a conversão direta de constante não compila).
	indice := int32(gwlExStyle)
	estilo, _, _ := procGetWindowLong.Call(hwnd, uintptr(indice))
	return estilo&wsExTopmost != 0
}

// comporJanelaNaFaixa copia para a faixa (BGRA top-down, do tamanho de rect) a
// parte de janelaRect que ainda NÃO foi coberta por coberto (as janelas mais
// altas na ordem Z), e acrescenta a área efetivamente copiada a coberto.
//
// Devolve true quando copiou pelo menos um pixel. Sem isso, uma janela que está
// na ordem Z mas não aparece de fato na faixa (coberta por outra, ou totalmente
// fora) contaria como "fonte" e a validação de área parcial passaria com a faixa
// ainda congelada.
func comporJanelaNaFaixa(faixa []byte, rect, janelaRect winui.Rect, pixels []byte, coberto []winui.Rect) bool {
	inter := rect.Intersect(janelaRect)
	if inter.Empty() {
		return false
	}
	copiou := false
	// Divisão em faixas horizontais: dentro de uma linha, os trechos livres são
	// delimitados pelos limites laterais das janelas que já cobriram a área.
	for y := inter.Y; y < inter.Bottom(); y++ {
		x := inter.X
		for x < inter.Right() {
			// CASO 1 — x já está DENTRO de uma janela mais alta: o trecho
			// pertence a ela e NÃO pode ser sobrescrito. Avança até o limite
			// direito dela sem copiar nada.
			//
			// Esta checagem precisa vir ANTES do cálculo de `fim`. A versão
			// anterior só olhava `c.X > x` (janela que COMEÇA à frente de x) e
			// por isso, com x exatamente no início da janela de cima, caía no
			// ramo de cópia e escrevia até o fim da linha — a janela de baixo
			// sobrescrevia a de cima, exatamente o inverso do que o operador
			// vê. Era o bug do teste TestComposicaoRespeitaAOrdemZ.
			dentroDe := 0
			for _, c := range coberto {
				if y < c.Y || y >= c.Bottom() {
					continue
				}
				if x >= c.X && x < c.Right() && c.Right() > dentroDe {
					dentroDe = c.Right()
				}
			}
			if dentroDe > x {
				x = dentroDe
				continue
			}
			// CASO 2 — x está livre: copia até o início da próxima janela mais
			// alta que comece à frente (ou até o fim da interseção).
			fim := inter.Right()
			for _, c := range coberto {
				if y < c.Y || y >= c.Bottom() {
					continue
				}
				if c.X > x && c.X < fim {
					fim = c.X
				}
			}
			if fim > x {
				if copiarTrecho(faixa, rect, janelaRect, pixels, x, y, fim-x) {
					copiou = true
				}
				x = fim
				continue
			}
			// Defensivo: sem progresso (não deveria acontecer), encerra a linha
			// para não entrar em laço infinito.
			x++
		}
	}
	if copiou {
		coberto = append(coberto, inter)
	}
	return copiou
}

// copiarTrecho copia largura pixels da linha y (coordenadas do DESKTOP) do
// bitmap pixels (do tamanho de janelaRect) para faixa (do tamanho de rect).
// Devolve false quando o trecho sai de qualquer um dos dois buffers.
func copiarTrecho(faixa []byte, rect, janelaRect winui.Rect, pixels []byte, x, y, largura int) bool {
	if largura <= 0 {
		return false
	}
	destX := x - rect.X
	destY := y - rect.Y
	srcX := x - janelaRect.X
	srcY := y - janelaRect.Y
	if destX < 0 || destY < 0 || srcX < 0 || srcY < 0 {
		return false
	}
	bytes := largura * 4
	offDst := (destY*rect.W + destX) * 4
	offSrc := (srcY*janelaRect.W + srcX) * 4
	if offDst+bytes > len(faixa) || offSrc+bytes > len(pixels) {
		return false
	}
	copy(faixa[offDst:offDst+bytes], pixels[offSrc:offSrc+bytes])
	return true
}

// faixaIgualRGB compara dois buffers BGRA pelo conteúdo RGB (ignora o canal
// alfa). Devolve false para tamanhos diferentes.
func faixaIgualRGB(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i+3 < len(a); i += 4 {
		if a[i] != b[i] || a[i+1] != b[i+1] || a[i+2] != b[i+2] {
			return false
		}
	}
	return true
}

// regiaoCoberta informa se a união de coberto (retângulos em coordenadas do
// desktop) cobre TODA a região do cartão.
//
// A verificação é exata, por linhas: para cada linha do cartão somam-se os
// trechos cobertos e compara-se com a largura. Não há como "quase cobrir" — a
// faixa é estreita (a altura do cartão) e a soma por linha é barata (poucos
// retângulos).
func regiaoCoberta(coberto []winui.Rect, regiao winui.Rect) bool {
	if regiao.Empty() || len(coberto) == 0 {
		return false
	}
	for y := regiao.Y; y < regiao.Bottom(); y++ {
		if larguraCobertaNaLinha(coberto, regiao, y) < regiao.W {
			return false
		}
	}
	return true
}

// larguraCobertaNaLinha devolve quantos pixels da linha y da região estão
// cobertos, contando trechos SOBREPOSTOS uma única vez.
func larguraCobertaNaLinha(coberto []winui.Rect, regiao winui.Rect, y int) int {
	total := 0
	for x := regiao.X; x < regiao.Right(); {
		cobertoAqui := false
		limite := regiao.Right()
		for _, c := range coberto {
			if y < c.Y || y >= c.Bottom() {
				continue
			}
			if x >= c.X && x < c.Right() {
				cobertoAqui = true
				if c.Right() < limite {
					limite = c.Right()
				}
			}
		}
		if !cobertoAqui || limite <= x {
			x++
			continue
		}
		total += limite - x
		x = limite
	}
	return total
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
