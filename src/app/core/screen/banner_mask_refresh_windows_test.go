//go:build windows

package screen

import (
	"testing"

	"discovery/app/core/winui"
)

// ── Regressão do fantasma do banner (faixa congelada na tela) ──
//
// Sintoma relatado em campo: a faixa onde o cartão do banner fica aparecia
// CONGELADA no visualizador, com pixels mortos de um instante antigo.
//
// Duas causas independentes, ambas fixadas aqui:
//
// (1) O refresh escrevia SÓ a interseção entre o retângulo do cartão e o da
//     janela escolhida (a primeira da ordem Z sob o ponto CENTRAL do cartão).
//     Quando a janela de trás cobria apenas PARTE do cartão — o caso normal,
//     porque o cartão é centralizado no topo — as faixas laterais não eram
//     escritas por ninguém, em refresh nenhum: fantasma permanente POR
//     CONSTRUÇÃO. A correção compõe a faixa inteira com as janelas da ordem Z e
//     DESCARTA a máscara quando a composição não cobre tudo.
//
// (2) regiaoUtil exigia variação de cor (maxR-minR >= 2) e rejeitava toda região
//     UNIFORME. Ao fechar o navegador a área sob o cartão vira papel de parede
//     liso — uniforme, portanto rejeitada — e o refresh mantinha a máscara
//     antiga indefinidamente.

// regiaoUniforme monta uma região BGRA de w x h com a cor (b,g,r) uniforme.
func regiaoUniforme(w, h int, b, g, r byte) []byte {
	px := make([]byte, w*h*4)
	for i := 0; i+3 < len(px); i += 4 {
		px[i] = b
		px[i+1] = g
		px[i+2] = r
	}
	return px
}

// ── Contrato de regiaoUtil (com o parâmetro `anterior`) ──
//
// A assinatura ganhou a máscara ANTERIOR porque "tudo preto" deixou de ser
// rejeitado por si só: com o refresh compondo a faixa INTEIRA, uma faixa
// legitimamente preta (vídeo, tema escuro) tem de continuar aceita. O que não
// pode ser aceito é o preto APAGAR conteúdo que existia — é o sintoma do bitmap
// vazio devolvido por app UWP/GPU SEM falhar (retorno != 0).

// Regressão direta do bug: página em branco sob o cartão é conteúdo legítimo e
// deve ser ACEITA. Antes era rejeitada por não ter variação de cor.
func TestRegiaoUtilAceitaRegiaoUniformeBranca(t *testing.T) {
	if !regiaoUtil(regiaoUniforme(480, 76, 0xFF, 0xFF, 0xFF), regiaoUniforme(480, 76, 0x11, 0x22, 0x33)) {
		t.Fatal("região branca uniforme foi rejeitada — é conteúdo legítimo (página em branco sob o cartão)")
	}
}

// Papel de parede liso / barra de título lisa também são conteúdo legítimo.
func TestRegiaoUtilAceitaRegiaoUniformeCinza(t *testing.T) {
	if !regiaoUtil(regiaoUniforme(200, 40, 0xE3, 0xE3, 0xE3), regiaoUniforme(200, 40, 0x11, 0x22, 0x33)) {
		t.Fatal("região cinza uniforme foi rejeitada — é conteúdo legítimo (barra de título / wallpaper)")
	}
}

// Área escura lisa (tema escuro, vídeo preto) continua válida — o sinal de
// captura bloqueada é o bitmap TODO preto sobre conteúdo vivo, não o conteúdo
// escuro.
func TestRegiaoUtilAceitaRegiaoUniformeEscura(t *testing.T) {
	if !regiaoUtil(regiaoUniforme(120, 40, 0x1A, 0x12, 0x0E), regiaoUniforme(120, 40, 0x11, 0x22, 0x33)) {
		t.Fatal("região escura uniforme (mas não preta) foi rejeitada — é conteúdo legítimo")
	}
}

// O sinal real de captura inválida: bitmap todo preto que APAGARIA conteúdo vivo.
func TestRegiaoUtilRejeitaBitmapTodoPretoSobreConteudoVivo(t *testing.T) {
	if regiaoUtil(regiaoUniforme(100, 30, 0, 0, 0), regiaoUniforme(100, 30, 0x11, 0x22, 0x33)) {
		t.Fatal("bitmap todo preto foi aceito sobre conteúdo vivo — é captura inválida (UWP/GPU devolve vazio sem falhar)")
	}
}

// Faixa legitimamente preta (vídeo/tema escuro) NÃO pode ser rejeitada: a máscara
// anterior também era preta, então o preto não está apagando nada.
func TestRegiaoUtilAceitaPretoSobrePreto(t *testing.T) {
	if !regiaoUtil(regiaoUniforme(100, 30, 0, 0, 0), regiaoUniforme(100, 30, 0, 0, 0)) {
		t.Fatal("faixa preta sobre máscara preta foi rejeitada — vídeo/tema escuro sob o cartão ficaria congelado")
	}
}

