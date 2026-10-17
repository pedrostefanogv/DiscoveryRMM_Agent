//go:build windows

package remotesession

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── Cache do PATH: single-flight, concorrência e liveness ──
//
// Interface: cada diretório do PATH é listado fora do mutex, com timeout curto
// próprio e single-flight por chave. Sem isso, TABs concorrentes serializavam e
// um diretório UNC morto bloqueava todos, ignorando o ctx do pedido.

// TestCompletionPathIndex_ConcorrentesFazemUmUnicoScan prova o single-flight:
// 25 pedidos simultâneos com a mesma chave = uma única varredura.
func TestCompletionPathIndex_ConcorrentesFazemUmUnicoScan(t *testing.T) {
	ix := newCompletionPathIndex()
	var varreduras int32
	scan := func(context.Context, []string) []string {
		atomic.AddInt32(&varreduras, 1)
		time.Sleep(50 * time.Millisecond) // janela para os demais entrarem no voo
		return []string{"git", "go"}
	}

	iniciar := make(chan struct{})
	tamanhos := make(chan int, 25)
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-iniciar
			tamanhos <- len(ix.get(context.Background(), []string{"C:\\Ferramentas"}, scan))
		}()
	}
	close(iniciar)
	wg.Wait()
	close(tamanhos)

	for n := range tamanhos {
		if n != 2 {
			t.Fatalf("pedido concorrente recebeu %d nomes, want 2", n)
		}
	}
	if n := atomic.LoadInt32(&varreduras); n != 1 {
		t.Fatalf("varreduras = %d com 25 pedidos concorrentes, want 1", n)
	}
}

