//go:build windows

package app

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestResolveServiceControl(t *testing.T) {
	cases := []struct {
		name  string
		cmd   svc.Cmd
		grace int
		want  serviceControlAction
	}{
		{"stop encerra imediatamente", svc.Stop, 30, actionTeardownNow},
		{"stop sem grace", svc.Stop, 0, actionTeardownNow},
		{"preshutdown com grace segura", svc.PreShutdown, 30, actionHoldThenTeardown},
		{"preshutdown sem grace encerra", svc.PreShutdown, 0, actionTeardownNow},
		{"shutdown com grace segura", svc.Shutdown, 10, actionHoldThenTeardown},
		{"shutdown sem grace encerra", svc.Shutdown, 0, actionTeardownNow},
		{"interrogate responde status", svc.Interrogate, 30, actionInterrogate},
		{"desconhecido responde status", svc.Cmd(200), 30, actionInterrogate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveServiceControl(tc.cmd, tc.grace); got != tc.want {
				t.Fatalf("resolveServiceControl(%v,%d)=%d, want %d", tc.cmd, tc.grace, got, tc.want)
			}
		})
	}
}

func TestGraceDurationFor(t *testing.T) {
	if got := graceDurationFor(svc.PreShutdown, 30); got != 30*time.Second {
		t.Fatalf("preshutdown: got %s, want 30s", got)
	}
	// Fallback legacy (Shutdown) e limitado ao cap do Windows.
	if got := graceDurationFor(svc.Shutdown, 30); got != legacyShutdownGraceCap {
		t.Fatalf("shutdown cap: got %s, want %s", got, legacyShutdownGraceCap)
	}
	if got := graceDurationFor(svc.Shutdown, 2); got != 2*time.Second {
		t.Fatalf("shutdown curto: got %s, want 2s", got)
	}
}

func TestShutdownGraceSeconds(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", defaultShutdownGraceSeconds},
		{"0", 0},
		{"60", 60},
		{"999", maxShutdownGraceSeconds},
		{"-5", 0},
		{"abc", defaultShutdownGraceSeconds},
	}
	for _, tc := range cases {
		t.Run("env="+tc.env, func(t *testing.T) {
			t.Setenv("DISCOVERY_SHUTDOWN_GRACE_SECONDS", tc.env)
			if got := shutdownGraceSeconds(); got != tc.want {
				t.Fatalf("shutdownGraceSeconds(%q)=%d, want %d", tc.env, got, tc.want)
			}
		})
	}
}

// ── Integração da sequência do PRESHUTDOWN (A.4.1, Fase 5/8.3) ──────────────
//
// Estes testes exercitam a MESMA máquina de estados usada por
// discoveryService.Execute, com o SCM substituído por um dublê: nenhum serviço
// real, nenhuma App e nenhum timer real (o deadline é disparado na mão).
// O que se prova é a ORDEM: PRESHUTDOWN aceito → nota de shutdown do sistema →
// STOP_PENDING/deadline → teardown → STOPPED, e que o teardown nunca acontece
// antes do deadline.
//
// Validação em VM (A.4.2) — deliberadamente NÃO automatizada, ainda pendente:
//   - o SCM entrega SERVICE_CONTROL_PRESHUTDOWN ANTES do logoff/desligamento
//     (Windows 10/11, com Fast Startup ligado E desligado);
//   - durante o grace o acesso remoto (agentConn/NATS/remote debug) continua
//     respondendo;
//   - `sc stop DiscoveryAgent` retorna em < 5s (Stop não usa grace);
//   - PRESHUTDOWN com grace=0 mantém o comportamento legado (encerra na hora).

// fakeServiceControlTimer é um timer controlado pelo teste: o deadline só
// vence quando o teste envia no canal.
type fakeServiceControlTimer struct {
	ch      chan time.Time
	stopped bool
}

func newFakeServiceControlTimer() *fakeServiceControlTimer {
	return &fakeServiceControlTimer{ch: make(chan time.Time, 1)}
}

func (t *fakeServiceControlTimer) Chan() <-chan time.Time { return t.ch }
func (t *fakeServiceControlTimer) Stop()                  { t.stopped = true }

// serviceControlProbe é o dublê de SCM: grava a ordem dos efeitos, os status
// reportados e os timers pedidos pelo fluxo.
type serviceControlProbe struct {
	events          []string
	statuses        []svc.Status
	timers          []*fakeServiceControlTimer
	timerDurations  []time.Duration
	tickers         []*fakeServiceControlTimer
	tickerDurations []time.Duration
	sleeps          []time.Duration
}

func (p *serviceControlProbe) clock() serviceControlClock {
	return serviceControlClock{
		NewTimer: func(d time.Duration) serviceControlTimer {
			p.timerDurations = append(p.timerDurations, d)
			t := newFakeServiceControlTimer()
			p.timers = append(p.timers, t)
			return t
		},
		NewTicker: func(d time.Duration) serviceControlTimer {
			p.tickerDurations = append(p.tickerDurations, d)
			t := newFakeServiceControlTimer()
			p.tickers = append(p.tickers, t)
			return t
		},
		Sleep: func(d time.Duration) { p.sleeps = append(p.sleeps, d) },
	}
}