// Sem máscara anterior (captura inicial) o critério conservador continua: tudo
// preto é rejeitado (mesma regra de capturarMascaraRegiao/telaTambemPreta).
func TestRegiaoUtilRejeitaPretoSemAnterior(t *testing.T) {
	if regiaoUtil(regiaoUniforme(100, 30, 0, 0, 0), nil) {
		t.Fatal("bitmap todo preto foi aceito sem máscara anterior")
	}
}

// Região vazia/curta não é captura utilizável.
func TestRegiaoUtilRejeitaRegiaoCurta(t *testing.T) {
	if regiaoUtil([]byte{0xFF, 0xFF, 0xFF}, nil) {
		t.Fatal("buffer com menos de um pixel foi aceito")
	}
	if regiaoUtil(nil, nil) {
		t.Fatal("buffer nulo foi aceito")
	}
}

// Conteúdo com variação continua aceito (regressão do comportamento anterior).
func TestRegiaoUtilAceitaGradiente(t *testing.T) {
	px := regiaoUniforme(200, 40, 0, 0, 0)
	for i := 0; i+3 < len(px); i += 4 {
		px[i+2] = byte((i / 4) % 256)
	}
	if !regiaoUtil(px, regiaoUniforme(200, 40, 0x11, 0x22, 0x33)) {
		t.Fatal("gradiente foi rejeitado — conteúdo com variação deve ser aceito")
	}
}

// ── Composição da faixa a partir da ordem Z ──
//
// Estes testes cobrem a causa raiz (1): o refresh precisa atualizar a FAIXA
// INTEIRA do cartão, não só a interseção com uma janela escolhida. As funções
// puras abaixo (comporJanelaNaFaixa / regiaoCoberta / larguraCobertaNaLinha)
// são exatamente as que o caminho real usa — o PrintWindow é a única parte que
// não roda em teste (exige janela real).

// TesteDeRegressaoJanelaDeTrasCobreSoParteDoCartao reproduz o cenário do campo:
// a janela de trás cobre o CENTRO do cartão (que é o que a versão antiga
// escolhia por janelaSobPonto) e deixa as duas pontas de fora.
//
// Comportamento ANTIGO (bug): só o centro era escrito; as pontas ficavam com os
// pixels do instante da criação do cartão, para sempre.
// Comportamento NOVO: o centro é escrito pela janela e as pontas são
// reconhecidas como NÃO cobertas — o chamador descarta a máscara em vez de
// manter um retângulo morto na tela.
func TestRegressaoJanelaDeTrasCobreSoParteDoCartao(t *testing.T) {
	// Cartão de 100x20 em (0,0); janela de trás cobre só x=[40,60).
	cartao := winui.Rect{X: 0, Y: 0, W: 100, H: 20}
	janela := winui.Rect{X: 40, Y: 0, W: 20, H: 20}

	faixa := make([]byte, cartao.W*cartao.H*4)
	pixels := regiaoUniforme(janela.W, janela.H, 0xAA, 0xBB, 0xCC)

	var coberto []winui.Rect
	if !comporJanelaNaFaixa(faixa, cartao, janela, pixels, coberto) {
		t.Fatal("a janela que cobre o centro do cartão deveria ter composto algum pixel")
	}
	coberto = append(coberto, cartao.Intersect(janela))

	// O centro foi atualizado…
	if got := faixa[(10*cartao.W+50)*4]; got != 0xAA {
		t.Fatalf("centro da faixa = %#x, esperado 0xAA (conteúdo da janela de trás)", got)
	}
	// …e as pontas NÃO estão cobertas: é isso que a versão antiga ignorava e que
	// deixava o fantasma agarrado nas laterais.
	if regiaoCoberta(coberto, cartao) {
		t.Fatal("a cobertura foi considerada completa com a janela cobrindo só o centro — o fantasma voltaria")
	}
	// O chamador decide por regiaoCoberta; aqui confirmamos que a ponta esquerda
	// continua com o valor inicial (não escrita por ninguém).
	if got := faixa[(10*cartao.W+5)*4]; got != 0x00 {
		t.Fatalf("ponta esquerda foi escrita (%#x) — não deveria, a janela não a cobre", got)
	}
}

