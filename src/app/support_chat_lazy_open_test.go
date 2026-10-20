package app

import (
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Contrato da decisao pura do lazy load: enquanto a criacao preguicosa da
// janela principal esta em curso — E enquanto o webview dela carrega depois
// da criacao (mainWindowInitGraceAfter) — a janela do chat NAO deve ser
// criada em paralelo. O travamento de 20/10 (16:38) comecou ~3 s DEPOIS de
// "janela principal criada sob demanda"; o dump mostra ServeWebViewRequest
// em voo — criar outro webview nesse intervalo congela a interface.
func TestMainWindowInitPendingState(t *testing.T) {
	now := time.Now()
	inicio := now.Add(-2 * time.Second)
	velho := now.Add(-mainWindowInitGrace - time.Second)
	criadoAgora := now.Add(-1 * time.Second)
	criadoVelho := now.Add(-mainWindowInitGraceAfter - time.Second)

	casos := []struct {
		nome     string
		esperado bool
		exists   bool
		factory  bool
		started  time.Time
		created  time.Time
	}{
		{"janela criada agora: webview ainda carregando", true, true, true, inicio, criadoAgora},
		{"janela criada ha tempo: livre", false, true, true, inicio, criadoVelho},
		{"janela existe sem marca de criacao (defensivo): livre", false, true, true, inicio, time.Time{}},
		{"criacao em curso: pendente", true, false, true, inicio, time.Time{}},
		{"carencia expirou sem janela: WebView2 travado (deixa o agendador desistir)", false, false, true, velho, time.Time{}},
		{"fabrica registrada, criacao nunca comecou: nao pendente", false, false, true, time.Time{}, time.Time{}},
		{"sem fabrica: nada para esperar", false, false, false, inicio, time.Time{}},
	}
	for _, c := range casos {
		p := mainWindowInitPendingState(c.exists, c.factory, c.started, c.created, now)
		if p != c.esperado {
			t.Fatalf("%s: esperado pendente=%v, obtido %v", c.nome, c.esperado, p)
		}
	}
}

// O agendador DESISTE no prazo quando o lazy load nao termina (WebView2
// travado de vez): libera a deduplicacao, nao reabre em loop e registra o
// motivo — a reconciliacao/retomada cobre a abertura depois.
func TestScheduleSupportChatOpenAfterLazyLoad_DesisteNoPrazo(t *testing.T) {
	a := NewApp(AppStartupOptions{ServiceMode: true})
	a.windowFactory = func() application.Window { return nil }
	a.windowInitStarted = time.Now()

	guardado := supportChatLazyOpenWait
	poll := supportChatLazyOpenPoll
	supportChatLazyOpenWait = 300 * time.Millisecond
	supportChatLazyOpenPoll = 50 * time.Millisecond
	defer func() { supportChatLazyOpenWait = guardado; supportChatLazyOpenPoll = poll }()

	a.scheduleSupportChatOpenAfterLazyLoad()

	deadline := time.Now().Add(3 * time.Second)
	for a.supportChatLazyOpenRequested.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if a.supportChatLazyOpenRequested.Load() {
		t.Fatal("o agendador deveria ter desistido no prazo e liberado a deduplicacao")
	}
	entrou := false
	for _, l := range a.Logs.GetAll() {
		if strings.Contains(l, "nao terminou de carregar no prazo") {
			entrou = true
		}
	}
	if !entrou {
		t.Fatal("esperado log de desistencia do lazy load")
	}
}

// BUG (revisao): carencia expirada SEM janela = WebView2 travado de vez. O
// agendador NAO deve logar "carregada" (log mentiroso) nem tentar abrir em
// cima de uma main thread congelada — desiste pelo caminho "travada".
func TestScheduleSupportChatOpenAfterLazyLoad_CarenciaExpiradaSemJanela_DesisteComoTravada(t *testing.T) {
	a := NewApp(AppStartupOptions{ServiceMode: true})
	a.windowFactory = func() application.Window { return nil }
	a.windowInitStarted = time.Now().Add(-mainWindowInitGrace - time.Second)

	a.scheduleSupportChatOpenAfterLazyLoad()

	deadline := time.Now().Add(3 * time.Second)
	for a.supportChatLazyOpenRequested.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if a.supportChatLazyOpenRequested.Load() {
		t.Fatal("o agendador deveria ter concluido e liberado a deduplicacao")
	}
	var travada, mentira bool
	for _, l := range a.Logs.GetAll() {
		if strings.Contains(l, "nao terminou de carregar (travada)") {
			travada = true
		}
		if strings.Contains(l, "janela principal carregada; abrindo o chat") {
			mentira = true
		}
	}
	if !travada {
		t.Fatal("esperado log de desistencia no caminho \"travada\"")
	}
	if mentira {
		t.Fatal("nao deve logar \"janela principal carregada\" sem a janela existir")
	}
}
