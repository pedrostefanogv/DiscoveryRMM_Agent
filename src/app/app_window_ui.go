package app

import (
	"log"
	"time"
)

// SetApplication guarda a referência da aplicação (contrato local uiApp; o
// adapter do Wails é injetado por SetApplicationWails no build taggado).
//
//wails:ignore
func (a *App) SetApplication(app uiApp) {
	a.app = app
}

// SetMainWindow guarda a referência da janela principal (contrato local
// uiWindow; o adapter do Wails é injetado por SetMainWindowWails no build
// taggado).
//
//wails:ignore
func (a *App) SetMainWindow(window uiWindow) {
	a.mainWindow = window
}

// ShowMainWindow restaura e mostra a janela principal (usado no single-instance).
// Também garante que a janela fique dentro da área de trabalho visível
// (proteção contra DPI scaling 125%/150% que esconde o chrome da janela).
//
//wails:ignore
func (a *App) ShowMainWindow() {
	// Criação preguiçosa: em autostart no tray a janela não existe até agora,
	// e é justamente aqui (clique no tray, segunda instância, clique em toast)
	// que ela passa a ser necessária. Sem isso o clique "não faria nada".
	win := a.mainWindow
	if win == nil {
		created, err := a.ensureMainWindow()
		if err != nil {
			log.Printf("[window] falha ao criar janela principal sob demanda: %v", err)
			return
		}
		win = created
	}
	if win == nil {
		return
	}
	// raiseWindowToFront faz o mesmo "truque" de topmost que existia aqui e
	// acrescenta o que faltava: restauração explícita, força bruta de primeiro
	// plano no Windows (foreground lock) e piscar da barra de tarefas quando o
	// foco é recusado — ver window_foreground*.go.
	raiseWindowToFront(win, false)
	a.FitWindowToWorkArea()

	// Abrir a interface é o "despertar" natural do usuário: confere se existe
	// convite de chat pendente que se perdeu (canal unicast sem replay). O atraso
	// curto deixa o webview carregar. Não roda quando já existe sessão/janela
	// local: o Refresh poderia apagar um convite "waiting" numa oscilação do
	// servidor antes de o usuário responder.
	if !a.supportChatWindowOpen() && !a.supportChatService().State().HasSession {
		go a.resumeSupportChatIfActive(500 * time.Millisecond)
	}
}

// EmitEvent emite um evento customizado para o frontend (v3).
//
// No modo serviço (SYSTEM) não existe frontend — o evento é repassado às
// UIs companion conectadas via IPC named pipe (PLANO_AGENT_SERVICE_SYSTEM.md,
// Fase 2). Notificações "notification:new", connectivity, chat:question etc.
// chegam à UI renderizada na sessão do usuário sem acoplamento extra.
//
//wails:ignore
func (a *App) EmitEvent(name string, data ...any) {
	data = normalizeEventArgs(data)
	if a.app == nil {
		// Sem app Wails (serviço ou headless): repassa via IPC se houver UI.
		a.broadcastIPCEvent(name, data...)
		return
	}
	a.app.EmitEvent(name, data...)
}

// normalizeEventArgs converte a forma multi-argumento do EmitEvent
// ("chave", valor, "chave", valor, ...) em UM único mapa, que é o formato que os
// handlers do frontend consomem (`data.connected`, `data.updates`, ...).
//
// Por que: no Wails v3 (EventManager.Emit) MAIS DE UM argumento faz
// Event.Data ser o slice de argumentos e o runtime JS entrega `ev.data` cru —
// os handlers liam `data.<campo>` em um ARRAY (undefined) e interpretavam o
// evento como vazio. Era o mesmo defeito de raiz do chat "servidor offline":
// aqui ele é eliminado em TODOS os caminhos (UI standalone e serviço→IPC).
//
// As demais formas permanecem intactas: 0 argumentos, 1 mapa, 1 valor cru,
// 1 nil e qualquer sequência que não seja estritamente pares string/valor.
func normalizeEventArgs(data []any) []any {
	if len(data) < 2 || len(data)%2 != 0 {
		return data
	}
	pairs := make(map[string]any, len(data)/2)
	for i := 0; i < len(data); i += 2 {
		key, ok := data[i].(string)
		if !ok {
			return data
		}
		pairs[key] = data[i+1]
	}
	return []any{pairs}
}

// HideMainWindow esconde a janela principal (close-to-tray).
//
//wails:ignore
func (a *App) HideMainWindow() {
	if a.mainWindow == nil {
		return
	}
	a.mainWindow.Hide()
}

// MinimiseMainWindow minimiza a janela principal.
//
//wails:ignore
func (a *App) MinimiseMainWindow() {
	if a.mainWindow == nil {
		return
	}
	a.mainWindow.Minimise()
}

// QuitApp encerra a aplicação (v3).
//
//wails:ignore
func (a *App) QuitApp() {
	if a.QuitRequested.Swap(true) {
		return
	}
	if a.app == nil {
		return
	}
	a.app.Quit()
}

func (a *App) RequestAppClose() {
	a.closeMu.Lock()
	a.allowClose = true
	a.closeMu.Unlock()
}

func (a *App) ShouldHideOnClose() bool {
	a.closeMu.RLock()
	defer a.closeMu.RUnlock()
	return !a.allowClose
}

func (a *App) IsTrayReady() bool {
	return a.trayReady.Load()
}

func (a *App) hideWindowOnStartup() {
	// Com a criação preguiçosa da janela (autostart no tray) não há janela para
	// esconder: o WebView2 sequer foi inicializado. Nada a fazer.
	if a.mainWindow == nil {
		log.Println("[startup] janela principal adiada (autostart no tray): WebView2 só inicia na primeira abertura")
		return
	}
	// B4: safeGo em vez de goroutine crua — recovery de panic no watcher do tray.
	a.safeGo(func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		timeout := time.NewTimer(12 * time.Second)
		defer timeout.Stop()

		for {
			select {
			case <-a.ctx.Done():
				return
			case <-timeout.C:
				log.Println("[startup] aviso: timeout aguardando tray para iniciar minimizado")
				return
			case <-ticker.C:
				if !a.IsTrayReady() {
					continue
				}
				a.MinimiseMainWindow()
				a.HideMainWindow()
				log.Println("[startup] janela iniciada minimizada no tray")
				return
			}
		}
	})
}

func (a *App) ClearMemoryCaches() { a.clearMemoryCaches() }

func (a *App) clearMemoryCaches() {
	a.AgentInfo.Invalidate()
	a.AppStorePolicy.Invalidate()

	a.InvCache.Reset()

	// Devolve o page cache do SQLite que ficou quente com as leituras grandes
	// (ex.: a política efetiva da app-store, ~4,7 MB comprimidos). Sem isto o
	// cache de páginas continuava segurando os dados mesmo com os caches de
	// aplicação esvaziados.
	if a.CoreAgent.DB != nil {
		if err := a.CoreAgent.DB.ShrinkMemory(); err != nil {
			log.Printf("[tray] shrink_memory na limpeza de caches: %v", err)
		}
	}

	log.Println("[tray] caches em memória limpos para economizar recursos")
}