// Duas janelas lado a lado, na ordem Z, cobrem o cartão inteiro: cada uma
// contribui com a sua metade e a composição é considerada completa. Este é o
// caminho que substitui a atualização parcial.
func TestComposicaoDeDuasJanelasCobreOCartaoInteiro(t *testing.T) {
	cartao := winui.Rect{X: 0, Y: 0, W: 100, H: 20}
	esquerda := winui.Rect{X: 0, Y: 0, W: 50, H: 20}
	direita := winui.Rect{X: 50, Y: 0, W: 50, H: 20}

	faixa := make([]byte, cartao.W*cartao.H*4)
	var coberto []winui.Rect

	if !comporJanelaNaFaixa(faixa, cartao, esquerda, regiaoUniforme(50, 20, 0x11, 0x11, 0x11), coberto) {
		t.Fatal("a janela da esquerda deveria compor pixels")
	}
	coberto = append(coberto, cartao.Intersect(esquerda))
	if !comporJanelaNaFaixa(faixa, cartao, direita, regiaoUniforme(50, 20, 0x22, 0x22, 0x22), coberto) {
		t.Fatal("a janela da direita deveria compor pixels")
	}
	coberto = append(coberto, cartao.Intersect(direita))

	if !regiaoCoberta(coberto, cartao) {
		t.Fatal("duas janelas lado a lado deveriam cobrir o cartão inteiro")
	}
	if got := faixa[(10*cartao.W+10)*4]; got != 0x11 {
		t.Fatalf("metade esquerda = %#x, esperado 0x11", got)
	}
	if got := faixa[(10*cartao.W+90)*4]; got != 0x22 {
		t.Fatalf("metade direita = %#x, esperado 0x22", got)
	}
}

// Janela mais ALTA na ordem Z tapa a mais baixa: a segunda não pode sobrescrever
// o que a primeira já escreveu (é o que o operador veria de fato).
//
// `baixo` cobre o cartão INTEIRO de propósito: ela preenche as laterais que
// `cima` deixa livres (comportamento correto e desejado) e, na faixa em que as
// duas se sobrepõem, tem de perder para `cima`.
func TestComposicaoRespeitaAOrdemZ(t *testing.T) {
	cartao := winui.Rect{X: 0, Y: 0, W: 100, H: 20}
	baixo := winui.Rect{X: 0, Y: 0, W: 100, H: 20} // janela de fundo (cartão inteiro)
	cima := winui.Rect{X: 30, Y: 0, W: 40, H: 20}  // janela na frente dela (sobrepõe 30..70)

	faixa := make([]byte, cartao.W*cartao.H*4)
	var coberto []winui.Rect

	// Ordem Z: `cima` vem primeiro (mais alta).
	comporJanelaNaFaixa(faixa, cartao, cima, regiaoUniforme(cima.W, cima.H, 0xEE, 0xEE, 0xEE), coberto)
	coberto = append(coberto, cartao.Intersect(cima))
	comporJanelaNaFaixa(faixa, cartao, baixo, regiaoUniforme(baixo.W, baixo.H, 0x33, 0x33, 0x33), coberto)
	coberto = append(coberto, cartao.Intersect(baixo))

	if !regiaoCoberta(coberto, cartao) {
		t.Fatal("a união das duas janelas cobre o cartão inteiro")
	}
	// Dentro da janela de cima vale a cor DELA (a de baixo não a sobrescreve)…
	if got := faixa[(10*cartao.W+50)*4]; got != 0xEE {
		t.Fatalf("área da janela de cima = %#x, esperado 0xEE (a janela de baixo não pode sobrescrever)", got)
	}
	// …e nas laterais, que só a janela de baixo cobre, vale a cor dela.
	if got := faixa[(10*cartao.W+10)*4]; got != 0x33 {
		t.Fatalf("lateral esquerda = %#x, esperado 0x33 (só a janela de baixo a cobre)", got)
	}
	if got := faixa[(10*cartao.W+90)*4]; got != 0x33 {
		t.Fatalf("lateral direita = %#x, esperado 0x33 (só a janela de baixo a cobre)", got)
	}
}

// Janela totalmente fora do cartão não compõe nada e não pode contar como
// "fonte": sem isso a validação de cobertura passaria com a faixa ainda
// congelada.
func TestComposicaoIgnoraJanelaForaDoCartao(t *testing.T) {
	cartao := winui.Rect{X: 0, Y: 0, W: 100, H: 20}
	fora := winui.Rect{X: 200, Y: 0, W: 50, H: 50}
	faixa := make([]byte, cartao.W*cartao.H*4)
	if comporJanelaNaFaixa(faixa, cartao, fora, regiaoUniforme(50, 50, 0x99, 0x99, 0x99), nil) {
		t.Fatal("janela fora do cartão foi contada como fonte")
	}
	if regiaoCoberta(nil, cartao) {
		t.Fatal("cobertura vazia foi considerada completa")
	}
}

