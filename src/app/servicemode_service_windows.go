package app

// Serviço Windows do Discovery Agent (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 1).
//
// Implementa svc.Handler: roda o core do agent (agentConn/NATS, inventory,
// automation, sync, P2P, self-update) como serviço LocalSystem — sem Wails,
// sem tray, sem janela, sem debug HTTP de UI.
//
// Estratégia conservadora (revisão 2026-09-04 do plano): manter `App` intacto
// e usar runtimeFlags.ServiceMode para pular os pontos acoplados à UI dentro
// de startup()/shutdown(). O pipeline de core é idêntico ao standalone.

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows/svc"

	"discovery/app/core/logger"
	"discovery/app/core/platform"
)

// ServiceName é o nome do serviço Windows registrado pelo NSIS.
const ServiceName = "DiscoveryAgent"

// discoveryService implementa svc.Handler para o core do agent.
type discoveryService struct {
	app *App
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
	log.Printf("[service] modo serviço iniciado (args=%v)", os.Args)

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

// Execute é o callback do SCM. Aceita Start/Stop/Shutdown/Interrogate.
func (s *discoveryService) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}
	log.Printf("[service] SCM Start recebido")

	// Cria a App sem opções de UI (tray/janela). O runtimeFlags.ServiceMode
	// faz startup() pular tudo acoplado à sessão do usuário.
	app := NewApp(AppStartupOptions{ServiceMode: true})
	s.app = app

	// Roda o core em goroutine; o ctx interno da App controla o ciclo.
	go func() {
		app.ServiceStartup(context.Background(), application.ServiceOptions{})
	}()

	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}
	log.Printf("[service] core do agent em execução (SYSTEM)")

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
				time.Sleep(100 * time.Millisecond)
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				log.Printf("[service] SCM %s recebido — encerrando core", svcCmdName(c.Cmd))
				changes <- svc.Status{State: svc.StopPending, Accepts: cmdsAccepted}
				app.ServiceShutdown()
				changes <- svc.Status{State: svc.Stopped, Accepts: cmdsAccepted}
				// Exit code 0: encerramento limpo — NÃO conta como crash para
				// as failure actions do SCM (restart/5000 só dispara em código
				// não-zero).
				return false, 0
			default:
				log.Printf("[service] comando SCM não suportado ignorado: %d", c.Cmd)
			}
		}
	}
}

func svcCmdName(cmd svc.Cmd) string {
	switch cmd {
	case svc.Stop:
		return "Stop"
	case svc.Shutdown:
		return "Shutdown"
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
	app.ServiceStartup(context.Background(), application.ServiceOptions{})
	select {}
}
