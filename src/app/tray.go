package app

import (
	"log"
	"runtime/debug"
	"time"

	"github.com/energye/systray"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
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
	state := a.currentTrayIconState()
	if a.trayIconState.Load() == state {
		return
	}
	icon := a.trayIconForState(state)
	if len(icon) == 0 {
		return
	}
	setTrayIcon(icon)
	a.trayIconState.Store(state)
}

func (a *App) runTrayStateLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.syncTrayVisualState()
		}
	}
}

// startTray initialises the system-tray icon in a background goroutine.
// The icon bytes come from a.trayIcon (set via AppStartupOptions.TrayIcon).
// It must be called after the Wails context is stored in a.ctx.
func (a *App) startTray() {
	a.trayReady.Store(false)
	a.trayIconState.Store(trayIconStateUnknown)

	go func() {
		trayStop := make(chan struct{})

		systray.Run(func() {
			setTrayTitle("Discovery")
			setTrayTooltip("Discovery")

			systray.SetOnClick(func(menu systray.IMenu) {
				a.safeTrayAction("tray-click", func() {
					wailsRuntime.WindowUnminimise(a.ctx)
					wailsRuntime.WindowShow(a.ctx)
				})
			})
			systray.SetOnDClick(func(menu systray.IMenu) {
				a.safeTrayAction("tray-double-click", func() {
					wailsRuntime.WindowUnminimise(a.ctx)
					wailsRuntime.WindowShow(a.ctx)
				})
			})

			mShow := systray.AddMenuItem("Abrir", "Mostrar a janela")
			mShow.Click(func() {
				a.safeTrayAction("tray-menu-open", func() {
					wailsRuntime.WindowUnminimise(a.ctx)
					wailsRuntime.WindowShow(a.ctx)
				})
			})

			systray.AddSeparator()

			mQuit := systray.AddMenuItem("Sair", "Encerrar o aplicativo")
			mQuit.Click(func() {
				a.safeTrayAction("tray-menu-quit", func() {
					a.RequestAppClose()
					go wailsRuntime.Quit(a.ctx)
				})
			})

			a.trayReady.Store(true)
			a.syncTrayVisualState()
			go a.runTrayStateLoop(trayStop)
			log.Println("[tray] pronto: icone e menu inicializados")
		}, func() {
			close(trayStop)
			a.trayReady.Store(false)
			a.trayIconState.Store(trayIconStateUnknown)
			log.Println("[tray] encerrado")
		})
	}()
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
	if !efficiencyModeEnabled {
		setTrayTitle("Discovery")
		setTrayTooltip("Discovery")
		return
	}

	if !supported {
		setTrayTitle("Discovery")
		setTrayTooltip("Discovery")
		return
	}

	if idle {
		setTrayTitle("Discovery Eco")
		setTrayTooltip("Discovery - Modo de eficiencia ativo (aguardo)")
		return
	}

	setTrayTitle("Discovery")
	setTrayTooltip("Discovery - Processando")
}
