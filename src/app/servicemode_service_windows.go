package app

// Serviço Windows do Discovery Agent (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 1).
//
// Implementa svc.Handler: roda o core do agent (agentConn/NATS, inventory,
// automation, sync, P2P, self-update) como serviço LocalSystem — sem Wails,
// sem tray, sem janela, sem debug HTTP de UI.
//
// Estratégia conservadora (revisão 2026-09-04 do plano): manter "App" intacto
// e usar runtimeFlags.ServiceMode para pular os pontos acoplados à UI dentro
// de startup()/shutdown(). O pipeline de core é idêntico ao standalone.

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/svc"

	"discovery/app/core/logger"
	"discovery/app/core/platform"
)

// ServiceName é o nome do serviço Windows registrado pelo NSIS.
const ServiceName = "DiscoveryAgent"

const (
	// defaultShutdownGraceSeconds é o tempo (s) que o serviço segura o
	// encerramento após um SERVICE_CONTROL_PRESHUTDOWN/SHUTDOWN, mantendo o
	// desktop e o acesso remoto vivos durante o reinício/desligamento nativo
	// do Windows.
	defaultShutdownGraceSeconds = 30
	// maxShutdownGraceSeconds limita o valor aceito por
	// DISCOVERY_SHUTDOWN_GRACE_SECONDS.
	maxShutdownGraceSeconds = 120
	// legacyShutdownGraceCap limita o grace quando o PRESHUTDOWN não é
	// entregue (fallback via SERVICE_CONTROL_SHUTDOWN): o Windows só concede
	// WaitToKillServiceTimeout (~5s) nesse caminho, então não adianta segurar
	// mais que isso.
	legacyShutdownGraceCap = 4 * time.Second
)

// shutdownGraceSeconds resolve o grace de encerramento:
// DISCOVERY_SHUTDOWN_GRACE_SECONDS (0..120). 0 = comportamento legado
// (não aceita PRESHUTDOWN / encerra imediatamente).
func shutdownGraceSeconds() int {
	raw := strings.TrimSpace(os.Getenv("DISCOVERY_SHUTDOWN_GRACE_SECONDS"))
	if raw == "" {
		return defaultShutdownGraceSeconds
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("[service] DISCOVERY_SHUTDOWN_GRACE_SECONDS inválido (%q) — usando %ds", raw, defaultShutdownGraceSeconds)
		return defaultShutdownGraceSeconds
	}
	if n < 0 {
		n = 0
	}
	if n > maxShutdownGraceSeconds {
		log.Printf("[service] DISCOVERY_SHUTDOWN_GRACE_SECONDS=%d acima do teto — limitando a %ds", n, maxShutdownGraceSeconds)
		n = maxShutdownGraceSeconds
	}
	return n
}

// serviceControlAction é a decisão pura tomada a partir de um comando do SCM.
type serviceControlAction int

const (
	actionInterrogate serviceControlAction = iota
	actionTeardownNow
	actionHoldThenTeardown
)

// resolveServiceControl decide o que fazer com um comando do SCM. Função pura
// (testável): Stop (sc stop / update / uninstall) encerra imediatamente;
// PreShutdown/Shutdown seguram o grace quando configurado.
func resolveServiceControl(cmd svc.Cmd, graceSeconds int) serviceControlAction {
	switch cmd {
	case svc.Stop:
		return actionTeardownNow
	case svc.PreShutdown, svc.Shutdown:
		if graceSeconds > 0 {
			return actionHoldThenTeardown
		}
		return actionTeardownNow
	default:
		return actionInterrogate
	}
}

// graceDurationFor calcula o tempo de espera efetivo por comando. O caminho
// legado (Shutdown sem PRESHUTDOWN) é limitado ao cap do Windows.
func graceDurationFor(cmd svc.Cmd, graceSeconds int) time.Duration {
	d := time.Duration(graceSeconds) * time.Second
	if cmd == svc.Shutdown && d > legacyShutdownGraceCap {
		d = legacyShutdownGraceCap
	}
	return d
}

