package app

import (
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Contrato da decisao pura do lazy load: enquanto a criacao preguicosa da
// janela principal esta em curso (e por uma carencia curta depois), a janela
// do chat NAO deve ser criada em paralelo — dois WebView2 nascendo juntos na
// mesma main thread congelam a interface inteira (relato 20/10).
func TestMainWindowInitPendingState(t *testing.T) {
	now := time.Now()
	recente := now.Add(-2 * time.Second)
	velho := now.Add(-mainWindowInitGrace - time.Second)

	casos := []struct {
		nome     string
		pendente bool
		params   [4]any
	}{
		{"janela existe: nunca pendente", false, [4]any{true, true, recente, now}},
		{"sem fabrica: nada para esperar", false, [4]any{false, false, recente, now}},
		{"fabrica registrada, criacao nunca comecou: nao pendente", false, [4]any{false, true, time.Time{}, now}},
		{"criacao em curso: pendente", true, [4]any{false, true, recente, now}},
		{"carencia expirada: abre mesmo assim", false, [4]any{false, true, velho, now}},
	}
	for _, c := range casos {
		p := mainWindowInitPendingState(c.params[0].(bool), c.params[1].(bool), c.params[2].(time.Time), c.params[3].(time.Time))
		if p != c.pendente {
			t.Fatalf("%s: esperado pendente=%v, obtido %v", c.nome, c.pendente, p)
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

// Quando o lazy load TERMINA (janela criada), o agendador abre o chat em
// seguida em vez de abandonar a abertura ("abrir a janela do chat depois").
func TestScheduleSupportChatOpenAfterLazyLoad_AbreDepoisDoLoad(t *testing.T) {
	a := NewApp(AppStartupOptions{ServiceMode: true})
	// Lazy load concluido: carencia expirada e janela inexistente (a.app nil
	// faz openSupportChatWindow registrar a indisponibilidade e voltar).
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
	abriu := false
	for _, l := range a.Logs.GetAll() {
		if strings.Contains(l, "janela principal carregada; abrindo o chat") {
			abriu = true
		}
	}
	if !abriu {
		t.Fatal("esperado log de abertura do chat apos o lazy load")
	}
}
