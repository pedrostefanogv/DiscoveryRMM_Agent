package main

import (
	"context"
	"embed"
	"log"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"discovery/internal/mcp"
)

//go:embed all:frontend
var assets embed.FS

// Version is set at build time via ldflags:
//
//	go build -ldflags "-X main.Version=1.2.3"
var Version = "dev"

func main() {
	startupDebugMode := detectStartupDebugMode()
	startupMinimized := hasStartupArg("--startup-minimized")

	// If started with --mcp, run as a stdio MCP server (for Claude Desktop, etc).
	if hasStartupArg("--mcp") {
		runMCPServer()
		return
	}

	if startupDebugMode {
		log.Println("[startup] Shift/Ctrl detectado: inicializando em modo debug (transitorio)")
	}
	if startupMinimized {
		log.Println("[startup] execucao automatica detectada: iniciar minimizado no tray")
	}

	app := NewApp(AppStartupOptions{DebugMode: startupDebugMode, StartMinimized: startupMinimized})

	singleInstance := &options.SingleInstanceLock{
		UniqueId: "com.discovery.app",
		OnSecondInstanceLaunch: func(data options.SecondInstanceData) {
			log.Printf("[single-instance] segunda abertura bloqueada. args=%v", data.Args)
			if app.ctx == nil {
				return
			}
			wailsRuntime.WindowUnminimise(app.ctx)
			wailsRuntime.WindowShow(app.ctx)
			// Brief always-on-top toggle helps bring the existing window to foreground.
			wailsRuntime.WindowSetAlwaysOnTop(app.ctx, true)
			wailsRuntime.WindowSetAlwaysOnTop(app.ctx, false)
		},
	}

	err := wails.Run(&options.App{
		Title:     "Discovery",
		Width:     WindowWidth,
		Height:    WindowHeight,
		Frameless: true,
		// Keep right-click context menu enabled in production so users can use
		// built-in spellcheck suggestions/corrections in text fields.
		EnableDefaultContextMenu: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		OnBeforeClose: func(ctx context.Context) (prevent bool) {
			if !app.ShouldHideOnClose() {
				return false
			}
			if !app.IsTrayReady() {
				log.Println("[tray] close solicitado antes do tray ficar pronto; encerrando app para evitar estado sem menu")
				return false
			}
			// Limpar caches em memória antes de ir para o tray
			app.clearMemoryCaches()
			wailsRuntime.WindowHide(ctx)
			return true // hide to tray instead of quitting
		},
		SingleInstanceLock: singleInstance,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}

func hasStartupArg(arg string) bool {
	for _, value := range os.Args[1:] {
		if strings.EqualFold(strings.TrimSpace(value), arg) {
			return true
		}
	}
	return false
}

// runMCPServer starts the app in headless MCP server mode (JSON-RPC over stdio).
func runMCPServer() {
	app := NewApp(AppStartupOptions{})
	// Initialize a background context for the app services.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.ctx = ctx
	app.cancel = cancel

	srv := mcp.NewServer(app.GetMCPRegistry(), mcp.ServerInfo{
		Name:    "discovery",
		Version: Version,
	})

	log.SetOutput(os.Stderr) // keep logs out of the JSON-RPC stream
	log.Println("[mcp] servidor MCP iniciado via stdio")

	if err := srv.Run(ctx, os.Stdin, os.Stdout); err != nil {
		log.Fatalf("[mcp] erro: %v", err)
	}
}