// discoveryService implementa svc.Handler para o core do agent.
type discoveryService struct {
	app *App
	// teardownOnce garante um único app.ServiceShutdown() mesmo quando
	// PreShutdown e Shutdown chegam em sequência.
	teardownOnce sync.Once
}

// RunServiceMode é o entrypoint do modo serviço chamado por main.go.
// Detecta se o processo foi lançado pelo SCM (svc.IsWindowsService) ou com
// --service manual; nunca inicializa a aplicação Wails.
func RunServiceMode() {
	// Log do serviço em arquivo dedicado (Fase 0.2) — antes de qualquer
	// operação, para capturar falhas de início.
	if p := platform.ServiceLogFilePath(); p != "" {
		if err := logger.SetFileOutput(p); err != nil {
			log.Printf("[service] aviso: falha ao redirecionar log para %s: %v", p, err)
		}
	}
	// Tee do stdlib log para o arquivo do serviço (mesmo caminho da UI, que
	// já faz isso em src/main.go): linhas de log.Printf — dreno do stderr do
	// worker de remote session, logs do manager/spawn — antes iam só para o
	// stderr do serviço (invisível em produção) e o diagnóstico do acesso
	// remoto ficava impossível no agent-service.log.
	logger.RedirectStdLog(logger.LevelInfo)
	log.Printf("[service] modo serviço iniciado (args=%v)", os.Args)

	grace := shutdownGraceSeconds()
	log.Printf("[service] grace de encerramento: %ds (fonte=DISCOVERY_SHUTDOWN_GRACE_SECONDS)", grace)

	// Auto-configura o serviço no SCM: PRESHUTDOWN timeout proporcional ao
	// grace + start= auto. Best-effort: falha não impede o serviço de subir.
	if err := platform.ConfigureServiceLifecycle(ServiceName, platform.PreshutdownTimeoutFor(grace)); err != nil {
		log.Printf("[service] aviso: falha ao configurar ciclo de vida do serviço: %v", err)
	}

	inService, err := svc.IsWindowsService()
	if err != nil {
		log.Printf("[service] falha ao detectar contexto SCM: %v — executando direto", err)
		runServiceCore()
		return
	}
	if !inService {
		// --service manual fora do SCM: roda o core direto (útil para debug).
		log.Printf("[service] executando fora do SCM — rodando core direto (debug)")
		runServiceCore()
		return
	}

	if err := svc.Run(ServiceName, &discoveryService{}); err != nil {
		log.Printf("[service] svc.Run falhou: %v", err)
	}
}

// IsWindowsServiceProcess reporta se o processo atual foi lançado pelo SCM
// (usado por main.go para auto-detecção sem --service explícito).
func IsWindowsServiceProcess() bool {
	inService, err := svc.IsWindowsService()
	if err != nil {
		return false
	}
	return inService
}

