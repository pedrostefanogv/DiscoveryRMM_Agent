package app

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"discovery/app/core/autoupdate"
	"discovery/app/core/logstore"
	"discovery/app/core/mcp"
	"discovery/app/core/platform"
	"discovery/app/core/screenshot"
	"discovery/app/coreagent"
	"discovery/app/debughttp"
	"discovery/app/services/chat"
	"discovery/app/services/psadt"
	"discovery/app/support"
	"discovery/app/supportchat"
)

var Version = "dev"

const (
	catalogURL       = "https://raw.githubusercontent.com/pedrostefanogv/winget-package-explo/refs/heads/main/public/data/packages.json"
	catalogTimeout   = 10 * time.Minute
	wingetTimeout    = 5 * time.Minute
	inventoryTimeout = 45 * time.Second
	printerTimeout   = 30 * time.Second
	chatConfigFile   = "chat_config.json"

	efficiencyModeEnabled = false

	WindowWidth     = 1280
	WindowHeight    = 860
	WindowMinWidth  = 980
	WindowMinHeight = 700
)

func GetDataDir() string {
	return platform.DataDir()
}

// WebView2UserDataPath retorna o diretório de dados do WebView2 da UI.
// Fixo em %ProgramData%\Discovery\WebView2 (Windows) para evitar o erro
// "Microsoft Edge WebView2 não pode ler e gravar o diretório de dados":
// o default do Wails (%APPDATA%\<exe>\EBWebView) quebra quando a UI roda
// elevada/SYSTEM (Task Scheduler, restart pós-update), pois %APPDATA%
// resolve para o systemprofile. ProgramData é gravável em qualquer contexto.
//
// O perfil é separado POR SESSÃO (WebView2\session-<id>): com a UI sem elevação
// (A2) duas sessões (console + RDP) podem rodar a UI ao mesmo tempo, e o
// Chromium embutido não aceita dois processos no mesmo diretório de perfil.
// Sem sessão (serviço/sessão 0/0xFFFFFFFF = "sem sessão") mantém o caminho base.
func WebView2UserDataPath() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	return webView2SessionDir(platform.UIWebView2Root(), currentProcessSessionID())
}

// webView2SessionDir resolve o User Data Folder do WebView2 para uma sessão.
// Sessão 0 (serviços) e 0xFFFFFFFF (WTS "sem sessão") NÃO podem virar
// "session-0"/"session-4294967295": o primeiro colidiria com o perfil base e o
// segundo criaria um diretório inútil com permissões herdadas de ProgramData.
// Função pura para ser testável sem depender da sessão real do processo.
func webView2SessionDir(base string, session uint32) string {
	if session == 0 || session == 0xFFFFFFFF {
		return base
	}
	return filepath.Join(base, fmt.Sprintf("session-%d", session))
}

