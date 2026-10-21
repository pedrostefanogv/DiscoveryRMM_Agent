// webview-sim é um DIAGNÓSTICO isolado: reproduz (ou não) o travamento da
// SEGUNDA janela WebView2 do processo, com a matriz de modos:
//
//	a = sem argumentos de browser extras (baseline)
//	b = argumentos EXATOS do agent (renderer-process-limit=1 etc.)
//	c = argumentos do agent SEM renderer-process-limit
//
// Cada modo: cria a janela 1 (visível), espera assentar, cria a janela 2
// OCULTA (como o prewarm do chat) e mede o tempo de criação de cada uma.
// Se a criação passar de 45 s, imprime "REPRODUZIDO" e sai com código 3.
package main

import (
	"embed"
	"fmt"
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed index.html
var assets embed.FS

type step struct {
	evento string
	ms     int64
}

func ts() string { return time.Now().Format("15:04:05.000") }

func bools(b bool) string {
	if b {
		return "sim"
	}
	return "nao"
}

// iniciaCartaoSim replica a peça que o sim ainda não tinha: um POPUP topmost
// nativo em THREAD PRÓPRIA (como o cartão do convite). Opcional via modo 'e'.
func iniciaCartaoSim(fechar chan struct{}) {
	go func() {
		runtime.LockOSThread()
		var modUser32 = windows.NewLazySystemDLL("user32.dll")
		var modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
		procGetMessage := modUser32.NewProc("GetMessageW")
		procCreateEvent := modKernel32.NewProc("CreateEventW")
		procMsgWait := modUser32.NewProc("MsgWaitForMultipleObjectsEx")
		ev, _, _ := procCreateEvent.Call(0, 0, 0, 0)
		defer windows.CloseHandle(windows.Handle(ev))
		for {
			select {
			case <-fechar:
				return
			default:
			}
			// Bombeia mensagens da própria thread (qualquer janela dela).
			procMsgWait.Call(1, uintptr(ev), 200, 0x4|0x2) // MWMO_INPUTAVAILABLE|MWMO_WAITALL-ish
			var msg [48]byte
			procGetMessage.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
		}
	}()
}

// criaJanela roda NewWithOptions em goroutine e devolve o tempo gasto;
// estoura o prazo com exit(3) — travamento reproduzido.
func criaJanela(app *application.App, opts application.WebviewWindowOptions, rotulo string) {
	inicio := time.Now()
	done := make(chan struct{})
	go func() {
		w := app.Window.NewWithOptions(opts)
		_ = w
		close(done)
	}()
	select {
	case <-done:
		fmt.Printf("[%s] %s: OK em %d ms\n", ts(), rotulo, time.Since(inicio).Milliseconds())
	case <-time.After(45 * time.Second):
		fmt.Printf("[%s] %s: REPRODUZIDO — NewWithOptions travado >45 s\n", ts(), rotulo)
		os.Exit(3)
	}
}

func main() {
	// A main thread precisa de COM STA antes do Run (senão a criação do
	// WebView2 falha com CO_E_NOTINITIALIZED).
	runtime.LockOSThread()
	var modOle32 = windows.NewLazySystemDLL("ole32.dll")
	hr, _, _ := modOle32.NewProc("CoInitializeEx").Call(0, 2) // 2 = COINIT_APARTMENTTHREADED
	if hr != 0 && hr != 1 { // S_OK=0, S_FALSE=1
		fmt.Printf("CoInitializeEx: HRESULT 0x%08x\n", hr)
		os.Exit(1)
	}
	modo := "a"
	if len(os.Args) > 1 {
		modo = os.Args[1]
	}
	var args []string
	switch modo {
	case "b", "d", "e":
		args = []string{
			"--renderer-process-limit=1",
			"--js-flags=--max-old-space-size=256",
			"--disable-background-networking",
		}
	case "c":
		args = []string{
			"--js-flags=--max-old-space-size=256",
			"--disable-background-networking",
		}
	}
	udf := os.TempDir() + string(os.PathSeparator) + "webview-sim-" + modo
	// SIM_UDF sobrepõe a pasta de dados (teste contra UDF real copiada).
	if v := os.Getenv("SIM_UDF"); v != "" {
		udf = v
	}
	os.MkdirAll(udf, 0o755)

	fmt.Printf("[%s] === modo %s: iniciando (udf=%s) ===\n", ts(), modo, udf)

	app := application.New(application.Options{
		Name: "webview-sim-" + modo,
		Windows: application.WindowsOptions{
			WebviewUserDataPath:  udf,
			AdditionalBrowserArgs: args,
			DisabledFeatures:     []string{"msEdgeSidebarV2", "msEdgeAutofill"},
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})

	// A criação ANTES do Run é adiada pelo Wails para dentro dele; a sequência
	// roda em goroutine e o Run fica na main thread (COM STA desta thread).
	// d: replica o AGENT — principal FRAMELESS + janela 2/3 com as opções EXATAS
	// da janela do chat (AlwaysOnTop, tamanho travado, botões ocultos).
	frameless := modo == "d"
	comCartao := modo == "e"
	fecharCartao := make(chan struct{})
	if comCartao {
		iniciaCartaoSim(fecharCartao)
		fmt.Printf("[%s] cartao nativo simulado em thread propria (modo e)\n", ts())
	}
	chatOpts := func(titulo string, hidden bool) application.WebviewWindowOptions {
		return application.WebviewWindowOptions{
			Title:                 titulo,
			Width:                 520,
			Height:                680,
			MinWidth:              520,
			MinHeight:             680,
			MaxWidth:              520,
			MaxHeight:             680,
			DisableResize:         true,
			CloseButtonState:      application.ButtonHidden,
			MaximiseButtonState:   application.ButtonDisabled,
			FullscreenButtonState: application.ButtonDisabled,
			AlwaysOnTop:           true,
			URL:                   "/index.html",
			Hidden:                hidden,
		}
	}

	go func() {
		criaJanela(app, application.WebviewWindowOptions{
			Title:     "SIM W1 (" + modo + ")",
			Width:     420,
			Height:    300,
			URL:       "/index.html",
			Frameless: frameless,
		}, "w1 visivel (frameless=" + bools(frameless) + ")")

		time.Sleep(4 * time.Second) // assenta: navegação/pintura da w1 concluída

		criaJanela(app, chatOpts("SIM W2 oculta (" + modo + ")", true), "w2 oculta estilo chat")

		time.Sleep(2 * time.Second)

		criaJanela(app, chatOpts("SIM W3 visivel (" + modo + ")", false), "w3 visivel estilo chat")

		fmt.Printf("[%s] === modo %s: completo sem travamento ===\n", ts(), modo)
		close(fecharCartao)
		os.Exit(0)
	}()

	app.Run()
}
