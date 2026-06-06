package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	appkg "discovery/app"
	"discovery/internal/platform"
)

//go:embed all:frontend
var assets embed.FS

func main() {
	if note := strings.TrimSpace(suppressGameBarOverlay()); note != "" {
		log.Printf("[startup][gamebar] %s", note)
	}

	startupDebugMode := detectStartupDebugMode() || hasStartupArg("--debug")
	startupMinimized := hasStartupArg("--startup-minimized")
	startupSource := strings.TrimSpace(parseArgValue("--startup-source"))
	startupWindowFrame, startupFrameless := resolveStartupWindowFrame()
	cleanupDeleteOnExit := hasStartupArg("--agent-delete-cleanup")

	if cleanupDeleteOnExit {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := appkg.RunAgentDecommissionCleanup(cleanupCtx); err != nil {
			log.Printf("[decommission] cleanup remoto finalizado com aviso: %v", err)
		}
		return
	}

	if startupDebugMode {
		log.Println("[startup] --debug detectado: inicializando em modo debug (transitorio)")
	}
	// Always inject frontend assets — debug HTTP server may be activated
	// dynamically during startup() if Shift/Ctrl is detected.
	// Strip the "frontend/" prefix from the embed.FS so the HTTP server
	// can serve paths as "index.html", "app.js", etc. directly.
	frontendSub, subErr := fs.Sub(assets, "frontend")
	if subErr != nil {
		log.Fatalf("[startup] falha ao resolver frontend assets: %v", subErr)
	}
	appkg.SetDebugFrontendAssets(http.FS(frontendSub))
	if startupSource == "" {
		if startupMinimized {
			startupSource = "autostart"
		} else {
			startupSource = "manual"
		}
	}
	log.Printf("[startup] origem da execucao: %s", startupSource)
	if startupMinimized {
		log.Println("[startup] execucao automatica detectada: iniciar minimizado no tray")
	}
	log.Printf("[startup][window] frame=%s frameless=%t width=%d height=%d startMinimized=%t", startupWindowFrame, startupFrameless, appkg.WindowWidth, appkg.WindowHeight, startupMinimized)

	app := appkg.NewApp(appkg.AppStartupOptions{
		DebugMode:            startupDebugMode,
		StartMinimized:       startupMinimized,
		TrayIcon:             trayIconICO,
		TrayProvisioningIcon: trayProvisioningICO,
		TrayOfflineIcon:      trayOfflineICO,
	})

	singleInstance := &options.SingleInstanceLock{
		UniqueId: "com.discovery.app",
		OnSecondInstanceLaunch: func(data options.SecondInstanceData) {
			log.Printf("[single-instance] segunda abertura bloqueada. args=%v", data.Args)
			ctx := app.Ctx()
			if ctx == nil {
				return
			}
			wailsRuntime.WindowUnminimise(ctx)
			wailsRuntime.WindowShow(ctx)
			wailsRuntime.WindowSetAlwaysOnTop(ctx, true)
			wailsRuntime.WindowSetAlwaysOnTop(ctx, false)
		},
	}

	err := wails.Run(&options.App{
		Title:                    "Discovery",
		Width:                    appkg.WindowWidth,
		Height:                   appkg.WindowHeight,
		MinWidth:                 appkg.WindowMinWidth,
		MinHeight:                appkg.WindowMinHeight,
		Frameless:                startupFrameless,
		StartHidden:              startupMinimized,
		CSSDragProperty:          "--wails-draggable",
		CSSDragValue:             "drag",
		EnableDefaultContextMenu: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  appkg.AppStartup(app),
		OnShutdown: appkg.AppShutdown(app),
		OnBeforeClose: func(ctx context.Context) (prevent bool) {
			if !app.ShouldHideOnClose() {
				return false
			}
			if !app.IsTrayReady() {
				log.Println("[tray] close solicitado antes do tray ficar pronto; encerrando app para evitar estado sem menu")
				return false
			}
			app.ClearMemoryCaches()
			wailsRuntime.WindowHide(ctx)
			return true
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

func parseArgValue(argName string) string {
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(strings.ToLower(arg), strings.ToLower(argName)+"=") {
			return arg[len(argName)+1:]
		}
	}
	return ""
}

func resolveStartupWindowFrame() (string, bool) {
	if hasStartupArg("--windowed-frame") {
		return "standard", false
	}

	frame := strings.TrimSpace(parseArgValue("--window-frame"))
	if frame == "" {
		frame = platform.WindowFrame()
	}

	switch strings.ToLower(frame) {
	case "", "frameless":
		return "frameless", true
	case "standard", "framed", "windowed":
		return "standard", false
	default:
		log.Printf("[startup][window] valor invalido para frame %q; usando frameless", frame)
		return "frameless", true
	}
}
