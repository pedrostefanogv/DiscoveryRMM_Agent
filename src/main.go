package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	appkg "discovery/app"
	"discovery/app/core/logger"
	"discovery/app/core/platform"
)

//go:embed all:frontend
var assets embed.FS

func main() {
	logger.RedirectStdLog(logger.LevelInfo)

	// ── Cleanup de .bak_update de self-update ──
	// O NSIS renomeia discovery-agent.exe -> discovery-agent.exe.bak_update
	// antes de copiar o novo binário. Após update bem-sucedido, o .bak_update
	// é removido pelo próprio NSIS. Aqui limpamos qualquer residual.
	if exePath, exeErr := os.Executable(); exeErr == nil {
		oldPath := exePath + ".bak_update"
		if _, statErr := os.Stat(oldPath); statErr == nil {
			_ = os.Remove(oldPath)
		}
	}

	// Help/version: exibe e sai sem iniciar a GUI.
	// showCLIHelp respeita a regra de nao misturar prefixos (-- vs / vs -).
	if showCLIHelp() {
		return
	}

	if note := strings.TrimSpace(suppressGameBarOverlay()); note != "" {
		logger.Info("gamebar overlay", "note", note)
	}

	startupDebugMode := detectStartupDebugMode() || hasStartupArg("--debug") || hasStartupArg("/debug") || hasStartupArg("-debug")
	startupMinimized := hasStartupArg("--startup-minimized") || hasStartupArg("/startup-minimized") || hasStartupArg("-startup-minimized")
	startupSource := firstNonEmpty(
		strings.TrimSpace(parseArgValue("--startup-source")),
		strings.TrimSpace(parseArgValue("/startup-source")),
		strings.TrimSpace(parseArgValue("-startup-source")),
	)
	startupWindowFrame, startupFrameless := resolveStartupWindowFrame()
	cleanupDeleteOnExit := hasStartupArg("--agent-delete-cleanup") || hasStartupArg("/agent-delete-cleanup") || hasStartupArg("-agent-delete-cleanup")

	if cleanupDeleteOnExit {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := appkg.RunAgentDecommissionCleanup(cleanupCtx); err != nil {
			log.Printf("[decommission] cleanup remoto finalizado com aviso: %v", err)
		}
		return
	}

	// ── Modo dispatcher do terminal ──
	// Lançado como subprocesso pelo agente (quando DISCOVERY_TERM_DISPATCHER
	// está ativo) para executar o ConPTY num processo filho isolado, reduzindo
	// a janela de injeção de DLL que causa o 0xC0000142. Não inicializa a GUI.
	if hasStartupArg("--terminal-dispatcher") || hasStartupArg("/terminal-dispatcher") || hasStartupArg("-terminal-dispatcher") {
		appkg.TerminalRunDispatcher()
		return
	}

	if startupDebugMode {
		log.Println("[startup] modo debug detectado: inicializando com servidor HTTP de debug (transitorio)")
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

	// ── Wails v3: aplicação explícita ──
	// Cria a aplicação, registra o App como service, cria a janela e executa.
	// O ciclo de vida (startup/shutdown) é tratado via ServiceStartup/ServiceShutdown.
	//
	// Os domínios remote debug e acesso remoto são registrados como Services
	// Wails v3 separados (via adapters thin) para dividir a carga de trabalho
	// do App. Os services são iniciados na ordem de registro e encerrados na
	// ordem inversa.
	appInstance := application.New(application.Options{
		Name: "Discovery",
		Services: []application.Service{
			application.NewService(app),
			application.NewService(app.RemoteDebugService()),
			application.NewService(app.RemoteSessionService()),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.discovery.app",
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				log.Printf("[single-instance] segunda abertura bloqueada. args=%v", data.Args)
				app.ShowMainWindow()
			},
		},
	})

	// Guarda a referência da aplicação no App para acesso a eventos/janela/tray.
	app.SetApplication(appInstance)

	window := appInstance.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "Discovery",
		Width:     appkg.WindowWidth,
		Height:    appkg.WindowHeight,
		MinWidth:  appkg.WindowMinWidth,
		MinHeight: appkg.WindowMinHeight,
		Frameless: startupFrameless,
		Hidden:    startupMinimized,
		// No v3 o drag de janela frameless é feito via CSS `--wails-draggable`.
		// O frontend já usa essa propriedade (CSSDragProperty do v2).
	})

	// Close-to-tray: intercepta o fechamento da janela.
	// Se o app deve esconder em vez de fechar, cancela o evento (impede o
	// hook interno de marcar a janela como "unconditionallyClose").
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if !app.ShouldHideOnClose() {
			return
		}
		if !app.IsTrayReady() {
			log.Println("[tray] close solicitado antes do tray ficar pronto; encerrando app para evitar estado sem menu")
			return
		}
		app.ClearMemoryCaches()
		window.Hide()
		event.Cancel()
	})

	// ── Ajuste de janela para área visível (DPI scaling) ──
	// Em telas com escala 125%/150% a janela padrão pode ultrapassar a área
	// de trabalho, escondendo o chrome (min/max/close). O clamp é aplicado:
	//   - no primeiro show da janela (WindowShow);
	//   - a cada resize (WindowDidResize) — cobre maximizar/restaurar;
	//   - quando o DPI muda (WindowDPIChanged) — cobre arrastar entre
	//     monitores com escalas diferentes.
	// A lógica é idempotente: se a janela já cabe na WorkArea, nada é feito.
	//
	// Debounce: WindowDidResize dispara dezenas de vezes durante um resize
	// interativo. SetSize/SetPosition usam InvokeSync (dispatch para a main
	// thread + wait), então aplicar o clamp a cada evento "brigaria" com o
	// arraste do usuário. O debounce de 150ms aplica o clamp só quando o
	// resize estabiliza.
	var fitDebounce *time.Timer
	var fitMu sync.Mutex
	scheduleFit := func() {
		fitMu.Lock()
		defer fitMu.Unlock()
		if fitDebounce != nil {
			fitDebounce.Stop()
		}
		fitDebounce = time.AfterFunc(150*time.Millisecond, func() {
			if window.IsMaximised() || window.IsFullscreen() {
				return
			}
			app.FitWindowToWorkArea()
		})
	}
	fitHook := func(event *application.WindowEvent) {
		if window.IsMaximised() || window.IsFullscreen() {
			return
		}
		scheduleFit()
	}
	window.RegisterHook(events.Common.WindowShow, fitHook)
	window.RegisterHook(events.Common.WindowDidResize, fitHook)
	window.RegisterHook(events.Common.WindowDPIChanged, fitHook)

	app.SetMainWindow(window)

	if err := appInstance.Run(); err != nil {
		log.Fatal(err)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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
	if hasStartupArg("--windowed-frame") || hasStartupArg("/windowed-frame") || hasStartupArg("-windowed-frame") {
		return "standard", false
	}

	frame := firstNonEmpty(
		strings.TrimSpace(parseArgValue("--window-frame")),
		strings.TrimSpace(parseArgValue("/window-frame")),
		strings.TrimSpace(parseArgValue("-window-frame")),
	)
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