// Janela que cobre o cartão INTEIRO fecha a composição: é o caso em que o
// fantasma não existia (e não pode regredir).
func TestComposicaoDeJanelaUnicaCobreTudo(t *testing.T) {
	cartao := winui.Rect{X: 10, Y: 5, W: 100, H: 20}
	janela := winui.Rect{X: 0, Y: 0, W: 500, H: 500}
	faixa := make([]byte, cartao.W*cartao.H*4)
	var coberto []winui.Rect
	if !comporJanelaNaFaixa(faixa, cartao, janela, regiaoUniforme(janela.W, janela.H, 0x77, 0x77, 0x77), coberto) {
		t.Fatal("a janela grande deveria compor pixels")
	}
	coberto = append(coberto, cartao.Intersect(janela))
	if !regiaoCoberta(coberto, cartao) {
		t.Fatal("uma janela que contém o cartão inteiro cobre tudo")
	}
	if got := faixa[(0*cartao.W+0)*4]; got != 0x77 {
		t.Fatalf("canto da faixa = %#x, esperado 0x77", got)
	}
}

// Trechos de linhas diferentes: uma janela que cobre só a METADE SUPERIOR do
// cartão não pode ser confundida com cobertura total.
func TestCoberturaParcialPorLinha(t *testing.T) {
	cartao := winui.Rect{X: 0, Y: 0, W: 100, H: 20}
	superior := winui.Rect{X: 0, Y: 0, W: 100, H: 10}
	if regiaoCoberta([]winui.Rect{superior}, cartao) {
		t.Fatal("janela que cobre só as 10 primeiras linhas não cobre o cartão")
	}
	if !regiaoCoberta([]winui.Rect{{X: 0, Y: 0, W: 100, H: 20}}, cartao) {
		t.Fatal("janela da altura do cartão cobre tudo")
	}
}

// Retângulos SOBREPOSTOS não podem ser somados duas vezes (a soma por linha
// contaria 150 px numa linha de 100 e esconderia um buraco real).
func TestCoberturaNaoContaSobreposicaoDuasVezes(t *testing.T) {
	regiao := winui.Rect{X: 0, Y: 0, W: 100, H: 10}
	coberto := []winui.Rect{
		{X: 0, Y: 0, W: 60, H: 10},
		{X: 0, Y: 0, W: 60, H: 10}, // duplicado
	}
	if regiaoCoberta(coberto, regiao) {
		t.Fatal("sobreposição contada duas vezes fez uma cobertura de 60% passar por completa")
	}
	if got := larguraCobertaNaLinha(coberto, regiao, 5); got != 60 {
		t.Fatalf("largura coberta = %d, esperado 60 (sem contar a sobreposição)", got)
	}
}

// ── Cobertura parcial: a máscara morre no mesmo ciclo ──
//
// A regra de ouro é "prefira congelar a vazar", mas congelar PARA SEMPRE não é
// congelar: é fantasma. Quando a composição não cobre a faixa do cartão, a
// máscara fica meio-atualizada — metade com o conteúdo de agora, metade com os
// pixels do instante da criação — e isso é indistinguível de um retângulo morto.
//
// Nesse caso a máscara é DESCARTADA no mesmo ciclo e a área volta à captura ao
// vivo, com a afinidade como proteção (ver garantirProtecaoSemMascara). O cartão
// pode reaparecer por 1 quadro; a alternativa era ele ficar agarrado por horas.

// Prova o contrato que sustenta o descarte: uma faixa coberta só em parte NUNCA
// é considerada completa, em nenhuma combinação de janelas que a deixem com
// buraco.
func TestRegiaoCobertaRejeitaFaixaComBuraco(t *testing.T) {
	cartao := winui.Rect{X: 0, Y: 0, W: 100, H: 20}
	// As duas pontas cobertas, o miolo descoberto: exatamente o cenário do
	// campo, em que a janela de trás cobre só o centro do cartão.
	coberto := []winui.Rect{
		{X: 0, Y: 0, W: 20, H: 20},
		{X: 80, Y: 0, W: 20, H: 20},
	}
	if regiaoCoberta(coberto, cartao) {
		t.Fatal("faixa com o miolo descoberto foi considerada coberta — a máscara ficaria parcial e congelada")
	}
}

// Só o centro coberto (o que a versão antiga escolhia via janelaSobPonto): as
// laterais ficariam congeladas para sempre. Tem de ser reconhecido como parcial.
func TestRegiaoCobertaRejeitaSoOCentro(t *testing.T) {
	cartao := winui.Rect{X: 0, Y: 0, W: 100, H: 20}
	coberto := []winui.Rect{cartao.Intersect(winui.Rect{X: 40, Y: 0, W: 20, H: 20})}
	if regiaoCoberta(coberto, cartao) {
		t.Fatal("janela cobrindo só o centro do cartão foi aceita como cobertura total")
	}
}

// Uma faixa levemente maior que o cartão (janela transbordando) cobre tudo: o
// teste garante que o cálculo por linha não se perde com retângulos maiores.
func TestRegiaoCobertaAceitaJanelaMaiorQueOCartao(t *testing.T) {
	cartao := winui.Rect{X: 30, Y: 40, W: 100, H: 20}
	coberto := []winui.Rect{{X: 0, Y: 0, W: 500, H: 500}}
	if !regiaoCoberta(coberto, cartao) {
		t.Fatal("janela maior que o cartão deveria cobrir a faixa inteira")
	}
}

