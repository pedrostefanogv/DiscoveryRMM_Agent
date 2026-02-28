package main

import (
	_ "embed"

	"github.com/energye/systray"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed build/windows/icon.ico
var trayIconICO []byte

// startTray initialises the system-tray icon in a background goroutine.
// It must be called after the Wails context is stored in a.ctx.
func (a *App) startTray() {
	go systray.Run(func() {
		systray.SetIcon(trayIconICO)
		systray.SetTitle("Discovery")
		systray.SetTooltip("Discovery – Winget Store")

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
			systray.Quit()
			wailsRuntime.Quit(a.ctx)
		})
	}, nil)
}
