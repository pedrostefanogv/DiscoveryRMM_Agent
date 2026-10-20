package app

import (
	"errors"
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ── Criação preguiçosa da janela principal (otimização de RAM) ──────────────
//
// O agente iniciado automaticamente (tray/task de logon/restart de update) sobe
// com --startup-minimized e, até aqui, criava a janela mesmo oculta. No Wails v3
// a janela oculta AINDA cria o controller do WebView2 e navega
// (webview_window_windows.go: chromium.Show() + NavigationCompleted antes de
// Hide()), custando os processos do WebView2 (~230 MB medidos na máquina de
// referência) sem nunca mostrar nada ao usuário.
//
// Agora a janela só é criada quando alguém realmente precisa dela: clique no
// tray, segunda instância manual ou clique em toast de chat. Tray, IPC, SSE do
// chat e notificações nativas funcionam sem janela.
//
// A construção em si (options + hooks de close-to-tray e DPI) fica em main.go,
// que é quem tem acesso explícito à application do Wails; este arquivo só
// controla QUANDO ela acontece.

// SetWindowFactory registra a fábrica da janela principal. Idempotente: a
// última registrada vence (main.go registra uma única vez).
func (a *App) SetWindowFactory(factory func() application.Window) {
	a.windowMu.Lock()
	a.windowFactory = factory
	a.windowMu.Unlock()
}

// EnsureMainWindow cria (uma única vez) a janela principal, se ainda não
// existir. Idempotente e seguro para chamadas concorrentes (tray, segunda
// instância e clique em toast podem pedir a janela quase ao mesmo tempo).
// ensureMainWindow é o núcleo: cria e devolve a janela sob o mesmo mutex que
// publica a referência. Devolver o valor (em vez de o chamador reler o campo)
// evita corrida de dados na PRIMEIRA criação, quando a escrita acontece numa
// goroutine diferente da leitura (clique no tray vs. handler de segunda
// instância).
func (a *App) ensureMainWindow() (application.Window, error) {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()

	if a.mainWindow != nil {
		return a.mainWindow, nil
	}
	if a.windowFactory == nil {
		return nil, errors.New("fabrica da janela principal nao registrada")
	}
	// Marca o inicio da primeira tentativa: a abertura do chat usa essa marca
	// para NAO criar a janela do chat em cima de um WebView2 ainda nascendo
	// (main thread ocupada = interface congelada). So a primeira conta: a
	// carencia e medida a partir do comeco da criacao.
	if a.windowInitStarted.IsZero() {
		a.windowInitStarted = time.Now()
	}
	window := a.windowFactory()
	if window == nil {
		return nil, errors.New("fabrica da janela principal devolveu nil")
	}
	a.mainWindow = window
	log.Println("[window] janela principal criada sob demanda (WebView2 inicializado)")
	return window, nil
}

// EnsureMainWindow garante que a janela principal exista (idempotente).
func (a *App) EnsureMainWindow() error {
	_, err := a.ensureMainWindow()
	return err
}

// HasMainWindow informa se a janela principal já foi criada.
func (a *App) HasMainWindow() bool {
	return a.mainWindow != nil
}

// mainWindowInitGrace e quanto tempo a criacao preguicosa da janela principal
// e considerada "em curso" depois de comecar. Dentro desse periodo, abrir a
// janela do chat em paralelo custa dois WebView2 nascendo juntos na mesma main
// thread — o travamento completo da interface observado no log (UI para de
// responder ao IPC logo apos "janela principal criada sob demanda").
const mainWindowInitGrace = 15 * time.Second

// mainWindowInitPendingState e a decisao pura (testavel) de mainWindowInitPending:
// o lazy load da janela principal esta em curso (ou acabou de terminar dentro
// da carencia)?
func mainWindowInitPendingState(windowExists, factorySet bool, started, now time.Time) bool {
	if windowExists || !factorySet {
		return false
	}
	if started.IsZero() {
		return false
	}
	return now.Sub(started) < mainWindowInitGrace
}

// mainWindowInitPending informa se a criacao preguicosa da janela principal
// esta em curso (ou dentro da carencia logo apos). Enquanto estiver, a
// janela do chat NAO deve ser criada: espera o carregamento terminar e abre
// depois (ver scheduleSupportChatOpenAfterLazyLoad).
func (a *App) mainWindowInitPending() bool {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()
	return mainWindowInitPendingState(a.mainWindow != nil, a.windowFactory != nil, a.windowInitStarted, time.Now())
}