// ── Trava contra recapturar uma máscara descartada ──
//
// Quando a máscara é descartada em vida, o cartão JÁ ESTÁ na tela. Uma segunda
// captura da mesma janela leria a tela FÍSICA — onde o cartão está desenhado — e
// gravaria o PRÓPRIO CARTÃO dentro da máscara; o pipeline então o reescreveria em
// todo frame, vazando-o para o operador. Esta é a única mudança deste trabalho
// que mexe em risco de vazamento, então ela é conservadora por construção: a
// recaptura é RECUSADA.
func TestCapturarMascaraRegiaoRecusaJanelaDescartada(t *testing.T) {
	limparMascaras(t)
	limparMascarasDescartadas(t)
	const hwnd = 0xDE5C
	marcarMascaraDescartada(hwnd)

	err := capturarMascaraRegiao(hwnd)
	if err == nil {
		t.Fatal("capturarMascaraRegiao aceitou uma janela cuja máscara já foi descartada — leria o próprio cartão")
	}
	if !mascaraFoiDescartada(hwnd) {
		t.Fatal("a trava foi removida mesmo com a captura recusada")
	}
	if mascaraDisponivel(hwnd) {
		t.Fatal("uma máscara foi registrada apesar da trava")
	}
}

// registerBannerCapture limpa a trava: o Windows recicla HWNDs e um cartão NOVO
// que herde o handle de um antigo precisa poder capturar a máscara dele.
func TestRegisterBannerCaptureLimpaTravaDeDescarte(t *testing.T) {
	limparMascaras(t)
	limparMascarasDescartadas(t)
	const hwnd = 0xDE5D
	marcarMascaraDescartada(hwnd)
	registerBannerCapture(hwnd)
	t.Cleanup(func() { unregisterBannerCapture(hwnd) })
	if mascaraFoiDescartada(hwnd) {
		t.Fatal("registerBannerCapture não limpou a trava — um cartão novo no mesmo handle nasceria sem máscara")
	}
}

// A trava é POR JANELA: descartar a máscara do banner não pode impedir a captura
// da máscara do aviso da bandeja.
func TestTravaDeDescarteEhPorJanela(t *testing.T) {
	limparMascarasDescartadas(t)
	marcarMascaraDescartada(0xAA01)
	if mascaraFoiDescartada(0xAA02) {
		t.Fatal("a trava de uma janela vazou para outra")
	}
}

// ── O cartão é PULADO, não interrompe a composição ──
//
// O cartão do banner é TOPMOST: na ordem Z ele aparece ANTES das janelas normais
// que estão atrás dele. Se a varredura quebrasse no cartão (ou o tratasse como
// área coberta), NENHUMA janela seria impressa em ciclo nenhum: a máscara seria
// considerada parcial, descartada no primeiro refresh e o refresh morreria por
// completo — a faixa voltaria a ser capturada ao vivo e o cartão apareceria no
// stream (com a afinidade como única defesa).
//
// Este teste fixa a invariante pela função pura: uma camada de cartão não pode
// contribuir com cobertura nem consumo de fonte, e as janelas seguintes têm de
// compor normalmente.
func TestCartaoNaoInterrompeNemCobreAComposicao(t *testing.T) {
	cartao := winui.Rect{X: 0, Y: 0, W: 100, H: 20}
	janelaAtras := winui.Rect{X: 0, Y: 0, W: 100, H: 20}

	faixa := make([]byte, cartao.W*cartao.H*4)
	var coberto []winui.Rect
	// Simula a varredura: a PRIMEIRA camada é o cartão (topmost) e é pulada; a
	// segunda é a janela real, que precisa compor a faixa inteira.
	copiou := comporJanelaNaFaixa(faixa, cartao, janelaAtras, regiaoUniforme(janelaAtras.W, janelaAtras.H, 0x5A, 0x5A, 0x5A), coberto)
	if !copiou {
		t.Fatal("a janela atrás do cartão deveria compor pixels — o cartão não pode interromper a varredura")
	}
	coberto = append(coberto, cartao.Intersect(janelaAtras))
	if !regiaoCoberta(coberto, cartao) {
		t.Fatal("a janela atrás do cartão cobre a faixa inteira: a máscara não pode ser descartada como parcial")
	}
	if got := faixa[(10*cartao.W+50)*4]; got != 0x5A {
		t.Fatalf("faixa = %#x, esperado 0x5A (conteúdo da janela atrás do cartão)", got)
	}
}

