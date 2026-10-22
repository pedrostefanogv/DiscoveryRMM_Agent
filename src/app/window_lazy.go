package app

import (
	"errors"
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// ── Criação preguiçosa da janela principal (otimização de RAM) ──────────────
//
// O agente iniciado automaticamente (tray/task de logon/restart de update) sobe
// com --startup-minimized e, até aqui, criava a janela mesmo oculta. No Wails v3
// a janela oculta AINDA cria o controller do WebView2 e navega
// (webview_window_windows.go: chromium.Show() + NavigationCompleted antes de
// Hide()), custando os processos do WebView2 (~230 MB medidos na máquina de
// referência) sem nunca mostrar nada ao usuário.
//
// Agora a janela só é criada quando alguém realmente precisa dela: clique no
// tray, segunda instância manual ou clique em toast de chat. Tray, IPC, SSE do
// chat e notificações nativas funcionam sem janela.
//
// A construção em si (options + hooks de close-to-tray e DPI) fica em main.go,
// que é quem tem acesso explícito à application do Wails; este arquivo só
// controla QUANDO ela acontece.

// SetWindowFactory registra a fábrica da janela principal. Idempotente: a
// última registrada vence (main.go registra uma única vez).
func (a *App) SetWindowFactory(factory func() application.Window) {
	a.windowMu.Lock()
	a.windowFactory = factory
	a.windowMu.Unlock()
}

// EnsureMainWindow cria (uma única vez) a janela principal, se ainda não
// existir. Idempotente e seguro para chamadas concorrentes (tray, segunda
// instância e clique em toast podem pedir a janela quase ao mesmo tempo).
// ensureMainWindow é o núcleo: cria e devolve a janela sob o mesmo mutex que
// publica a referência. Devolver o valor (em vez de o chamador reler o campo)
// evita corrida de dados na PRIMEIRA criação, quando a escrita acontece numa
// goroutine diferente da leitura (clique no tray vs. handler de segunda
// instância).
func (a *App) ensureMainWindow() (application.Window, error) {
	for {
		a.windowMu.Lock()
		if a.mainWindow != nil {
			a.windowMu.Unlock()
			return a.mainWindow, nil
		}
		if a.windowFactory == nil {
			a.windowMu.Unlock()
			return nil, errors.New("fabrica da janela principal nao registrada")
		}
		// Marca o inicio da primeira tentativa: a abertura do chat usa essa marca
		// para NAO criar a janela do chat em cima de um WebView2 ainda nascendo
		// (main thread ocupada = interface congelada). So a primeira conta: a
		// carencia e medida a partir do comeco da criacao.
		if a.windowInitStarted.IsZero() {
			a.windowInitStarted = time.Now()
		}
		factory := a.windowFactory
		a.windowMu.Unlock()

		// FIX 21/10: single-flight de criação SEM segurar windowMu. A factory
		// bloqueia a goroutine chamadora até o WebView2 nascer (InvokeSync para
		// a main thread) — segurar o mutex nesse intervalo deixaria chamadores
		// que rodam na PRÓPRIA main thread presos no mutex enquanto a main
		// thread é justamente quem precisa executar a criação. Com o aquecimento
		// em segundo plano (warmMainWindowHidden) a criação passa a nascer de
		// uma goroutine qualquer, então quem perder este CAS espera sem prender
		// nenhum mutex.
		if a.windowCreating.CompareAndSwap(false, true) {
			// defer roda no retorno da FUNÇÃO (uma única passagem do loop cria).
			defer a.windowCreating.Store(false)
			// DIAGNÓSTICO 21/10: amostra a main thread durante o nascimento do
			// WebView2; se travar, loga + minidump imediato (ver watchdog).
			liberaVigia := a.vigiaCriacaoJanela("janela principal")
			window := factory()
			liberaVigia()
			a.windowMu.Lock()
			defer a.windowMu.Unlock()
			if a.mainWindow != nil {
				// Defensivo: outra criação venceu — a publicada é a válida.
				return a.mainWindow, nil
			}
			if window == nil {
				return nil, errors.New("fabrica da janela principal devolveu nil")
			}
			// A janela NOVA carrega do zero: o sinal de conteudo anterior (de uma
			// janela antiga) nao vale para ela — senao a janela do chat seria
			// criada sobre o WebView2 ainda nascendo da janela recriada.
			a.mainWindowContentLoaded.Store(false)
			a.mainWindow = window
			a.windowInitCompletedAt = time.Now()
			log.Println("[window] janela principal criada sob demanda (WebView2 inicializado)")
			return window, nil
		}

		// Outra goroutine está criando AGORA. Espera sem prender mutex: se ela
		// vencer, devolve a janela publicada; se falhar, assume a criação na
		// próxima volta do loop externo.
		for a.windowCreating.Load() {
			a.windowMu.Lock()
			done := a.mainWindow
			a.windowMu.Unlock()
			if done != nil {
				return done, nil
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// EnsureMainWindow garante que a janela principal exista (idempotente).
func (a *App) EnsureMainWindow() error {
	_, err := a.ensureMainWindow()
	return err
}

// HasMainWindow informa se a janela principal já foi criada.
func (a *App) HasMainWindow() bool {
	return a.mainWindow != nil
}

// mainWindowInitGrace e quanto tempo a criacao preguicosa da janela principal
// e considerada "em curso" depois de comecar. Dentro desse periodo, abrir a
// janela do chat em paralelo custa dois WebView2 nascendo juntos na mesma main
// thread — o travamento completo da interface observado no log (UI para de
// responder ao IPC logo apos "janela principal criada sob demanda").
const mainWindowInitGrace = 15 * time.Second

// mainWindowInitContentLoadedGrace e quanto tempo DEPOIS da janela principal
// ser criada o webview dela ainda tem direito de carregar SEM o sinal do
// frontend (build antigo, frontend quebrado). O log do travamento de 20/10
// (16:38 e 20:37) mostra a UI parando de responder ao IPC segundos DEPOIS de
// "janela principal criada sob demanda" — a criacao do objeto Window volta
// antes de o WebView2 terminar de subir, e criar a janela do chat nesse
// intervalo reproduz o congelamento (dump: ServeWebViewRequest em voo). Nesta
// maquina o WebView2 chegou a levar ~54 s para nascer: a carencia antiga de
// 8 s expirava antes do sinal e a janela do chat era criada sobre a main
// thread ocupada — por isso a carencia sem sinal agora cobre o nascimento
// inteiro e, com o sinal real, a abertura acontece na hora.
const mainWindowInitContentLoadedGrace = 90 * time.Second

// mainWindowInitPendingState e a decisao pura (testavel) de mainWindowInitPending:
// o lazy load da janela principal esta em curso — ANTES da criacao (carencia
// de mainWindowInitGrace), DEPOIS dela enquanto o webview carrega SEM o sinal
// real do frontend (mainWindowInitContentLoadedGrace), ou ate o sinal chegar
// (contentLoaded=true encerra a pendencia na hora).
//
// FIX 20/10 (20:37): a carencia antiga por RELOGIO (8 s) expirava antes de o
// WebView2 desta maquina terminar de nascer (~54 s) e openSupportChatWindow
// criava a janela do chat sobre a main thread ocupada — a interface inteira
// congelava. O sinal de conteudo (MainWindowContentReady, app-init.js) agora
// governa; a carencia e apenas o fallback para frontend que nao sinaliza.
func mainWindowInitPendingState(windowExists, factorySet bool, started, completed, now time.Time, contentLoaded bool) bool {
	if !factorySet {
		return false
	}
	if windowExists {
		if completed.IsZero() {
			// Defensivo: criada sem marca (builds antigos em memoria).
			return false
		}
		if contentLoaded {
			// Sinal REAL de prontidao: o webview da janela principal carregou.
			return false
		}
		return now.Sub(completed) < mainWindowInitContentLoadedGrace
	}
	if started.IsZero() {
		return false
	}
	return now.Sub(started) < mainWindowInitGrace
}

// mainWindowInitPending informa se a criacao preguicosa da janela principal
// esta em curso — ou se o webview dela ainda esta carregando dentro da
// carencia pos-criacao. Enquanto estiver, a janela do chat NAO deve ser
// criada: espera o carregamento terminar e abre depois
// (ver scheduleSupportChatOpenAfterLazyLoad).
func (a *App) mainWindowInitPending() bool {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()
	return mainWindowInitPendingState(a.mainWindow != nil, a.windowFactory != nil, a.windowInitStarted, a.windowInitCompletedAt, time.Now(), a.mainWindowContentLoaded.Load())
}

// mainWindowReady informa se a janela principal ja existe. Usado pelo
// agendador do chat para distinguir "carregou" (abre o chat) de "carência
// expirou sem janela" (WebView2 travado de vez: desiste em vez de criar
// janela em cima de uma main thread congelada).
func (a *App) mainWindowReady() bool {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()
	return a.mainWindow != nil
}

// MainWindowContentReady e o binding chamado pelo FRONTEND da janela principal
// (app-init.js, fim do bootstrap) confirmando que o WebView2 dela carregou.
//
// E o sinal REAL que substitui a carencia por relogio: openSupportChatWindow
// so cria a janela do chat quando a janela principal esta pronta (ou depois da
// carencia de mainWindowInitContentLoadedGrace). Antes, a carencia de 8 s
// expirava antes do WebView2 (~54 s nesta maquina) e a criacao da janela do
// chat sobre a main thread ocupada CONGELAVA a interface inteira — o incidente
// de 20/10 (20:37: aceito o convite, a UI trava e o chat nunca abre).
//
// Seguranca de contexto: so a janela principal carrega app-init.js (a janela do
// chat usa chat-window.html + app-support-chat.js), entao uma chamada vinda da
// janela do chat nao existe. Um sinal tardio de um frontend antigo e inocuo:
// quando nao ha janela nova nascendo, o flag true e o estado correto.
func (a *App) MainWindowContentReady() {
	if a == nil {
		return
	}
	if a.mainWindowContentLoaded.Swap(true) {
		return
	}
	a.Logs.Append("[window] janela principal com webview carregado (sinal do frontend)")
}

// ── Aquecimento do WebView2 fora do caminho do convite (fix 21/10) ──────────
//
// INCIDENTE 21/10 (convite no tray): o prewarm criava a janela do chat OCULTA
// com a janela principal NUNCA criada (o gate mainWindowInitPending via "nunca
// começou" como livre) e a janela do chat virava o PRIMEIRO ambiente WebView2
// do processo, nascendo na main thread no meio do convite. A main thread parou
// de responder ao IPC por ~96 s (dois nascimentos serializados: o do chat do
// prewarm e o da janela principal pedida pelo clique no tray logo depois) e o
// ui-watchdog do serviço matou a UI; o convite expirou no servidor e o clique
// em "Abrir chat" virou no-op.
//
// REGRA NOVA: o nascimento FRIO do WebView2 nunca acontece no fluxo do convite.
// No tray, quem paga o custo frio é a JANELA PRINCIPAL, nascida oculta em
// segundo plano (fora do caminho do aceite e sem violar a regra "a janela do
// chat nunca é criada em status waiting"). Com o ambiente quente, a janela do
// chat nasce em attach rápido (medido: ~2 s a quente) e o aceite vira
// instantâneo.

// markMainWindowInitStarted marca o início da criação da janela principal
// (idempotente). O aquecimento marca SINCRONAMENTE para que os agendadores
// (scheduleSupportChatOpenAfterLazyLoad) enxerguem a criação em curso já na
// primeira verificação, sem depender da goroutine ganhar CPU.
func (a *App) markMainWindowInitStarted() {
	a.windowMu.Lock()
	if a.windowInitStarted.IsZero() {
		a.windowInitStarted = time.Now()
	}
	a.windowMu.Unlock()
}

// startMainWindowWarm nasce a janela principal oculta em segundo plano quando o
// WebView2 do processo ainda está frio (tray sem janela). Idempotente e seguro
// para chamadores concorrentes (convite, aceite, reconciliação).
// startMainWindowWarm nasce a janela principal oculta em segundo plano quando o
// WebView2 do processo ainda está frio (tray sem janela). Idempotente e seguro
// para chamadores concorrentes (convite, aceite, reconciliação). Devolve true
// só quando ESTE pedido começou o aquecimento — chamadores em loop
// (reconciliação, resume) usam o retorno para não spammar o log a cada passagem.
func (a *App) startMainWindowWarm() bool {
	if a == nil || a.app == nil {
		return false
	}
	if a.mainWindowReady() {
		return false // ambiente já existe — nada para aquecer
	}
	if !a.mainWindowWarmRequested.CompareAndSwap(false, true) {
		return false // aquecimento já em curso
	}
	a.markMainWindowInitStarted()
	go func() {
		defer a.mainWindowWarmRequested.Store(false)
		a.warmMainWindowHidden()
	}()
	return true
}

// warmMainWindowHidden cria a janela principal oculta (uma única vez). A
// goroutine CHAMADORA é quem bloqueia no nascimento do WebView2 — a main thread
// só executa o que a própria criação precisa e o restante da interface segue
// respondendo (cartão do convite é nativo, em thread própria; o aceite vai ao
// servidor por goroutine).
func (a *App) warmMainWindowHidden() {
	if a == nil || a.app == nil {
		return
	}
	a.windowMu.Lock()
	already := a.mainWindow != nil
	a.windowMu.Unlock()
	if already {
		return
	}
	a.windowCreateHidden.Store(true)
	log.Println("[window] WebView2 frio (tray): nascendo a janela principal oculta em segundo plano para aquecer o ambiente fora do fluxo do convite")
	if _, err := a.ensureMainWindow(); err != nil {
		// Sem o nascimento, o pedido de oculto não pode sobrar para uma criação
		// futura do usuário (a janela nasceria invisível).
		a.windowCreateHidden.Store(false)
		log.Println("[window] aquecimento da janela principal falhou: " + err.Error())
	}
}

// ConsumeMainWindowCreateHidden lê e ZERA o pedido de criação oculta. A fábrica
// (main.go, outro pacote) chama na hora de construir a janela: o aquecimento
// pede oculta; pedidos diretos do usuário (tray/toast/segunda instância) não
// marcam nada — e se chegarem primeiro, a janela nasce visível normalmente.
//
//wails:ignore
func (a *App) ConsumeMainWindowCreateHidden() bool {
	if a == nil {
		return false
	}
	return a.windowCreateHidden.Swap(false)
}
