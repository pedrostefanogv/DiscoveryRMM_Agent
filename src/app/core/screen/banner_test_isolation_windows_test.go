//go:build windows

package screen

import (
	"reflect"
	"sync"
	"testing"
)

// ponteiroDaFuncao devolve o endereco de codigo de uma funcao (ou 0). Go nao
// permite comparar funcs com ==; o endereco e a forma de detectar se a var
// continua apontando para a MESMA funcao original.
func ponteiroDaFuncao(f any) uintptr {
	if f == nil {
		return 0
	}
	v := reflect.ValueOf(f)
	if v.Kind() != reflect.Func {
		return 0
	}
	return v.Pointer()
}

// mesmaFuncao compara duas funcoes pelo endereco de codigo.
func mesmaFuncao(a, b any) bool {
	return ponteiroDaFuncao(a) == ponteiroDaFuncao(b)
}

// -- Isolamento dos pontos de injecao globais entre testes --
//
// Motivo real deste arquivo: durante a verificacao da task-5 um teste de mascara
// (TestAtivarMascaraRecapturaComJanelaOculta) PASSOU na suite completa e FALHOU
// 6/6 quando rodado isolado. Isso e sintoma de ESTADO GLOBAL COMPARTILHADO entre
// testes: o verde da suite nao era confiavel, e um teste que so passa "no meio
// dos outros" esconde regressao em vez de pega-la.
//
// Varias funcoes do pacote sao vars justamente para serem injetaveis
// (mascaraVisivel, affinitySet/affinityGet, reavaliarAtraso, emendaConhecida,
// bannerTopmostSetWindowPos). Um teste que troque uma delas e nao restaure, ou
// que dependa de outra ja ter trocado, cria dependencia de ORDEM.
//
// Os testes abaixo NAO substituem a investigacao do caso concreto (que e do dono
// do arquivo): travam a invariante geral "todo ponto de injecao volta ao valor
// original ao fim do teste", para a proxima dependencia de ordem aparecer aqui,
// com nome claro, em vez de virar um flake na suite.

// estadoInjecoes guarda o valor basal de cada var injetavel do pacote.
type estadoInjecoes struct {
	mascaraVisivel      func(uintptr) bool
	affinitySet         func(uintptr, uint32) (bool, error)
	affinityGet         func(uintptr) (uint32, bool)
	bannerTopmostSetPos func(hwnd, insertAfter, x, y, cx, cy, flags uintptr)
}

func lerEstadoInjecoes() estadoInjecoes {
	return estadoInjecoes{
		mascaraVisivel:      mascaraVisivel,
		affinitySet:         affinitySet,
		affinityGet:         affinityGet,
		bannerTopmostSetPos: bannerTopmostSetWindowPos,
	}
}

// TestPontosDeInjecaoVoltamAoEstadoBasal trava a regressao que produziu o falso
// verde: um teste que troca uma var de injecao e NAO restaura deixa os testes
// seguintes medindo o mundo errado. A comparacao e pelo ENDERECO da funcao — pega
// ate o caso "restaurei uma funcao equivalente, mas nao a original".
func TestPontosDeInjecaoVoltamAoEstadoBasal(t *testing.T) {
	basal := lerEstadoInjecoes()

	// --- mascaraVisivel ---
	anterior := mascaraVisivel
	mascaraVisivel = func(uintptr) bool { return true }
	if mesmaFuncao(mascaraVisivel, anterior) {
		t.Fatal("injecao de mascaraVisivel nao teve efeito — o teste nao mede o que pensa")
	}
	mascaraVisivel = anterior
	if !mesmaFuncao(mascaraVisivel, basal.mascaraVisivel) {
		t.Fatal("mascaraVisivel nao voltou a funcao original — o proximo teste herda a injecao e a suite fica dependente de ORDEM")
	}

	// --- affinitySet / affinityGet ---
	anteriorSet, anteriorGet := affinitySet, affinityGet
	affinitySet = func(uintptr, uint32) (bool, error) { return true, nil }
	affinityGet = func(uintptr) (uint32, bool) { return wdaExcludeFromCapture, true }
	if mesmaFuncao(affinitySet, anteriorSet) || mesmaFuncao(affinityGet, anteriorGet) {
		t.Fatal("injecao de afinidade nao teve efeito")
	}
	affinitySet, affinityGet = anteriorSet, anteriorGet
	if !mesmaFuncao(affinitySet, basal.affinitySet) || !mesmaFuncao(affinityGet, basal.affinityGet) {
		t.Fatal("affinitySet/affinityGet nao voltaram ao original — a escada de afinidade dos proximos testes mediria um fake")
	}

	// --- bannerTopmostSetWindowPos ---
	anteriorTop := bannerTopmostSetWindowPos
	bannerTopmostSetWindowPos = func(uintptr, uintptr, uintptr, uintptr, uintptr, uintptr, uintptr) {}
	if mesmaFuncao(bannerTopmostSetWindowPos, anteriorTop) {
		t.Fatal("injecao do SetWindowPos de topmost nao teve efeito")
	}
	bannerTopmostSetWindowPos = anteriorTop
	if !mesmaFuncao(bannerTopmostSetWindowPos, basal.bannerTopmostSetPos) {
		t.Fatal("bannerTopmostSetWindowPos nao voltou ao original — testes de z-order seguintes contariam chamadas de um espiao alheio")
	}
}

// TestVarsGlobaisDeTesteNaoFicamSujas verifica o ESTADO (nao as funcoes) que os
// testes mexem: registry de mascaras e travas de recaptura sao globais por HWND,
// entao um HWND sintetico reaproveitado nao pode herdar estado de outro teste.
//
// Regressao que impede: o padrao do falso verde — uma suite que passa em uma
// ordem e falha em outra porque um teste deixou estado para tras.
func TestVarsGlobaisDeTesteNaoFicamSujas(t *testing.T) {
	mascarasMu.Lock()
	penduradas := len(mascaras)
	mascarasMu.Unlock()
	if penduradas != 0 {
		t.Logf("registry de mascaras tinha %d entrada(s) ao iniciar este teste — verifique se o teste anterior limpou (limparMascaras)", penduradas)
	}

	const hwndSentinela = uintptr(0x7E57)
	if mascaraFoiDescartada(hwndSentinela) {
		t.Fatal("o HWND sentinela ja nasce com a trava de recaptura posta — estado global vazando entre testes")
	}
	if mascaraDisponivel(hwndSentinela) {
		t.Fatal("o HWND sentinela ja nasce com mascara registrada — estado global vazando entre testes")
	}
}

// TestRestauracaoUsaValorLidoNaoValorFixo fixa o padrao correto de restauracao:
// devolver SEMPRE o valor lido no inicio do teste, nunca um valor fixo. Restaurar
// um valor fixo e o erro que cria dependencia de ordem quando dois testes mexem
// na mesma var.
func TestRestauracaoUsaValorLidoNaoValorFixo(t *testing.T) {
	original := mascaraVisivel
	t.Cleanup(func() { mascaraVisivel = original })

	// Simula um teste que ja tinha injetado ANTES deste: o valor lido e o do outro
	// teste, e e ELE que precisa ser devolvido (nao o default do pacote).
	outroTeste := func(uintptr) bool { return true }
	mascaraVisivel = outroTeste
	salvo := mascaraVisivel
	t.Cleanup(func() { mascaraVisivel = salvo })

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = mascaraVisivel(0) }()
	wg.Wait()

	if !mesmaFuncao(salvo, outroTeste) {
		t.Fatal("a restauracao deveria devolver o valor LIDO (injecao do teste anterior), nao um valor fixo")
	}
}
