package app

import (
	"errors"
	"log"

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
