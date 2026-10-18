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

// ── Máquina de estados do ciclo de controle do SCM (A.4.1) ──────────────────
//
// Execute() é um select sobre o canal do SCM. Para que a SEQUÊNCIA
// PRESHUTDOWN → grace → teardown seja testável sem SCM real, sem depender do
// Windows de verdade e sem App, ela vive em serviceControlFlow, atrás de
// injeções (relógio/timers, reporte de status, nota de shutdown do sistema e
// teardown do core). O comportamento não mudou — só o lugar onde a decisão
// mora, para permitir o teste de integração da sequência.
//
// Validação em VM (A.4.2) — NÃO coberta por teste automatizado, pendente:
//   - o SCM entrega SERVICE_CONTROL_PRESHUTDOWN ANTES do logoff/início do
//     desligamento (Windows 10/11, com Fast Startup ligado e desligado);
//   - com grace > 0 o acesso remoto continua utilizável durante o grace;
//   - `sc stop DiscoveryAgent` (SERVICE_CONTROL_STOP) retorna em < 5s, porque
//     Stop não passa pelo grace (vai direto para actionTeardownNow).
//
// O teste da sequência (sem SCM) está em servicemode_service_windows_test.go,
// nos casos TestServiceControlFlow_*.

// serviceControlHeartbeatInterval é o intervalo do status STOP_PENDING
// enviado durante o grace (prova ao SCM que o serviço não travou).
const serviceControlHeartbeatInterval = 5 * time.Second

// serviceControlTimer abstrai *time.Timer/*time.Ticker para que os testes
// possam disparar o deadline na mão, sem esperar tempo real.
type serviceControlTimer interface {
	Chan() <-chan time.Time
	Stop()
}

type goTimer struct{ t *time.Timer }

func (g goTimer) Chan() <-chan time.Time { return g.t.C }
func (g goTimer) Stop()                  { g.t.Stop() }

type goTicker struct{ t *time.Ticker }

func (g goTicker) Chan() <-chan time.Time { return g.t.C }
func (g goTicker) Stop()                  { g.t.Stop() }

// serviceControlClock cria os timers do fluxo e dorme (o SCM exige um
// intervalo mínimo entre os dois status enviados em resposta a um
// Interrogate). Injetável nos testes.
type serviceControlClock struct {
	NewTimer  func(time.Duration) serviceControlTimer
	NewTicker func(time.Duration) serviceControlTimer
	Sleep     func(time.Duration)
}

// realServiceControlClock é o relógio de produção.
func realServiceControlClock() serviceControlClock {
	return serviceControlClock{
		NewTimer:  func(d time.Duration) serviceControlTimer { return goTimer{t: time.NewTimer(d)} },
		NewTicker: func(d time.Duration) serviceControlTimer { return goTicker{t: time.NewTicker(d)} },
		Sleep:     time.Sleep,
	}
}

// serviceControlEffects são os efeitos colaterais observáveis do fluxo de
// controle. Nos testes viram gravações; em produção, envio de status ao SCM,
// log no logstore e teardown do core.
type serviceControlEffects struct {
	// Report envia um status ao SCM (changes <- svc.Status{...}).
	Report func(svc.Status)
	// NoteSystemShutdown marca que o SO entrou em encerramento/reinício.
	NoteSystemShutdown func()
	// Teardown encerra o core do agent exatamente uma vez (idempotente).
	Teardown func(reason string)
	// Logf registra no log unificado do serviço.
	Logf func(format string, args ...any)
}

// serviceControlFlow é a máquina de estados do ciclo de controle: decide, para
// cada comando do SCM, o status a reportar e quando o teardown acontece.
type serviceControlFlow struct {
	graceSeconds int
	cmdsAccepted svc.Accepted
	clock        serviceControlClock
	effects      serviceControlEffects

	// stopPending indica que o grace já começou (Stop/Shutdown repetido não
	// reagenda o deadline).
	stopPending bool
	// finished garante teardown + STOPPED reportados uma única vez.
	finished   bool
	checkpoint uint32

	graceTimer serviceControlTimer
	heartbeat  serviceControlTimer
}

