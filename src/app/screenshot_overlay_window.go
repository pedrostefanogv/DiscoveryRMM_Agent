package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// ── Janela DEDICADA do overlay de captura ───────────────────────────────────
//
// Por que existe: até aqui o overlay de seleção reaproveitava a JANELA
// PRINCIPAL (ela era redimensionada para cobrir o desktop virtual). O pedido de
// print da janela do chat de suporte chegava por evento ao frontend da janela
// principal — que normalmente está no tray enquanto o usuário conversa com o
// suporte. Nesse estado o Wails mantém o controller do WebView2 oculto
// (chromium.Hide), e o SetWindowPos cru (SWP_SHOWWINDOW) exibe a moldura da
// janela SEM nada pintado: um retângulo preto always-on-top cobrindo o desktop,
// do qual o usuário só saía fechando a janela principal. Os logs do agent
// mostravam exatamente isso (close-to-tray 8–10 s depois de cada pedido, sem
// nenhum erro registrado).
//
// A janela dedicada é independente da principal: criada sob demanda, escondida
// entre capturas, exibida SEMPRE pelo caminho normal do Wails (Show → o
// controller do WebView volta a ficar visível), então não há como ficar preta.
// O payload da tela congelada é PUXADO por ela (ScreenshotOverlayNext), o que
// elimina a corrida "evento emitido antes de a página registrar o handler".

const (
	screenshotOverlayWindowName = "screenshot-overlay"
	// screenshotOverlayReadyWait é o teto de espera pelo carregamento da página
	// da janela dedicada (primeira captura). Depois disso a janela é reusada.
	screenshotOverlayReadyWait = 12 * time.Second
	// screenshotOverlayNextWait limita cada pull do payload dentro da janela do
	// overlay; ao estourar, o frontend tenta de novo (janela viva entre capturas).
	screenshotOverlayNextWait = 2 * time.Minute
)

// screenshotOverlayWindow devolve a janela dedicada (nil quando ainda não existe).
func (a *App) screenshotOverlayWindow() application.Window {
	a.screenshotOverlayWinMu.Lock()
	defer a.screenshotOverlayWinMu.Unlock()
	return a.screenshotOverlayWin
}

// ensureScreenshotOverlayWindow cria (idempotente) a janela dedicada do overlay.
func (a *App) ensureScreenshotOverlayWindow() (application.Window, error) {
	a.screenshotOverlayWinMu.Lock()
	if win := a.screenshotOverlayWin; win != nil {
		a.screenshotOverlayWinMu.Unlock()
		return win, nil
	}
	appInstance := a.app
	if appInstance == nil {
		a.screenshotOverlayWinMu.Unlock()
		return nil, fmt.Errorf("aplicacao Wails indisponivel para a janela de captura")
	}

	ready := make(chan struct{})
	deliver := make(chan string, 1)
	win := appInstance.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:        screenshotOverlayWindowName,
		Title:       "Seleção de captura",
		Frameless:   true,
		Hidden:      true,
		AlwaysOnTop: true,
		Width:       640,
		Height:      480,
		URL:         "/screenshot-overlay.html",
	})
	a.screenshotOverlayWin = win
	a.screenshotOverlayReady = ready
	a.screenshotOverlayReadyFlag = false
	a.screenshotOverlayDeliver = deliver
	a.screenshotOverlayWinMu.Unlock()

	// Fechamento pelo usuário (Alt+F4): a sessão é encerrada e as janelas do
	// chat/principal voltam. A janela é recriada na próxima captura.
	win.RegisterHook(events.Common.WindowClosing, func(*application.WindowEvent) {
		go a.abortScreenshotOverlay("janela de captura fechada")
		a.forgetScreenshotOverlayWindow(win)
	})

	a.Logs.Append("[screenshot] janela dedicada de captura criada")
	return win, nil
}

// forgetScreenshotOverlayWindow descarta as referências quando a janela do
// overlay é fechada pelo usuário (a próxima captura cria uma nova).
func (a *App) forgetScreenshotOverlayWindow(win application.Window) {
	a.screenshotOverlayWinMu.Lock()
	defer a.screenshotOverlayWinMu.Unlock()
	if a.screenshotOverlayWin != win {
		return
	}
	a.screenshotOverlayWin = nil
	a.screenshotOverlayReady = nil
	a.screenshotOverlayReadyFlag = false
	a.screenshotOverlayDeliver = nil
}

// ScreenshotOverlayReady é chamado pelo frontend da janela dedicada quando o
// documento carregou. Exportado como binding do Wails.
func (a *App) ScreenshotOverlayReady() {
	a.screenshotOverlayWinMu.Lock()
	ready := a.screenshotOverlayReady
	if a.screenshotOverlayReadyFlag {
		ready = nil
	}
	a.screenshotOverlayReadyFlag = true
	a.screenshotOverlayWinMu.Unlock()
	if ready != nil {
		close(ready)
	}
}

// waitScreenshotOverlayReady espera a página da janela dedicada carregar.
func (a *App) waitScreenshotOverlayReady(win application.Window) error {
	a.screenshotOverlayWinMu.Lock()
	ready := a.screenshotOverlayReady
	already := a.screenshotOverlayReadyFlag
	a.screenshotOverlayWinMu.Unlock()
	if already || ready == nil {
		return nil
	}
	select {
	case <-ready:
		return nil
	case <-time.After(screenshotOverlayReadyWait):
		a.Logs.Append("[screenshot] aviso: pagina da janela de captura nao carregou em " +
			screenshotOverlayReadyWait.String())
		return fmt.Errorf("janela de captura nao carregou a tempo")
	}
}

// ScreenshotOverlayNext entrega o payload da tela congelada para a janela
// dedicada. É um pull: a janela chama em laço e fica esperando a próxima
// captura; sem payload devolve string vazia (o frontend tenta de novo).
//
// Exportado como binding do Wails.
func (a *App) ScreenshotOverlayNext() (string, error) {
	a.screenshotOverlayWinMu.Lock()
	deliver := a.screenshotOverlayDeliver
	a.screenshotOverlayWinMu.Unlock()
	if deliver == nil {
		return "", fmt.Errorf("janela de captura indisponivel")
	}
	select {
	case payload := <-deliver:
		return payload, nil
	case <-time.After(screenshotOverlayNextWait):
		return "", nil
	}
}

// deliverScreenshotOverlayPayload publica o payload para a janela dedicada,
// descartando um payload anterior que ninguém consumiu (sessão órfã).
func (a *App) deliverScreenshotOverlayPayload(payloadJSON string) {
	if strings.TrimSpace(payloadJSON) == "" {
		return
	}
	a.screenshotOverlayWinMu.Lock()
	deliver := a.screenshotOverlayDeliver
	a.screenshotOverlayWinMu.Unlock()
	if deliver == nil {
		return
	}
	select {
	case <-deliver:
	default:
	}
	select {
	case deliver <- payloadJSON:
	default:
	}
}

// drainScreenshotOverlayDeliver limpa um payload pendente (fim da sessão): sem
// isso a janela do overlay redesenharia uma captura já encerrada.
func (a *App) drainScreenshotOverlayDeliver() {
	a.screenshotOverlayWinMu.Lock()
	deliver := a.screenshotOverlayDeliver
	a.screenshotOverlayWinMu.Unlock()
	if deliver == nil {
		return
	}
	select {
	case <-deliver:
	default:
	}
}

// hideScreenshotOverlayWindow esconde a janela dedicada (fim da sessão).
func (a *App) hideScreenshotOverlayWindow() {
	win := a.screenshotOverlayWindow()
	if win == nil {
		return
	}
	win.Hide()
}