// TestCompletionPathIndex_CtxCanceladoNaoEsperaVarredura garante que o pedido
// cujo ctx foi cancelado retorna na hora, mesmo com a varredura em andamento.
func TestCompletionPathIndex_CtxCanceladoNaoEsperaVarredura(t *testing.T) {
	ix := newCompletionPathIndex()
	bloqueio := make(chan struct{})
	defer close(bloqueio) // libera a goroutine da varredura no fim do teste

	scan := func(context.Context, []string) []string {
		<-bloqueio
		return []string{"git"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	resultado := make(chan []string, 1)
	go func() { resultado <- ix.get(ctx, []string{"C:\\Morto"}, scan) }()

	esperarVarreduraEmVoo(t, ix)
	cancel()

	select {
	case nomes := <-resultado:
		if nomes != nil {
			t.Fatalf("ctx cancelado deveria devolver nil; obtidos %v", nomes)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ctx cancelado ficou esperando a varredura (deveria retornar rapido)")
	}
}

// TestCompletionPathIndex_CtxJaCanceladoNaoVarre evita trabalho inútil.
func TestCompletionPathIndex_CtxJaCanceladoNaoVarre(t *testing.T) {
	ix := newCompletionPathIndex()
	var varreduras int32
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nomes := ix.get(ctx, []string{"C:\\A"}, func(context.Context, []string) []string {
		atomic.AddInt32(&varreduras, 1)
		return []string{"git"}
	})
	if nomes != nil || atomic.LoadInt32(&varreduras) != 0 {
		t.Fatalf("ctx ja cancelado deveria devolver nil sem varrer; nomes=%v varreduras=%d", nomes, varreduras)
	}
}

// TestScanPathExecutables_DeduplicaDiretoriosSemDiferenciarCaixa cobre PATH
// repetido (inclusive com caixa/barra final diferentes): lista uma vez só e não
// duplica nomes.
func TestScanPathExecutables_DeduplicaDiretoriosSemDiferenciarCaixa(t *testing.T) {
	var (
		mu       sync.Mutex
		chamadas = make(map[string]int)
	)
	listDir := func(dir string) ([]completionFSEntry, error) {
		chave := strings.ToLower(filepath.Clean(dir))
		mu.Lock()
		chamadas[chave]++
		mu.Unlock()
		switch chave {
		case "c:\\ferramentas":
			return []completionFSEntry{entryFile("Git.exe"), entryFile("GIT.EXE"), entryFile("go.exe")}, nil
		case "c:\\outro":
			return []completionFSEntry{entryFile("git.exe")}, nil
		}
		return nil, fmt.Errorf("diretorio inexistente: %s", dir)
	}

	dirs := []string{"C:\\Ferramentas", "c:\\ferramentas", "C:\\Ferramentas\\", "C:\\Outro", "C:\\Outro", "  "}
	nomes := scanPathExecutables(context.Background(), dirs, listDir, nil, time.Second)

	if len(nomes) != 2 {
		t.Fatalf("nomes = %v, want 2 (Git.exe/go.exe deduplicados sem caixa)", nomes)
	}
	for _, quer := range []string{"Git.exe", "go.exe"} {
		if !contemTextoNomes(nomes, quer) {
			t.Fatalf("nome %q ausente; obtidos %v", quer, nomes)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for chave, n := range chamadas {
		if n != 1 {
			t.Fatalf("diretorio %q foi listado %d vezes, want 1", chave, n)
		}
	}
	if len(chamadas) != 2 {
		t.Fatalf("diretorios listados = %v, want 2 unicos", chamadas)
	}
}

// TestScanPathExecutables_ListerLentoNaoBloqueiaDemais prova o timeout por
// diretório: um lister que ignora o ctx não impede os demais diretórios.
func TestScanPathExecutables_ListerLentoNaoBloqueiaDemais(t *testing.T) {
	bloqueio := make(chan struct{})
	defer close(bloqueio)

	listDir := func(dir string) ([]completionFSEntry, error) {
		if strings.EqualFold(dir, "C:\\Morto") {
			<-bloqueio // simula UNC morto: ReadDir preso por minutos
		}
		return []completionFSEntry{entryFile("git.exe")}, nil
	}

	inicio := time.Now()
	nomes := scanPathExecutables(context.Background(), []string{"C:\\Morto", "C:\\Bom"}, listDir, nil, 50*time.Millisecond)
	decorrido := time.Since(inicio)

	if decorrido > 2*time.Second {
		t.Fatalf("varredura levou %v; o lister lento deveria ser cortado pelo timeout por diretorio", decorrido)
	}
	if len(nomes) != 1 || !strings.EqualFold(nomes[0], "git.exe") {
		t.Fatalf("nomes = %v, want [git.exe] do diretorio bom", nomes)
	}
}

// TestScanPathExecutables_TruncaAposDeduplicarEOrdenar cobre o limite: o corte
// em completionPathMaxEntries acontece depois de deduplicar e ordenar.
func TestScanPathExecutables_TruncaAposDeduplicarEOrdenar(t *testing.T) {
	entries := make([]completionFSEntry, 0, completionPathMaxEntries+200)
	for i := 0; i < completionPathMaxEntries; i++ {
		entries = append(entries, entryFile(fmt.Sprintf("n%04d.exe", i)))
	}
	// Duplicatas de caixa dos primeiros nomes: sem dedupe, consumiriam o limite.
	for i := 0; i < 100; i++ {
		entries = append(entries, entryFile(fmt.Sprintf("N%04d.EXE", i)))
	}
	entries = append(entries, entryFile("zzz.exe"))

	listDir := func(string) ([]completionFSEntry, error) { return entries, nil }
	nomes := scanPathExecutables(context.Background(), []string{"C:\\A"}, listDir, nil, time.Second)

	if len(nomes) != completionPathMaxEntries {
		t.Fatalf("nomes = %d, want %d", len(nomes), completionPathMaxEntries)
	}
	if nomes[0] != "n0000.exe" || nomes[len(nomes)-1] != "n3999.exe" {
		t.Fatalf("corte nao foi alfabetico: primeiro=%q ultimo=%q", nomes[0], nomes[len(nomes)-1])
	}
}

// esperarVarreduraEmVoo bloqueia até o single-flight estar em andamento.
func esperarVarreduraEmVoo(t *testing.T, ix *completionPathIndex) {
	t.Helper()
	limite := time.Now().Add(2 * time.Second)
	for time.Now().Before(limite) {
		ix.mu.Lock()
		emVoo := ix.inflight != nil
		ix.mu.Unlock()
		if emVoo {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("varredura nao entrou em voo")
}

// contemTextoNomes informa se a lista contém o nome (comparação case-insensitive).
func contemTextoNomes(nomes []string, quer string) bool {
	for _, n := range nomes {
		if strings.EqualFold(n, quer) {
			return true
		}
	}
	return false
}