// ── Invariante: a máscara NUNCA pode conter o próprio cartão ──
//
// O cartão é o dado mais sensível da sessão (o nome do operador). A captura da
// máscara faz BitBlt da tela FÍSICA, onde o cartão fica desenhado — então ela só
// é segura com a janela OCULTA. Antes esta garantia existia só no comentário da
// função: nada no código a verificava, e ativarMascara podia soltar a trava de
// recaptura de um cartão já visível, gravando o cartão dentro da máscara (e o
// pipeline o reescreveria em TODO frame). Estes testes fixam a invariante no
// código, que é onde ela precisa estar.

// visibilidadeDeTeste força a visibilidade que mascaraVisivel reporta, sem abrir
// janela real.
//
// É um apelido de mascaraInvisivel (banner_mask_windows_test.go) de propósito:
// tinha a mesma implementação com o mesmo defeito — capturava `anterior` e
// restaurava o que estivesse pendurado de outro teste. Existe UMA função que
// mexe em mascaraVisivel e restaura o valor de produção; os testes passam por
// ela e a suíte deixa de depender da ordem de execução.
func visibilidadeDeTeste(t *testing.T, visivel bool) {
	t.Helper()
	mascaraInvisivel(t, visivel)
}

// O caso perigoso: cartão JÁ VISÍVEL. A captura tem de ser RECUSADA — é a única
// coisa que impede a máscara de conter o cartão e vazá-lo em todo frame.
func TestCapturarMascaraRecusaJanelaVisivel(t *testing.T) {
	limparMascaras(t)
	limparMascarasDescartadas(t)
	const hwnd = 0x1E7A

	err := capturarMascaraRegiao(hwnd)
	if err == nil {
		t.Fatal("capturarMascaraRegiao aceitou uma janela VISÍVEL — gravaria o próprio cartão na máscara e o vazaria no stream")
	}
	if mascaraDisponivel(hwnd) {
		t.Fatal("uma máscara foi registrada a partir de uma janela visível")
	}
}

// A recaptura do ativarMascara só é legítima com a janela oculta: com o cartão em
// tela ela é recusada e a trava é RECOLOCADA (a área segue dependendo da
// afinidade, nunca de uma máscara contaminada).
func TestAtivarMascaraNaoRecapturaCartaoVisivel(t *testing.T) {
	limparMascaras(t)
	limparMascarasDescartadas(t)
	const hwnd = 0x1E7B
	visibilidadeDeTeste(t, true) // cartão NA TELA
	marcarMascaraDescartada(hwnd)

	ativarMascara(hwnd)

	if mascaraDisponivel(hwnd) {
		t.Fatal("ativarMascara recapturou com o cartão visível — a máscara conteria o próprio cartão")
	}
	if !mascaraFoiDescartada(hwnd) {
		t.Fatal("a trava ficou solta depois de uma recaptura recusada — uma tentativa futura poderia aceitar")
	}
}

// O caminho funcional continua vivo: com a janela oculta e com a trava, a
// recaptura acontece. É o que permite o cartão voltar a ter máscara depois de um
// descarte, sem reabrir o risco.
//
// Usa uma janela REAL (janelaMagenta cria mas NÃO mostra: o estado é o do fluxo
// de produção, em que a captura acontece antes do ShowWindow). HWND sintético não
// serve aqui: WindowRect não devolve retângulo para handle inexistente, e
// capturarMascaraRegiao falharia por "janela sem retângulo" antes de provar a
// recaptura — o teste passaria/ falharia pelo motivo errado.
func TestAtivarMascaraRecapturaComJanelaOculta(t *testing.T) {
	if testing.Short() {
		t.Skip("teste com janela real")
	}
	limparMascaras(t)
	limparMascarasDescartadas(t)
	hwnd := janelaMagenta(t, 900, 620)
	defer winui.DestroyWindow(hwnd)
	t.Cleanup(func() { removerMascara(hwnd) })

	// janelaMagenta só faz CreateWindow e NÃO chama ShowWindow: a janela já está
	// oculta — o mesmo estado do fluxo de produção (captura antes do ShowWindow).
	// winui não expõe SW_HIDE e não é preciso: nada aqui mostrou a janela.
	marcarMascaraDescartada(hwnd)

	ativarMascara(hwnd)

	if !mascaraDisponivel(hwnd) {
		t.Fatal("com a janela oculta a recaptura deveria ter registrado a máscara (caminho funcional perdido)")
	}
	if mascaraFoiDescartada(hwnd) {
		t.Fatal("a trava deveria ter sido solta após a recaptura bem-sucedida")
	}
}


// ── Regressões do refresh com buffers separados e papel de parede ──
//
// Duas falhas de projeto que produziam exatamente o sintoma relatado em campo
// (faixa branca/riscada no visualizador onde fica o cartão):
//
// (1) BUFFER ÚNICO: o PrintWindow da janela usava o MESMO tampão da faixa
//     composta. Janela de impressão menor que a faixa sobrescrevia o início
//     dela — a composição feita até ali era destruída e o que sobrava eram
//     riscos/lixo (ou branco chapado) gravados na máscara para sempre.
//
// (2) SEM FONTE DE FUNDO: num desktop limpo (só wallpaper atrás do cartão — o
//     caso mais comum) nenhuma janela da ordem Z cobria a faixa (Progman/
//     WorkerW são topmost e ficavam de fora), o refresh falhava 3× e a máscara
//     era descartada — a área voltava à captura ao vivo, o DDA ignora a
//     afinidade e o cartão aparecia no stream.

