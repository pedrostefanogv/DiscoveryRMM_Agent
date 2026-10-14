package app

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestOpenScreenshotOverlayEsperaAberturaEmAndamento cobre a guarda que
// serializa as aberturas do overlay: um pedido que chega enquanto outra captura
// está sendo preparada NÃO pode iniciar uma segunda captura — ele espera a
// abertura em andamento e recebe a mesma sessão (ou o mesmo erro).
//
// Regressão do relato "sessao de captura expirada ou invalida": sem a guarda,
// cada clique durante os ~1 s de preparação criava a sua própria sessão, os
// restore das perdedoras eram descartados e o front terminava com um id que o
// backend já não tinha.
func TestOpenScreenshotOverlayEsperaAberturaEmAndamento(t *testing.T) {
	a := &App{}
	a.screenshotOpening = true
	a.screenshotOpenDone = make(chan struct{})

	go func() {
		time.Sleep(50 * time.Millisecond)
		a.screenshotMu.Lock()
		a.screenshotOpening = false
		a.screenshotOpenErr = errors.New("falha simulada da abertura")
		done := a.screenshotOpenDone
		a.screenshotOpenDone = nil
		a.screenshotMu.Unlock()
		close(done)
	}()

	_, err := a.openScreenshotOverlay("teste", false, false, screenshotDestinationAIChat)
	if err == nil || err.Error() != "falha simulada da abertura" {
		t.Fatalf("esperava o erro da abertura em andamento, obteve %v", err)
	}
}

// TestOpenScreenshotOverlayConcorrenteNaoTrava garante que N pedidos
// concorrentes (o log real do usuário tinha 16 em ~3 s) terminam sem deadlock e
// deixam a guarda de abertura liberada — nenhum deles pode ficar preso no canal.
func TestOpenScreenshotOverlayConcorrenteNaoTrava(t *testing.T) {
	a := &App{}
	const n = 16

	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := a.openScreenshotOverlay("teste", false, false, screenshotDestinationAIChat)
			errs[i] = err
		}(i)
	}
	close(start)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("openScreenshotOverlay travou com pedidos concorrentes")
	}

	for i, err := range errs {
		if err == nil {
			t.Fatalf("pedido %d: esperava erro (App sem janela), obteve nil", i)
		}
		// App de teste não tem janela principal NEM aplicação Wails: a falha
		// precisa ser imediata e determinística. A mensagem mudou quando o overlay
		// passou a tolerar a janela principal ausente (criação preguiçosa da UI no
		// autostart no tray) — o erro passou a vir da checagem da aplicação Wails.
		if !strings.Contains(err.Error(), "indisponivel") {
			t.Fatalf("pedido %d: erro inesperado: %v", i, err)
		}
	}

	a.screenshotMu.Lock()
	opening := a.screenshotOpening
	a.screenshotMu.Unlock()
	if opening {
		t.Fatal("guarda de abertura ficou presa em true")
	}
}

// TestRestoreOrphanScreenshotOverlay cobre a defesa contra a "tela toda preta":
// quando o front chama Finish/Cancel com um id que o backend já não tem, a
// janela precisa voltar aos bounds normais — e só quando NÃO há sessão viva.
func TestRestoreOrphanScreenshotOverlay(t *testing.T) {
	a := &App{}
	calls := 0
	a.screenshotRestore = func() { calls++ }

	a.restoreOrphanScreenshotOverlay()
	if calls != 1 {
		t.Fatalf("restore executado %d vez(es), esperado 1", calls)
	}

	// Idempotente: o closure foi consumido e não pode rodar de novo.
	a.restoreOrphanScreenshotOverlay()
	if calls != 1 {
		t.Fatalf("restore executado de novo (%d vezes)", calls)
	}

	// Com sessão VIVA os bounds do overlay são legítimos: não mexer.
	a.screenshotRestore = func() { calls++ }
	a.screenshotOverlay = &screenshotOverlaySession{id: "shot-ativo"}
	a.restoreOrphanScreenshotOverlay()
	if calls != 1 {
		t.Fatalf("restore rodou com sessão ativa (%d vezes)", calls)
	}
	a.screenshotMu.Lock()
	kept := a.screenshotRestore != nil
	a.screenshotMu.Unlock()
	if !kept {
		t.Fatal("closure da sessão viva foi descartado")
	}
}
