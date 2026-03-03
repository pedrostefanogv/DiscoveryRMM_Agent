package main

import (
	"context"
	"embed"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"winget-store/internal/mcp"
)

//go:embed all:frontend
var assets embed.FS

// Version is set at build time via ldflags:
//
//	go build -ldflags "-X main.Version=1.2.3"
var Version = "dev"

func main() {
	// If started with --mcp, run as a stdio MCP server (for Claude Desktop, etc).
	if len(os.Args) > 1 && os.Args[1] == "--mcp" {
		runMCPServer()
		return
	}

	app := NewApp()

	err := wails.Run(&options.App{
		Title:  "Discovery",
		Width:  WindowWidth,
		Height: WindowHeight,
		// Keep right-click context menu enabled in production so users can use
		// built-in spellcheck suggestions/corrections in text fields.
		EnableDefaultContextMenu: true,
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

// runMCPServer starts the app in headless MCP server mode (JSON-RPC over stdio).
func runMCPServer() {
	app := NewApp()
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