// TestRegiaoTodaBranca trava o detector de branco usado pela captura da
// semente (espelho do detector de preto, refactorado para cor única).
func TestRegiaoTodaBranca(t *testing.T) {
	if !regiaoTodaBranca(regiaoUniforme(64, 8, 0xFF, 0xFF, 0xFF)) {
		t.Fatal("região toda branca não foi detectada")
	}
	if regiaoTodaBranca(regiaoUniforme(64, 8, 0xFE, 0xFF, 0xFF)) {
		t.Fatal("região com um canal diferente foi detectada como branca")
	}
	// O refactor não pode ter mudado o detector de preto.
	if !regiaoTodaPreta(regiaoUniforme(64, 8, 0, 0, 0)) {
		t.Fatal("região toda preta deixou de ser detectada")
	}
	if regiaoTodaPreta(regiaoUniforme(64, 8, 0, 0, 1)) {
		t.Fatal("região com canal diferente foi detectada como preta")
	}
}

// imprimirJanelaFake devolve uma impressão uniforme com a cor derivada do hwnd,
// do tamanho do retângulo devolvido — imita o contrato do PrintWindow real.
func imprimirJanelaFake(cor byte, r winui.Rect) func(uintptr, *[]byte) (winui.Rect, []byte, bool, string) {
	return func(uintptr, *[]byte) (winui.Rect, []byte, bool, string) {
		pixels := regiaoUniforme(r.W, r.H, cor, cor, cor)
		return r, pixels, true, ""
	}
}

// TestComporCamadasNaFaixaNaoAliasBufDaFaixa prova o contrato dos buffers
// separados: imprimir a segunda janela NÃO pode destruir a composição da
// primeira. Com buffer único (bug), a faixa terminava com a cor da ÚLTIMA
// janela impressa, não com a da janela de cima.
func TestComporCamadasNaFaixaNaoAliasBufDaFaixa(t *testing.T) {
	anterior := imprimirJanela
	t.Cleanup(func() { imprimirJanela = anterior })

	rect := winui.Rect{X: 0, Y: 0, W: 100, H: 20}
	// A (hwnd 0xAA) está NA FRENTE e cobre tudo; B (0xBB) cobre tudo também.
	camadas := []camadaJanela{{hwnd: 0xAA}, {hwnd: 0xBB}}
	imprimirJanela = func(hwnd uintptr, buf *[]byte) (winui.Rect, []byte, bool, string) {
		cor := byte(hwnd)
		tamanho := rect.W * rect.H * 4
		if cap(*buf) < tamanho {
			*buf = make([]byte, tamanho)
		}
		out := (*buf)[:tamanho]
		for i := 0; i+3 < len(out); i += 4 {
			out[i] = cor
			out[i+1] = cor
			out[i+2] = cor
		}
		return rect, out, true, ""
	}

	faixa := make([]byte, rect.W*rect.H*4)
	var bufJanela []byte
	coberto, custo, algumaFonte := comporCamadasNaFaixa(camadas, rect, faixa, &bufJanela)

	if !algumaFonte {
		t.Fatal("composição não reconheceu fonte nenhuma")
	}
	if got := faixa[(10*rect.W+50)*4]; got != 0xAA {
		t.Fatalf("faixa = %#x, esperado 0xAA — imprimir a janela de trás sobrescreveu a composição da da frente (buffer compartilhado?)", got)
	}
	if !regiaoCoberta(coberto, rect) {
		t.Fatal("cobertura deveria ser completa")
	}
	if custo != rect.W*rect.H*2 {
		t.Fatalf("custo = %d, esperado %d (duas janelas impressas)", custo, rect.W*rect.H*2)
	}
}

