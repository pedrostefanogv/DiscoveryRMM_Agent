package app

import (
	"log"
	"runtime/debug"
	"time"

	"github.com/energye/systray"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"discovery/internal/watchdog"
)

// startTray initialises the system-tray icon in a background goroutine.
// The icon bytes come from a.trayIcon (set via AppStartupOptions.TrayIcon).
// It must be called after the Wails context is stored in a.ctx.
func (a *App) startTray() {
	a.trayReady.Store(false)

	watchdog.SafeGo("tray-main", func() {
		// Start periodic heartbeat for tray
		if a.watchdogSvc != nil {
			heartbeat := watchdog.NewPeriodicHeartbeat(a.watchdogSvc, watchdog.ComponentTray, 20*time.Second)
			heartbeat.Start(a.ctx)
			defer heartbeat.Stop()
		}

		systray.Run(func() {
			if len(a.trayIcon) > 0 {
				setTrayIcon(a.trayIcon)
			}
			setTrayTitle("Discovery")
			setTrayTooltip("Discovery")

			// Send immediate heartbeat on tray ready
			if a.watchdogSvc != nil {
				a.watchdogSvc.Heartbeat(watchdog.ComponentTray)
			}

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
			log.Println("[tray] pronto: icone e menu inicializados")
		}, func() {
			a.trayReady.Store(false)
			log.Println("[tray] encerrado")
		})
	})
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