func (p *serviceControlProbe) effects() serviceControlEffects {
	return serviceControlEffects{
		Report: func(status svc.Status) {
			p.statuses = append(p.statuses, status)
			p.events = append(p.events, "status:"+serviceStateName(status.State))
		},
		NoteSystemShutdown: func() { p.events = append(p.events, "nota-shutdown") },
		Teardown:           func(reason string) { p.events = append(p.events, "teardown:"+reason) },
		Logf:               func(string, ...any) {},
	}
}

func (p *serviceControlProbe) newFlow(graceSeconds int) *serviceControlFlow {
	return newServiceControlFlow(graceSeconds, p.clock(), p.effects())
}

// eventIndex devolve a posição do primeiro evento com o prefixo informado.
func (p *serviceControlProbe) eventIndex(prefix string) int {
	for i, e := range p.events {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

// countEvents conta eventos com o prefixo informado.
func (p *serviceControlProbe) countEvents(prefix string) int {
	n := 0
	for _, e := range p.events {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

func (p *serviceControlProbe) lastStatus() svc.Status {
	return p.statuses[len(p.statuses)-1]
}

func serviceStateName(state svc.State) string {
	switch state {
	case svc.StartPending:
		return "StartPending"
	case svc.Running:
		return "Running"
	case svc.StopPending:
		return "StopPending"
	case svc.Stopped:
		return "Stopped"
	case svc.Paused:
		return "Paused"
	case svc.ContinuePending:
		return "ContinuePending"
	case svc.PausePending:
		return "PausePending"
	default:
		return "unknown"
	}
}

// (a) PRESHUTDOWN com grace > 0: aceita, anota o shutdown do sistema, agenda o
// teardown para o deadline e NÃO encerra antes dele.
func TestServiceControlFlow_PreshutdownHoldsUntilDeadline(t *testing.T) {
	p := &serviceControlProbe{}
	flow := p.newFlow(30)
	defer flow.Close()

	if flow.CmdsAccepted()&svc.AcceptPreShutdown == 0 {
		t.Fatal("com grace > 0 o serviço precisa aceitar PRESHUTDOWN")
	}

	finish, _ := flow.HandleControl(svc.PreShutdown)
	if finish {
		t.Fatal("PRESHUTDOWN com grace não pode encerrar na hora")
	}
	if len(p.timers) != 1 {
		t.Fatalf("PRESHUTDOWN com grace deveria agendar exatamente 1 deadline; timers=%d", len(p.timers))
	}
	if got := p.timerDurations[0]; got != 30*time.Second {
		t.Fatalf("deadline do grace = %s, esperado 30s", got)
	}
	if p.countEvents("teardown:") != 0 {
		t.Fatalf("teardown não pode rodar antes do deadline; eventos=%v", p.events)
	}
	// ORDEM: o shutdown do sistema é sinalizado ANTES de qualquer teardown.
	nota := p.eventIndex("nota-shutdown")
	if nota < 0 {
		t.Fatalf("PRESHUTDOWN deveria anotar o shutdown do sistema; eventos=%v", p.events)
	}
	last := p.lastStatus()
	if last.State != svc.StopPending || last.WaitHint != 30000 {
		t.Fatalf("status esperado StopPending/WaitHint=30000ms; obtido %s/%dms",
			serviceStateName(last.State), last.WaitHint)
	}

	// Um Shutdown chegando durante o grace (o SCM pode mandar os dois) NÃO
	// antecipa o teardown nem reagenda o deadline.
	if finish, _ := flow.HandleControl(svc.Shutdown); finish {
		t.Fatal("Shutdown durante o grace não pode encerrar na hora")
	}
	if len(p.timers) != 1 {
		t.Fatalf("Shutdown em grace não pode reagendar o deadline; timers=%d", len(p.timers))
	}

	// Interrogate durante o grace continua respondendo STOP_PENDING.
	if finish, _ := flow.HandleControl(svc.Interrogate); finish {
		t.Fatal("Interrogate não pode encerrar o serviço")
	}
	if p.lastStatus().State != svc.StopPending {
		t.Fatalf("Interrogate em grace deveria responder StopPending; obtido %s", serviceStateName(p.lastStatus().State))
	}

	// Deadline vencido (disparado pelo teste, sem esperar 30s): o canal já
	// precisa estar armado logo depois do PRESHUTDOWN.
	if flow.GraceC() == nil {
		t.Fatal("o canal do deadline do grace deveria existir depois do PRESHUTDOWN")
	}
	p.timers[0].ch <- time.Now()
	select {
	case <-flow.GraceC():
	case <-time.After(2 * time.Second):
		t.Fatal("o deadline do grace deveria disparar quando o timer vence")
	}
	flow.HandleGraceExpired()

	if got := p.countEvents("teardown:"); got != 1 {
		t.Fatalf("teardown deveria rodar exatamente 1 vez; rodou %d (eventos=%v)", got, p.events)
	}
	if teardown := p.eventIndex("teardown:"); teardown <= nota {
		t.Fatalf("ORDEM violada: preshutdown (nota-shutdown=%d) precisa vir antes do teardown=%d (eventos=%v)",
			nota, teardown, p.events)
	}
	if got := p.events[p.eventIndex("teardown:")]; got != "teardown:grace expirado" {
		t.Fatalf("motivo do teardown inesperado: %s", got)
	}
	if last := p.lastStatus(); last.State != svc.Stopped {
		t.Fatalf("depois do teardown o último status deve ser Stopped; obtido %s", serviceStateName(last.State))
	}

	// Idempotência: um segundo evento de deadline não repete teardown/STOPPED.
	flow.HandleGraceExpired()
	if got := p.countEvents("teardown:"); got != 1 {
		t.Fatalf("teardown duplicado: %d (eventos=%v)", got, p.events)
	}
}

// (b) Shutdown SEM PRESHUTDOWN: o Windows concede só WaitToKillServiceTimeout
// (~5s), então o deadline é limitado ao cap legado de 4s — mesmo com grace 30.
func TestServiceControlFlow_ShutdownWithoutPreshutdownUsesLegacyCap(t *testing.T) {
	p := &serviceControlProbe{}
	flow := p.newFlow(30)
	defer flow.Close()

	finish, _ := flow.HandleControl(svc.Shutdown)
	if finish {
		t.Fatal("Shutdown com grace deveria segurar o teardown")
	}
	if len(p.timerDurations) != 1 {
		t.Fatalf("Shutdown deveria agendar 1 deadline; timers=%d", len(p.timerDurations))
	}
	if got := p.timerDurations[0]; got != legacyShutdownGraceCap {
		t.Fatalf("Shutdown sem PRESHUTDOWN deveria usar o cap de %s; usou %s", legacyShutdownGraceCap, got)
	}
	if last := p.lastStatus(); last.State != svc.StopPending || last.WaitHint != uint32(legacyShutdownGraceCap.Milliseconds()) {
		t.Fatalf("status esperado StopPending/WaitHint=%dms; obtido %s/%dms",
			legacyShutdownGraceCap.Milliseconds(), serviceStateName(last.State), last.WaitHint)
	}
	if p.countEvents("teardown:") != 0 {
		t.Fatalf("teardown não pode rodar antes do deadline; eventos=%v", p.events)
	}

	// O heartbeat mantém o SCM informado durante o grace.
	if len(p.tickers) != 1 {
		t.Fatalf("grace deveria armazenar 1 heartbeat; tickers=%d", len(p.tickers))
	}
	flow.Heartbeat()
	if p.lastStatus().State != svc.StopPending || p.lastStatus().CheckPoint != 1 {
		t.Fatalf("heartbeat deveria reportar StopPending checkpoint=1; obtido %s/%d",
			serviceStateName(p.lastStatus().State), p.lastStatus().CheckPoint)
	}

	flow.HandleGraceExpired()
	if p.countEvents("teardown:") != 1 || p.lastStatus().State != svc.Stopped {
		t.Fatalf("deadline vencido deveria encerrar; eventos=%v", p.events)
	}
}

// (c) PRESHUTDOWN com grace = 0: comportamento legado — não aceita
// PRESHUTDOWN, não agenda deadline e encerra imediatamente (anotando o
// shutdown do sistema para a power action).
func TestServiceControlFlow_PreshutdownWithoutGraceTearsDownNow(t *testing.T) {
	cases := []struct {
		name       string
		cmd        svc.Cmd
		wantReason string
		wantNote   bool
	}{
		{"preshutdown", svc.PreShutdown, "SCM PreShutdown", true},
		{"shutdown", svc.Shutdown, "SCM Shutdown", true},
		{"stop", svc.Stop, "SCM Stop", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &serviceControlProbe{}
			flow := p.newFlow(0)
			defer flow.Close()

			if flow.CmdsAccepted()&svc.AcceptPreShutdown != 0 {
				t.Fatal("grace=0 não pode aceitar PRESHUTDOWN (comportamento legado)")
			}

			finish, reason := flow.HandleControl(tc.cmd)
			if !finish {
				t.Fatalf("%s com grace=0 deveria encerrar imediatamente", tc.name)
			}
			if reason != tc.wantReason {
				t.Fatalf("motivo = %q, esperado %q", reason, tc.wantReason)
			}
			if len(p.timers) != 0 || len(p.tickers) != 0 {
				t.Fatalf("grace=0 não pode agendar deadline/heartbeat; timers=%d tickers=%d", len(p.timers), len(p.tickers))
			}

			// Ordem: nota do shutdown (quando aplicável) → StopPending →
			// teardown → Stopped.
			want := []string{"status:StopPending", "teardown:" + tc.wantReason, "status:Stopped"}
			if tc.wantNote {
				want = append([]string{"nota-shutdown"}, want...)
			}
			if len(p.events) != len(want) {
				t.Fatalf("eventos = %v, esperado %v", p.events, want)
			}
			for i := range want {
				if p.events[i] != want[i] {
					t.Fatalf("ORDEM/evento %d = %q, esperado %q (eventos=%v)", i, p.events[i], want[i], p.events)
				}
			}
		})
	}
}