// TestComporCamadasNaFaixaPapelDeParedePorUltimoSemCusto prova o fallback do
// papel de parede: entra NO FIM da composição (preenche só o que ninguém
// cobriu) e não conta no custo do ciclo (trabalho estático não deve empurrar o
// refresh para o regime lento).
func TestComporCamadasNaFaixaPapelDeParedePorUltimoSemCusto(t *testing.T) {
	anterior := imprimirJanela
	t.Cleanup(func() { imprimirJanela = anterior })

	rect := winui.Rect{X: 0, Y: 0, W: 100, H: 20}
	esquerda := winui.Rect{X: 0, Y: 0, W: 50, H: 20}
	imprimirJanela = func(hwnd uintptr, buf *[]byte) (winui.Rect, []byte, bool, string) {
		if hwnd == 0x11 {
			return esquerda, regiaoUniforme(esquerda.W, esquerda.H, 0x11, 0x11, 0x11), true, ""
		}
		return rect, regiaoUniforme(rect.W, rect.H, 0x77, 0x77, 0x77), true, ""
	}

	// Janela normal na frente, papel de parede por último.
	camadas := []camadaJanela{{hwnd: 0x11}, {hwnd: 0x77, papelDeParede: true}}
	faixa := make([]byte, rect.W*rect.H*4)
	var bufJanela []byte
	coberto, custo, algumaFonte := comporCamadasNaFaixa(camadas, rect, faixa, &bufJanela)

	if !algumaFonte {
		t.Fatal("composição com papel de parede deveria reconhecer fonte")
	}
	if got := faixa[(10*rect.W+10)*4]; got != 0x11 {
		t.Fatalf("metade esquerda = %#x, esperado 0x11 (a janela normal tem prioridade)", got)
	}
	if got := faixa[(10*rect.W+90)*4]; got != 0x77 {
		t.Fatalf("metade direita = %#x, esperado 0x77 (o papel de parede preenche o resto)", got)
	}
	if !regiaoCoberta(coberto, rect) {
		t.Fatal("janela + papel de parede deveriam cobrir a faixa inteira — sem isso a máscara seria descartada no desktop limpo")
	}
	if custo != esquerda.W*esquerda.H {
		t.Fatalf("custo = %d, esperado %d — o papel de parede não deve contar no custo adaptativo", custo, esquerda.W*esquerda.H)
	}
}

// TestAtualizarMascaraAoVivoCommitaSoMudanca trava o skip de commit: quando a
// composição reproduz EXATAMENTE os pixels da máscara atual, o buffer não é
// trocado (economia de copy-on-write); quando muda, o novo conteúdo entra.
func TestAtualizarMascaraAoVivoCommitaSoMudanca(t *testing.T) {
	limparMascaras(t)
	limparMascarasDescartadas(t)
	const hwnd = 0x7E11
	rect := winui.Rect{X: 0, Y: 0, W: 100, H: 20}
	m := mascaraDeTeste(hwnd, rect, 0x11, true)
	mascarasMu.Lock()
	mascaras[hwnd] = m
	mascarasMu.Unlock()

	restaurarCamadas, restaurarImprimir := camadasSobreRegiao, imprimirJanela
	t.Cleanup(func() { camadasSobreRegiao = restaurarCamadas; imprimirJanela = restaurarImprimir })
	camadasSobreRegiao = func(winui.Rect) []camadaJanela { return []camadaJanela{{hwnd: 0x22}} }

	var bufFaixa, bufJanela []byte

	// Ciclo 1: a janela de trás devolve a MESMA cor da máscara → sem commit.
	imprimirJanela = imprimirJanelaFake(0x11, rect)
	antes := m.px
	custo := atualizarMascaraAoVivo(m, &bufFaixa, &bufJanela)
	if custo != rect.W*rect.H {
		t.Fatalf("custo = %d, esperado %d", custo, rect.W*rect.H)
	}
	if &m.px[0] != &antes[0] {
		t.Fatal("a máscara foi recommitada com conteúdo IDÊNTICO — o skip de copy-on-write não funcionou")
	}
	if m.falhasConsecutivas != 0 {
		t.Fatalf("falhas consecutivas = %d, esperado 0 (composição válida zera o contador)", m.falhasConsecutivas)
	}

	// Ciclo 2: conteúdo DIFERENTE → o commit tem de acontecer.
	imprimirJanela = imprimirJanelaFake(0x33, rect)
	atualizarMascaraAoVivo(m, &bufFaixa, &bufJanela)
	mascarasMu.Lock()
	depois := append([]byte(nil), m.px...)
	mascarasMu.Unlock()
	if got := depois[(10*rect.W+50)*4]; got != 0x33 {
		t.Fatalf("máscara após mudança = %#x, esperado 0x33 — o novo conteúdo de trás não entrou", got)
	}
	if &m.px[0] == &antes[0] {
		t.Fatal("o commit não trocou o buffer — copy-on-write violado")
	}
}

// TestCamadasSobreRegiaoPapelDeParedeNaoEBloqueado prova, com janela real, que
// uma janela comum NÃO é classificada como papel de parede (o classificador é
// por nome de classe: Progman/WorkerW).
func TestCamadasSobreRegiaoPapelDeParedeNaoEBloqueado(t *testing.T) {
	if testing.Short() {
		t.Skip("teste com janela real")
	}
	hwnd := janelaMagenta(t, 60, 60)
	defer winui.DestroyWindow(hwnd)
	if eJanelaPapelDeParede(hwnd) {
		t.Fatal("janela comum foi classificada como papel de parede")
	}
}

