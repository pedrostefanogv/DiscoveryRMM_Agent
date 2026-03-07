package main

import (
	_ "embed"
	"time"

	"github.com/energye/systray"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"winget-store/internal/watchdog"
)

//go:embed build/windows/icon.ico
var trayIconICO []byte

// startTray initialises the system-tray icon in a background goroutine.
// It must be called after the Wails context is stored in a.ctx.
func (a *App) startTray() {
	watchdog.SafeGo("tray-main", func() {
		// Start periodic heartbeat for tray
		if a.watchdogSvc != nil {
			heartbeat := watchdog.NewPeriodicHeartbeat(a.watchdogSvc, watchdog.ComponentTray, 20*time.Second)
			heartbeat.Start(a.ctx)
			defer heartbeat.Stop()
		}

		systray.Run(func() {
			setTrayIcon(trayIconICO)
			setTrayTitle("Discovery")
			setTrayTooltip("Discovery - Winget Store")

			// Send immediate heartbeat on tray ready
			if a.watchdogSvc != nil {
				a.watchdogSvc.Heartbeat(watchdog.ComponentTray)
			}

			systray.SetOnClick(func(menu systray.IMenu) {
				wailsRuntime.WindowShow(a.ctx)
			})
			systray.SetOnDClick(func(menu systray.IMenu) {
				wailsRuntime.WindowShow(a.ctx)
			})

			mShow := systray.AddMenuItem("Abrir", "Mostrar a janela")
			mShow.Click(func() {
				wailsRuntime.WindowShow(a.ctx)
			})

			systray.AddSeparator()

			mQuit := systray.AddMenuItem("Sair", "Encerrar o aplicativo")
			mQuit.Click(func() {
				a.RequestAppClose()
				wailsRuntime.Quit(a.ctx)
			})
		}, nil)
	})
}

func (a *App) updateTrayIdleState(idle bool, supported bool) {
	if !supported {
		setTrayTitle("Discovery")
		setTrayTooltip("Discovery - Winget Store")
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
