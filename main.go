package main

import (
	"context"
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend
var assets embed.FS

// Version is set at build time via ldflags:
//
//	go build -ldflags "-X main.Version=1.2.3"
var Version = "dev"

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:  "Discovery",
		Width:  WindowWidth,
		Height: WindowHeight,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		OnBeforeClose: func(ctx context.Context) (prevent bool) {
			wailsRuntime.WindowHide(ctx)
			return true // hide to tray instead of quitting
		},
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