// Execute é o callback do SCM. Aceita Start/Stop/Shutdown/PreShutdown/Interrogate.
//
// Diferença desde o fix de shutdown: PreShutdown/Shutdown NÃO encerram o core
// na hora. O serviço reporta StopPending, mantém agentConn/NATS/remote debug e
// o worker de captura vivos pelo grace configurado, e só então executa o
// teardown. Isso mantém o acesso remoto utilizável durante o reinício/
// desligamento nativo do Windows. Stop (sc stop/update/uninstall) continua
// encerrando imediatamente.
func (s *discoveryService) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	// grace=0 → comportamento legado: NÃO aceita PRESHUTDOWN (o SCM não
	// manda a notificação antecipada de shutdown).
	graceSeconds := shutdownGraceSeconds()
	cmdsAccepted := svc.AcceptStop | svc.AcceptShutdown
	if graceSeconds > 0 {
		cmdsAccepted |= svc.AcceptPreShutdown
	}

	changes <- svc.Status{State: svc.StartPending}
	log.Printf("[service] SCM Start recebido")

	// Cria a App sem opções de UI (tray/janela). O runtimeFlags.ServiceMode
	// faz startup() pular tudo acoplado à sessão do usuário.
	app := NewApp(AppStartupOptions{ServiceMode: true})
	s.app = app

	// Roda o core em goroutine; o ctx interno da App controla o ciclo.
	go func() {
		_ = app.RunCore(context.Background())
	}()

	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}
	log.Printf("[service] core do agent em execução (SYSTEM)")

	var (
		graceC      <-chan time.Time
		graceTimer  *time.Timer
		stopC       <-chan time.Time
		stopTicker  *time.Ticker
		checkPoint  uint32
		stopPending bool
	)
	defer func() {
		if graceTimer != nil {
			graceTimer.Stop()
		}
		if stopTicker != nil {
			stopTicker.Stop()
		}
	}()

	// teardown executa o encerramento do core exatamente uma vez.
	teardown := func(reason string) {
		s.teardownOnce.Do(func() {
			log.Printf("[service] %s — encerrando core", reason)
			app.ServiceShutdown()
		})
	}
	// stopAndReturn reporta STOPPED e finaliza o handler.
	stopAndReturn := func(reason string) (bool, uint32) {
		teardown(reason)
		changes <- svc.Status{State: svc.Stopped, Accepts: cmdsAccepted}
		// Exit code 0: encerramento limpo — NÃO conta como crash para
		// as failure actions do SCM (restart/5000 só dispara em código
		// não-zero).
		return false, 0
	}

	for {
		select {
		case c, ok := <-r:
			if !ok {
				return stopAndReturn("canal de controle do SCM fechado")
			}
			switch resolveServiceControl(c.Cmd, graceSeconds) {
			case actionInterrogate:
				status := svc.Status{State: svc.Running, Accepts: cmdsAccepted}
				if stopPending {
					status.State = svc.StopPending
					status.CheckPoint = checkPoint
					status.WaitHint = uint32(graceSeconds * 1000)
				}
				changes <- status
				time.Sleep(100 * time.Millisecond)
				changes <- status
			case actionTeardownNow:
				log.Printf("[service] SCM %s recebido — encerrando imediatamente", svcCmdName(c.Cmd))
				changes <- svc.Status{State: svc.StopPending, Accepts: cmdsAccepted}
				return stopAndReturn("SCM " + svcCmdName(c.Cmd))
			case actionHoldThenTeardown:
				if stopPending {
					// Já em grace: mantém StopPending (o SCM não repete
					// shutdown, mas um Interrogate pode chegar).
					continue
				}
				stopPending = true
				d := graceDurationFor(c.Cmd, graceSeconds)
				log.Printf("[service] SCM %s recebido — grace de encerramento ativo por %s (remoto preservado)", svcCmdName(c.Cmd), d)
				changes <- svc.Status{State: svc.StopPending, Accepts: cmdsAccepted, CheckPoint: checkPoint, WaitHint: uint32(d.Milliseconds())}
				graceTimer = time.NewTimer(d)
				graceC = graceTimer.C
				// Heartbeat de status durante o grace: mantém o SCM informado de
				// que o serviço está em STOP_PENDING de propósito (não travado).
				stopTicker = time.NewTicker(5 * time.Second)
				stopC = stopTicker.C
			}
		case <-stopC:
			checkPoint++
			changes <- svc.Status{State: svc.StopPending, Accepts: cmdsAccepted, CheckPoint: checkPoint, WaitHint: uint32(graceSeconds * 1000)}
		case <-graceC:
			return stopAndReturn("grace expirado")
		}
	}
}

func svcCmdName(cmd svc.Cmd) string {
	switch cmd {
	case svc.Stop:
		return "Stop"
	case svc.Shutdown:
		return "Shutdown"
	case svc.PreShutdown:
		return "PreShutdown"
	case svc.Interrogate:
		return "Interrogate"
	default:
		return "unknown"
	}
}

// runServiceCore roda o core fora do SCM (debug/manual). Bloqueia até o
// processo ser encerrado (SIGINT/KILL).
func runServiceCore() {
	app := NewApp(AppStartupOptions{ServiceMode: true})
	_ = app.RunCore(context.Background())
	select {}
}
