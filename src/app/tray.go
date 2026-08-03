package app

import (
	"fmt"
	"log"
	"os/exec"
	"runtime/debug"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"discovery/internal/processutil"
)

const (
	trayIconStateUnknown int32 = iota
	trayIconStateNormal
	trayIconStateProvisioning
	trayIconStateOffline
)

func resolveTrayIconState(configured bool, connected bool) int32 {
	if !configured {
		return trayIconStateProvisioning
	}
	if !connected {
		return trayIconStateOffline
	}
	return trayIconStateNormal
}

func (a *App) currentTrayIconState() int32 {
	configured := isAgentConfigured()
	if !configured {
		return resolveTrayIconState(false, false)
	}
	status := a.GetAgentStatus()
	return resolveTrayIconState(true, status.Connected)
}

func (a *App) trayIconForState(state int32) []byte {
	switch state {
	case trayIconStateProvisioning:
		if len(a.trayProvisioning) > 0 {
			return a.trayProvisioning
		}
	case trayIconStateOffline:
		if len(a.trayOffline) > 0 {
			return a.trayOffline
		}
	}
	return a.trayIcon
}

func (a *App) syncTrayVisualState() {
	if a.systemTray == nil {
		return
	}
	state := a.currentTrayIconState()
	if a.trayIconState.Load() == state {
		return
	}
	icon := a.trayIconForState(state)
	if len(icon) == 0 {
		return
	}
	a.systemTray.SetIcon(icon)
	a.trayIconState.Store(state)
}

func (a *App) runTrayStateLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.syncTrayVisualState()
		}
	}
}

// startTray initialises the system-tray icon using the Wails v3 native tray.
// The icon bytes come from a.trayIcon (set via AppStartupOptions.TrayIcon).
// It must be called after the Wails application is created (a.app != nil).
func (a *App) startTray() {
	if a.app == nil {
		log.Println("[tray] aviso: aplicação Wails não disponível; tray não iniciado")
		return
	}

	a.trayReady.Store(false)
	a.trayIconState.Store(trayIconStateUnknown)

	// Cria o tray nativo do Wails v3.
	tray := a.app.SystemTray.New()
	a.systemTray = tray

	// Ícone inicial (estado atual).
	if icon := a.trayIconForState(a.currentTrayIconState()); len(icon) > 0 {
		tray.SetIcon(icon)
	}
	tray.SetTooltip("Discovery")

	// Clique simples/duplo: mostra a janela.
	tray.OnClick(func() {
		a.safeTrayAction("tray-click", func() {
			a.ShowMainWindow()
		})
	})
	tray.OnDoubleClick(func() {
		a.safeTrayAction("tray-double-click", func() {
			a.ShowMainWindow()
		})
	})

	// Menu do tray.
	menu := a.app.Menu.New()

	menu.Add("Abrir").OnClick(func(_ *application.Context) {
		a.safeTrayAction("tray-menu-open", func() {
			a.ShowMainWindow()
		})
	})

	// Debug-only: "Abrir no navegador" menu item.
	// Only shown when the debug HTTP server is running.
	if a.runtimeFlags.DebugMode && a.GetDebugHTTPPort() > 0 {
		url := fmt.Sprintf("http://127.0.0.1:%d", a.GetDebugHTTPPort())
		menu.Add("Abrir no navegador").OnClick(func(_ *application.Context) {
			a.safeTrayAction("tray-menu-browser", func() {
				cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
				processutil.HideWindow(cmd)
				_ = cmd.Start()

				// Log na UI
				a.logs.append("[debug-http] abrindo " + url + " no navegador")
			})
		})
	}

	menu.AddSeparator()

	menu.Add("Sair").OnClick(func(_ *application.Context) {
		a.safeTrayAction("tray-menu-quit", func() {
			a.RequestAppClose()
			go a.QuitApp()
		})
	})

	tray.SetMenu(menu)

	a.trayReady.Store(true)
	a.syncTrayVisualState()
	go a.runTrayStateLoop()
	log.Println("[tray] pronto: icone e menu inicializados (Wails v3 nativo)")
}

func (a *App) safeTrayAction(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[tray] PANIC em callback '%s': %v\n%s", name, r, debug.Stack())
		}
	}()
	fn()
}

func (a *App) updateTrayIdleState(idle bool, supported bool) {
	if a == nil || !a.trayReady.Load() || a.systemTray == nil {
		return
	}

	a.safeTrayAction("tray-idle-state", func() {
		if !efficiencyModeEnabled {
			a.systemTray.SetTooltip("Discovery")
			return
		}

		if !supported {
			a.systemTray.SetTooltip("Discovery")
			return
		}

		if idle {
			a.systemTray.SetTooltip("Discovery - Modo de eficiencia ativo (aguardo)")
			return
		}

		a.systemTray.SetTooltip("Discovery - Processando")
	})
}

// syncRemoteSessionTray atualiza o tooltip da tray com indicacao de sessoes
// remotas ativas, fornecendo visibilidade ao usuario local.
func (a *App) syncRemoteSessionTray() {
	if a == nil || !a.trayReady.Load() || a.systemTray == nil {
		return
	}

	a.safeTrayAction("tray-remote-session", func() {
		count := a.activeRemoteSessions.Load()
		if count <= 0 {
			// Sem sessoes ativas — volta ao estado normal (idle state loop cuida do resto)
			return
		}
		tooltip := "Discovery"
		if count == 1 {
			tooltip = "Discovery - 1 sessao remota ativa"
		} else {
			tooltip = fmt.Sprintf("Discovery - %d sessoes remotas ativas", count)
		}
		a.systemTray.SetTooltip(tooltip)
	})
}