type App struct {
	// coreagent.CoreAgent embutido (migração física lote 1 — Fase A):
	// serviços de domínio/infra do core com tipos em pacotes externos.
	// A promoção de campos mantém `a.AgentConn`, `a.NotificationSvc`, etc.
	// funcionando — refactor mecânico sem mudança de comportamento. O acesso
	// explícito ao embed é `a.CoreAgent`; `Core` é um alias curto para o
	// mesmo struct (usado onde um método/promoted field colide, ex. DB()).
	// Alias declarado após o struct: ver func (a *App) core() abaixo — não,
	// Go não tem alias de embed; o acesso é via a.CoreAgent.<Campo>.
	coreagent.CoreAgent

	ctx                  context.Context
	cancel               context.CancelFunc
	packageManagerRouter *automationPackageManagerRouter

	mcpRegistry *mcp.Registry
	chatSvc     *chat.Service
	psadtSvc    *psadt.Service

	// ── Chat de suporte vinculado ao chamado ──
	// Estado 100% em memória (sem persistência no agent) + a janela dedicada,
	// independente da interface principal.
	supportChatSvc *supportchat.Service
	supportChatMu  sync.Mutex
	supportChatWin uiWindow
	// supportChatAllowClose libera o fechamento programático da janela depois
	// da confirmação do usuário (o hook de WindowClosing cancela o resto).
	//
	// É POR JANELA (um portão novo a cada janela criada): um flag global ficava
	// "true" para sempre depois do primeiro fechamento programático, e a janela
	// seguinte passava a fechar no X sem o modal — sem encerrar a sessão pelo
	// caminho síncrono do Go (o end virava best-effort no beforeunload e podia
	// não chegar, deixando a sessão viva para a reconciliação reabrir a janela).
	supportChatAllowClose *atomic.Bool
	// supportChatReconcileTicks conta os ticks da reconciliação periódica: com
	// conversa ATIVA e janela aberta (NATS entrega em tempo real + reconexão força
	// refresh) o REST só é consultado de vez em quando — ver
	// reconcileSupportChat em support_chat_window.go.
	supportChatReconcileTicks atomic.Int64
	// supportChatResumeRequested deduplica o "resumo após abertura negada" de
	// openSupportChatWindow: várias tentativas de abertura no mesmo instante
	// pediam vários resumos (e vários GET /active) para o mesmo evento.
	supportChatResumeRequested atomic.Bool
	// supportChatLazyOpenRequested deduplica a reabertura do chat após o lazy
	// load da janela principal: enquanto a janela principal (WebView2) está
	// nascendo, criar a janela do chat junto pode congelar o processo inteiro
	// (main thread presa na criação). O agendador único espera o carregamento
	// terminar — ou abre mesmo assim depois da carência; ver
	// scheduleSupportChatOpenAfterLazyLoad em support_chat_window.go.
	supportChatLazyOpenRequested atomic.Bool
	// supportChatWindowContentReady: o frontend da JANELA DO CHAT já montou o
	// DOM. Usado pela blindagem de navegação (ensureSupportChatWindowContent):
	// sem esse sinal no prazo, a janela é recarregada/renavegada.
	supportChatWindowContentReady atomic.Bool
	// supportChatWinPaints: a janela corrente já PINTOU (sinal do frontend em
	// SupportChatWindowPainted). Portão do REUSO no aceite: só vale reaproveitar
	// uma janela que o usuário já está vendo — reusar uma janela branca daria
	// "abriu o chat e não tem nada".
	supportChatWinPaints atomic.Bool
	// supportChatLastPlacement: posição da última janela do chat (a próxima
	// nasce nela). Protegida por supportChatMu.
	supportChatLastPlacement *supportChatPlacementRecord
	// supportChatLink: estado REAL do canal (NATS do SERVIÇO, não o da UI).
	// A janela deixou de inferir "conectado" a partir do próprio processo.
	supportChatLink supportChatLinkState
	// supportChatTraceMu/supportChatOpenTrace: medida do caminho
	// "aceite -> janela visível" (uma linha de log por abertura).
	supportChatTraceMu   sync.Mutex
	supportChatOpenTrace *supportChatOpenTrace
	// Dedupe do verify em background por sessionId: event/JS/IPC/reentrega podem
	// pedir a mesma validação várias vezes; cada verify faz até 20 GETs.
	supportChatVerifyMu  sync.Mutex
	supportChatVerifying map[string]bool
	// Cartões nativos do convite de chat (um por sessão; no máximo um visível).
	// O cancelador fecha o cartão sem decisão quando o convite é respondido por
	// outro caminho, substituído ou expira no servidor.
	supportChatNotifyMu sync.Mutex
	supportChatNotify   map[string]func()
	// supportChatNotifyShowMu serializa a abertura do cartão (evento IPC + JS +
	// reentrega podem concorrer).
	supportChatNotifyShowMu sync.Mutex
	// supportChatNotifyAnswered evita reabrir o cartão enquanto a resposta do
	// usuário ainda está indo ao servidor (estado local ainda "waiting").
	supportChatNotifyAnswered map[string]time.Time
	// supportChatNotifyShown conta quantas vezes o cartão de cada sessão já foi
	// apresentado. Limita a UMA reapresentação por sessão: o ciclo "fecha pela
	// rede de segurança → estado waiting sem cartão → reapresenta" repetia o
	// cartão 4 vezes num teste real, e cada volta perdia a decisão do usuário.
	supportChatNotifyShown map[string]int
	// supportChatNotifyGuardClosed guarda quando a rede de segurança fechou o
	// cartão sem decisão do usuário: nesse estado o "waiting" que sobrou NÃO é
	// um pedido que o usuário esteja vendo e não pode ser reapresentado de novo.
	supportChatNotifyGuardClosed map[string]time.Time
	// supportChatNotifyReinvited registra quando o fallback "estado waiting sem
	// cartão" reapresentou o convite por sessão: gatilhos concorrentes (adoção
	// de estado + evento IPC) logavam e reapresentavam no mesmo milissegundo
	// (logs de 2026-10-20 18:33:44.366, duas linhas idênticas).
	supportChatNotifyReinvited map[string]time.Time
	// Cache da política de anexos do chat (allowlist/tamanho vindos do servidor).
	supportChatPolicyMu      sync.Mutex
	supportChatPolicy        supportchat.AttachmentPolicy
	supportChatPolicyExpires time.Time
	supportChatPolicyReady   bool
	// Cache da política de anexos do CHAT IA (GET /me/ai-chat/attachment-settings),
	// compartilhada entre a validação no Go e o binding da UI.
	aiChatPolicyMu      sync.Mutex
	aiChatPolicy        support.AIAttachmentSettings
	aiChatPolicyExpires time.Time
	aiChatPolicyReady   bool
	// logStore é o banco unificado de logs do processo (nil quando indisponível).
	logStore *logstore.Store
	// commandDedupe é o dedupe persistido de comandos (sobrevive ao reboot no
	// meio de um restart/shutdown — ver command_dedupe_store.go).
	commandDedupe *commandDedupeStore

	// ── Captura de tela assistida (chat IA) ──
	// screenshotConsent controla a autorização do usuário; screenshotMu/
	// screenshotOverlay guardam a sessão ativa do overlay de seleção.
	screenshotMu      sync.Mutex
	screenshotConsent *screenshot.ConsentManager
	screenshotOverlay *screenshotOverlaySession
	// screenshotOpening/screenshotOpenDone/screenshotOpenErr serializam a
	// ABERTURA do overlay: enquanto uma captura está sendo preparada (hide +
	// freeze + encode, ~1 s), os pedidos que chegam esperam a MESMA sessão em
	// vez de dispararem outra captura e outra sessão. Sem isto, N cliques
	// concorrentes no botão de print do chat de suporte criavam N sessões, os
	// `restore` das perdedoras eram descartados e o Finish do front chegava com
	// um id que o backend já não tinha ("sessao de captura expirada ou invalida").
	screenshotOpening  bool
	screenshotOpenDone chan struct{}
	screenshotOpenErr  error
	// screenshotOpenAbort é marcado quando a janela é fechada/ocultada DURANTE a
	// abertura (a sessão ainda não foi publicada): o build libera a janela e
	// devolve erro em vez de publicar uma sessão órfã — a próxima captura
	// devolveria o frame congelado antigo.
	screenshotOpenAbort bool
	// screenshotRestore devolve a janela principal aos bounds normais quando a
	// sessão some sem release (Finish/cancel com id inválido): sem isso ela
	// ficava frameless e sempre-no-topo cobrindo todo o desktop — a "tela toda
	// preta" relatada, que só voltava fechando e reabrindo a janela pelo tray.
	screenshotRestore func()
	// screenshotOverlayActive evita que o clamp de janela (FitWindowToWorkArea)
	// desfaça o overlay em tela cheia durante a seleção.
	screenshotOverlayActive atomic.Bool
	// mainWindowFrameless guarda o modo da janela principal na inicialização.
	// O overlay NÃO mexe mais nela (ver screenshot_overlay_window.go).
	mainWindowFrameless bool
	// ── Janela DEDICADA do overlay de captura ──
	// O overlay de seleção não roda mais na janela principal: quando o pedido
	// vem da janela do chat de suporte a janela principal costuma estar no tray,
	// e uma janela escondida tem o controller do WebView2 oculto pelo Wails — o
	// SetWindowPos cru a exibia como um retângulo preto cobrindo o desktop, sem
	// UI nenhuma ("a tela fica escura na captura"). A janela dedicada é criada
	// sob demanda e exibida pelo caminho normal do Wails (Show → WebView visível).
	screenshotOverlayWinMu sync.Mutex
	screenshotOverlayWin   uiWindow
	// screenshotOverlayReady é fechado quando o frontend da janela do overlay
	// reporta que carregou (binding ScreenshotOverlayReady): o build espera por
	// ele antes de congelar a tela, para a janela nunca aparecer vazia.
	screenshotOverlayReadyFlag bool
	screenshotOverlayReady     chan struct{}
	// screenshotOverlayDeliver entrega o payload da tela congelada à janela do
	// overlay (consumido por ScreenshotOverlayNext). Capacidade 1: um payload
	// não consumido é descartado na entrega seguinte.
	screenshotOverlayDeliver chan string
	// Buffer das últimas capturas cheias (lightbox do chat): memória apenas.
	screenshotImagesMu   sync.Mutex
	screenshotImages     map[int64]screenshotStoredImage
	screenshotImageOrder []int64
	screenshotPolicy     screenshot.Policy
	screenshotLimiter    *screenshot.CaptureLimiter

	// toolsRegistration guarda o timestamp do último registro bem-sucedido de tools.
	// Usado para re-registrar se o cache do servidor expirou (TTL 5min por padrão no servidor).
	toolsRegistrationMu   sync.RWMutex
	lastToolsRegistration time.Time

	debugHTTP        *debughttp.Server
	chatSSE          *debughttp.Server
	chatEvents       *debughttp.ChatEventBroker
	debugAuthTokenMu sync.Mutex
	debugAuthToken   string

	closeMu                  sync.RWMutex
	allowClose               bool
	trayReady                atomic.Bool
	trayIconState            atomic.Int32
	trayIcon                 []byte
	trayProvisioning         []byte
	trayOffline              []byte
	activeRemoteSessions     atomic.Int32
	zeroTouchAttemptInFlight atomic.Bool
	zeroTouchApprovalPending atomic.Bool

	// serverAuth: estado de "provisionado, mas o servidor ainda não liberou o
	// acesso" (403 Agent registration is pending). É a fonte da verdade que a
	// overlay de onboarding e as telas de conteúdo (Loja/Suporte/KB) consultam
	// para mostrar a tela de espera em vez do erro cru do backend. Ver
	// server_authorization.go.
	serverAuth serverAuthTracker

	// Companion mode: último estado de onboarding recebido do serviço via
	// snapshot IPC (agent:status_snapshot). O sync (zero-touch) roda no
	// SERVIÇO — a flag zeroTouchApprovalPending local da UI nunca muda no
	// modo companion, e GetOnboardingStatus responde 'normal' mesmo quando o
	// agente está provisionado aguardando aprovação. Com o snapshot, a UI
	// reflete o estado real (e a overlay sai quando a aprovação chega).
	companionOnboardingMu sync.RWMutex
	companionOnboarding   map[string]interface{}

	startupTime time.Time

	// deferredRestart fica no App (não core): estado do power-command de
	// restart adiado — usa campos minúsculos intensivamente e é acionado
	// pela sessão de UI (powerCommandPayload). Revisão da migração lote 2.
	deferredRestart *deferredRestartState

	// silentPowerMu/silentPowerTimer controlam o timer interno do comando de
	// power silencioso (notifyUser=false): o atraso é contado pelo agente (sem
	// diálogo nativo do Windows) e pode ser cancelado/reagendado por um novo
	// comando de power.
	silentPowerMu    sync.Mutex
	silentPowerTimer *time.Timer

	// M4: fases de startup em andamento — o shutdown loga quantas ficaram
	// pendentes ao fechar o SQLite após o timeout.
	startupPhasePending atomic.Int32

	// AutoUpdateSvc (F2): auto-update da Loja no AGENTE. Vive no App (não no
	// CoreAgent) porque consome serviços de camadas mistas (AppStoreSvc,
	// UpdatesSvc, InventorySvc, AutomationSvc) e roda SÓ em ServiceMode — na
	// UI companion quem executa é o serviço.
	AutoUpdateSvc *autoupdate.Service

	// idleRelease controla a liberação de caches/memória após um período
	// CONTÍNUO de ociosidade (IdleReleaseDelay). Não roda a cada micro-ociosidade:
	// um timer é agendado ao entrar em idle e cancelado na primeira atividade.
	idleReleaseMu    sync.Mutex
	idleReleaseTimer *time.Timer

	// ── Wails v3 ──
	// Referências explícitas à aplicação/janela do Wails v3.
	// Substituem o acesso implícito via ctx do v2.
	app        uiApp
	mainWindow uiWindow
	systemTray uiSystemTray

	// windowMu serializa a criação preguiçosa da janela principal
	// (EnsureMainWindow): tray, segunda instância e clique em toast podem pedir a
	// janela quase ao mesmo tempo. Ver window_lazy.go.
	windowMu      sync.Mutex
	windowFactory func() uiWindow
	// windowInitStarted marca o INÍCIO da primeira criação preguiçosa da janela
	// principal. Enquanto a criação não termina (e por uma carência curta
	// depois), a abertura da janela do chat é adiada — criar outra janela com
	// WebView2 em cima de uma main thread ocupada é o travamento relatado.
	windowInitStarted time.Time
	// windowInitCompletedAt marca quando a janela principal FOI criada. O
	// WebView2 continua carregando DEPOIS disso (o travamento observado começa
	// ~3 s após a criação), então a carência pos-criação usa esta marca.
	windowInitCompletedAt time.Time
	// supportChatPrewarmRequested deduplica a pré-criação oculta da janela do
	// chat (prewarmSupportChatWindow): reapresentações do convite e reentregas
	// disparam o prewarm várias vezes no mesmo instante.
	supportChatPrewarmRequested atomic.Bool
	// ── Aquecimento do WebView2 fora do caminho do convite (fix 21/10) ──
	// windowCreating garante UMA criação da janela principal por vez SEM prender
	// windowMu durante o nascimento do WebView2: a factory bloqueia a goroutine
	// chamadora até o WebView2 nascer na main thread — segurar o mutex nesse
	// intervalo deixaria chamadores que rodam na PRÓPRIA main thread presos no
	// mutex, justamente quando a main thread é quem precisa executar a criação.
	// Ver window_lazy.go.
	windowCreating atomic.Bool
	// windowCreateHidden: pedido de criação da janela principal OCULTA
	// (aquecimento em segundo plano no tray, warmMainWindowHidden). A fábrica
	// (main.go) consome esta marca na hora de construir a janela.
	windowCreateHidden atomic.Bool
	// mainWindowWarmRequested deduplica os pedidos de aquecimento: convite
	// reapresentado, aceite e reconciliação podem pedir quase ao mesmo tempo.
	mainWindowWarmRequested atomic.Bool
	// supportChatTabShown: a conversa de suporte está EXIBIDA na aba da janela
	// principal (substituto de "janela do chat aberta" no modo embutido — a
	// reconciliação usa para não reexibir/elevar a janela a cada ciclo).
	supportChatTabShown atomic.Bool

	// mainWindowContentLoaded: o FRONTEND da janela principal confirmou que o
	// WebView2 carregou (binding MainWindowContentReady, chamado no bootstrap do
	// app-init.js). É o sinal REAL de prontidão: a carência por relógio expirava
	// antes de o WebView2 desta máquina terminar de nascer (~54 s medidos) e a
	// janela do chat era criada sobre a main thread ocupada — o congelamento
	// completo da interface de 20/10 (20:37). Ver window_lazy.go.
	mainWindowContentLoaded atomic.Bool

	// ── Notificação nativa da resposta do chat (Wails v3 notifications) ──
	// nativeNotifications é o serviço nativo do Wails v3 (registrado em
	// main.go); chatTabActive é reportado pelo frontend (SetChatTabActive) e a
	// decisão final combina esse estado com foco/visibilidade reais da janela.
	nativeNotifications uiNotifications
	// nativeNotificationsStarted: o ServiceStartup do serviço nativo concluiu
	// com sucesso (sem isso o notifier tem AppID/CLSID vazios).
	nativeNotificationsStarted atomic.Bool
	// notificationIconDir sobrescreve o diretório onde o PNG do logo do toast é
	// materializado (vazio = diretório de dados do agente). Usado pelos testes.
	notificationIconDir string
	// Fan-out do callback único de OnNotificationResponse do serviço nativo.
	nativeNotifyHandlersMu sync.Mutex
	nativeNotifyHandlers   []func(uiNotificationResult)
	chatTabActive          atomic.Bool
	// chatNotifyPending: já avisamos nesta ausência do usuário; lastAt é o
	// debounce mínimo entre envios.
	chatNotifyMu      sync.Mutex
	chatNotifyPending bool
	chatNotifyLastAt  time.Time
	// uiReady: o frontend já reportou estado (bindings carregados).
	// pendingChatFocus: a abertura veio de clique em toast com o app fechado.
	uiReady          atomic.Bool
	pendingChatFocus atomic.Bool

	// ── IPC serviço ↔ UI (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 2) ──
	// No serviço: ipcServer distribui eventos para as UIs conectadas.
	// Na UI: ipcClient conecta ao serviço (modo companion) com reconexão.
	ipcServer *IPCServer
	ipcClient *IPCClient

	// uiWatchdog relança a UI companion quando ela desaparece (só no modo
	// serviço). Fica em atomic.Pointer porque o loop do watchdog e o handler do
	// convite de chat (goroutines distintas) podem tocá-lo ao mesmo tempo.
	// Ver ui_watchdog.go.
	uiWatchdog atomic.Pointer[uiWatchdog]

	// Último snapshot de conectividade recebido do serviço via IPC
	// (agente:status_snapshot). Em companion mode o agentConn local não roda,
	// então o tray/status usam este snapshot para refletir o estado real do
	// core que roda no serviço.
	companionStatusMu sync.Mutex
	companionStatus   *AgentStatus

	// Estado do CANAL DE GRAVAÇÃO reportado pelo serviço (P1.4 do
	// PLANO_CORRECAO_CANAL_ADMIN_IPC.md): adminOk/adminPipe/writesBlocked.
	// Sem ele a UI em modo somente-leitura era invisível — o usuário só
	// descobria quando a ação de gravação falhava, e o chat culpava o servidor.
	companionIPCMu         sync.RWMutex
	companionIPCCapability map[string]interface{}

	// Itens de status do menu do tray (atualizados dinamicamente).
	trayStatusHostname   uiMenuItem
	trayStatusVersion    uiMenuItem
	trayStatusConnection uiMenuItem
}

func (a *App) GetRuntimeFlags() RuntimeFlags {
	return RuntimeFlags{DebugMode: a.RuntimeFlags.DebugMode, StartMinimized: a.RuntimeFlags.StartMinimized, ServiceMode: a.RuntimeFlags.ServiceMode}
}

func (a *App) SetContext(ctx context.Context) {
	if a.cancel != nil {
		a.cancel()
	}
	ctx, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel
}

func (a *App) Ctx() context.Context { return a.ctx }

func (a *App) GetServiceHealth() map[string]interface{} {
	return map[string]interface{}{
		"running":      true,
		"user_message": "Runtime local ativo (tray icon no logon).",
	}
}