// newServiceControlFlow monta o fluxo. clock/effects zerados caem nos padrões
// de produção (relógio real, efeitos no-op), de modo que o handler real só
// precisa informar o que de fato faz.
func newServiceControlFlow(graceSeconds int, clock serviceControlClock, effects serviceControlEffects) *serviceControlFlow {
	if clock.NewTimer == nil || clock.NewTicker == nil || clock.Sleep == nil {
		clock = realServiceControlClock()
	}
	if effects.Report == nil {
		effects.Report = func(svc.Status) {}
	}
	if effects.NoteSystemShutdown == nil {
		effects.NoteSystemShutdown = func() {}
	}
	if effects.Teardown == nil {
		effects.Teardown = func(string) {}
	}
	if effects.Logf == nil {
		effects.Logf = func(string, ...any) {}
	}
	// grace=0 → comportamento legado: NÃO aceita PRESHUTDOWN (o SCM não manda
	// a notificação antecipada de shutdown).
	cmds := svc.AcceptStop | svc.AcceptShutdown
	if graceSeconds > 0 {
		cmds |= svc.AcceptPreShutdown
	}
	return &serviceControlFlow{
		graceSeconds: graceSeconds,
		cmdsAccepted: cmds,
		clock:        clock,
		effects:      effects,
	}
}

// CmdsAccepted devolve os comandos aceitos, para reportar em svc.Status.
func (f *serviceControlFlow) CmdsAccepted() svc.Accepted {
	if f == nil {
		return svc.AcceptStop | svc.AcceptShutdown
	}
	return f.cmdsAccepted
}

// Close para os timers do grace/heartbeat (o handler chama no retorno).
func (f *serviceControlFlow) Close() {
	if f == nil {
		return
	}
	if f.graceTimer != nil {
		f.graceTimer.Stop()
		f.graceTimer = nil
	}
	if f.heartbeat != nil {
		f.heartbeat.Stop()
		f.heartbeat = nil
	}
}

// GraceC expõe o deadline do grace (nil enquanto não agendado), para o select
// do handler.
func (f *serviceControlFlow) GraceC() <-chan time.Time {
	if f == nil || f.graceTimer == nil {
		return nil
	}
	return f.graceTimer.Chan()
}

// HeartbeatC expõe o ticker de heartbeat (nil enquanto não agendado).
func (f *serviceControlFlow) HeartbeatC() <-chan time.Time {
	if f == nil || f.heartbeat == nil {
		return nil
	}
	return f.heartbeat.Chan()
}

// Heartbeat reporta STOP_PENDING com checkpoint incremental durante o grace.
func (f *serviceControlFlow) Heartbeat() {
	if f == nil || f.finished {
		return
	}
	f.checkpoint++
	f.effects.Report(svc.Status{
		State:      svc.StopPending,
		Accepts:    f.cmdsAccepted,
		CheckPoint: f.checkpoint,
		WaitHint:   uint32(f.graceSeconds * 1000),
	})
}

// HandleControl processa um comando do SCM. finish=true significa que o
// handler deve reportar STOPPED e retornar — o teardown já foi executado aqui.
func (f *serviceControlFlow) HandleControl(cmd svc.Cmd) (finish bool, reason string) {
	if f == nil {
		return false, ""
	}
	switch resolveServiceControl(cmd, f.graceSeconds) {
	case actionInterrogate:
		status := svc.Status{State: svc.Running, Accepts: f.cmdsAccepted}
		if f.stopPending {
			status.State = svc.StopPending
			status.CheckPoint = f.checkpoint
			status.WaitHint = uint32(f.graceSeconds * 1000)
		}
		// Dois reportes com intervalo curto: o SCM pode ter perdido o primeiro
		// enquanto o handler estava ocupado.
		f.effects.Report(status)
		f.clock.Sleep(100 * time.Millisecond)
		f.effects.Report(status)
		return false, ""

	case actionTeardownNow:
		// grace=0 (ou Stop): o encerramento é imediato, mas o sinal de
		// shutdown do SO continua valendo para o caminho de power action não
		// tratar o reboot como falha do PSADT.
		if isShutdownCommand(cmd) {
			f.effects.NoteSystemShutdown()
		}
		f.effects.Logf("[service] SCM %s recebido — encerrando imediatamente", svcCmdName(cmd))
		f.effects.Report(svc.Status{State: svc.StopPending, Accepts: f.cmdsAccepted})
		reason = "SCM " + svcCmdName(cmd)
		f.finish(reason)
		return true, reason

	case actionHoldThenTeardown:
		if f.stopPending {
			// Já em grace: mantém StopPending (o SCM não repete shutdown, mas
			// um Interrogate pode chegar).
			return false, ""
		}
		f.stopPending = true
		if isShutdownCommand(cmd) {
			f.effects.NoteSystemShutdown()
		}
		d := graceDurationFor(cmd, f.graceSeconds)
		f.effects.Logf("[service] SCM %s recebido — grace de encerramento ativo por %s (remoto preservado)", svcCmdName(cmd), d)
		f.effects.Report(svc.Status{
			State:      svc.StopPending,
			Accepts:    f.cmdsAccepted,
			CheckPoint: f.checkpoint,
			WaitHint:   uint32(d.Milliseconds()),
		})
		f.graceTimer = f.clock.NewTimer(d)
		// Heartbeat de status durante o grace: mantém o SCM informado de que o
		// serviço está em STOP_PENDING de propósito (não travado).
		f.heartbeat = f.clock.NewTicker(serviceControlHeartbeatInterval)
		return false, ""
	}
	return false, ""
}

// HandleGraceExpired executa o teardown quando o deadline do grace vence.
func (f *serviceControlFlow) HandleGraceExpired() { f.finish("grace expirado") }

// Stop encerra o ciclo por um motivo externo (ex.: canal do SCM fechado).
func (f *serviceControlFlow) Stop(reason string) { f.finish(reason) }

// finish executa o teardown exatamente uma vez e reporta STOPPED.
func (f *serviceControlFlow) finish(reason string) {
	if f == nil || f.finished {
		return
	}
	f.finished = true
	f.effects.Teardown(reason)
	f.effects.Report(svc.Status{State: svc.Stopped, Accepts: f.cmdsAccepted})
}

// isShutdownCommand indica os comandos que sinalizam encerramento do SO.
func isShutdownCommand(cmd svc.Cmd) bool {
	return cmd == svc.PreShutdown || cmd == svc.Shutdown
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
	// Logs unificados em logs.db — aberto antes de qualquer operação, para
	// capturar falhas de início, e com o sink do slog registrado. Só cai para
	// arquivo texto quando o banco não pode ser aberto.
	if ensureProcessLogStore("service", "discovery-service") == nil {
		if p := platform.ServiceLogFilePath(); p != "" {
			if err := logger.SetFileOutput(p); err != nil {
				log.Printf("[service] aviso: falha ao redirecionar log para %s: %v", p, err)
			}
		}
	}
	// Tee do stdlib log para o sink estruturado: linhas de log.Printf — dreno
	// do stderr do worker de remote session, logs do manager/spawn — antes iam
	// só para o stderr do serviço (invisível em produção) e o diagnóstico do
	// acesso remoto ficava impossível. Agora caem em logs.db (ou no arquivo de
	// fallback quando o banco não abre).
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
//
// A decisão de cada comando (e o deadline do grace) está em
// serviceControlFlow.HandleControl — aqui fica só o loop de eventos do SCM.
func (s *discoveryService) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
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

	flow := newServiceControlFlow(shutdownGraceSeconds(), realServiceControlClock(), serviceControlEffects{
		Report:             func(status svc.Status) { changes <- status },
		NoteSystemShutdown: noteSystemShutdown,
		// teardown executa o encerramento do core exatamente uma vez.
		Teardown: func(reason string) {
			s.teardownOnce.Do(func() {
				log.Printf("[service] %s — encerrando core", reason)
				app.ServiceShutdown()
			})
		},
		Logf: log.Printf,
	})
	defer flow.Close()

	changes <- svc.Status{State: svc.Running, Accepts: flow.CmdsAccepted()}
	log.Printf("[service] core do agent em execução (SYSTEM)")

	for {
		select {
		case c, ok := <-r:
			if !ok {
				flow.Stop("canal de controle do SCM fechado")
				return false, 0
			}
			if finish, _ := flow.HandleControl(c.Cmd); finish {
				// Exit code 0: encerramento limpo — NÃO conta como crash para
				// as failure actions do SCM (restart/5000 só dispara em código
				// não-zero).
				return false, 0
			}
		case <-flow.HeartbeatC():
			flow.Heartbeat()
		case <-flow.GraceC():
			flow.HandleGraceExpired()
			return false, 0
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
