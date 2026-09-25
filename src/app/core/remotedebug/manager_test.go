package remotedebug

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"discovery/app/core/sessionlive"
)

func TestComputeMaxDeadline_FallsBackToInitialDeadlineWhenServerDoesNotAuthorizeRenewal(t *testing.T) {
	now := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	initial := now.Add(20 * time.Minute)

	// Servidor nao enviou maxExpiresAtUtc: o agente NAO pode estender alem do
	// prazo inicial concedido (antes caia em now+1h e mantinha a sessao viva
	// sem autorizacao do servidor).
	got := ComputeMaxDeadline("", now, initial)
	if !got.Equal(initial) {
		t.Fatalf("fallback = %s, want %s", got.Format(time.RFC3339), initial.Format(time.RFC3339))
	}
}

func TestComputeMaxDeadline_UsesServerValueWhenPresent(t *testing.T) {
	now := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	initial := now.Add(20 * time.Minute)
	serverMax := now.Add(time.Hour).Format(time.RFC3339)

	got := ComputeMaxDeadline(serverMax, now, initial)
	if !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("max = %s, want %s", got.Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339))
	}
}

func TestComputeDeadline_DefaultOneHourCap(t *testing.T) {
	now := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	got := ComputeDeadline("", now)
	want := now.Add(time.Hour)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestComputeDeadline_UsesSoonerExpiry(t *testing.T) {
	now := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	expires := now.Add(20 * time.Minute).Format(time.RFC3339)
	got := ComputeDeadline(expires, now)
	want := now.Add(20 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestComputeDeadline_CapsLongExpiryToOneHour(t *testing.T) {
	now := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	expires := now.Add(3 * time.Hour).Format(time.RFC3339)
	got := ComputeDeadline(expires, now)
	want := now.Add(time.Hour)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestIsCommandType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{in: "8", want: true},
		{in: "RemoteDebug", want: true},
		{in: "remote-debug", want: true},
		{in: "cmd", want: false},
	}
	for _, tc := range cases {
		if got := IsCommandType(tc.in); got != tc.want {
			t.Fatalf("IsCommandType(%q) = %t, want %t", tc.in, got, tc.want)
		}
	}
}

func TestParseCommand_UsesCanonicalNATSSubject(t *testing.T) {
	cmd, err := ParseCommand(map[string]any{
		"action":    "start",
		"sessionId": "sess-1",
		"stream": map[string]any{
			"natsSubject": "tenant.client-1.site.site-1.agent.agent-1.remote-debug.log",
		},
	})
	if err != nil {
		t.Fatalf("ParseCommand: %v", err)
	}
	if got := strings.TrimSpace(cmd.Stream.NatsSubject); got != "tenant.client-1.site.site-1.agent.agent-1.remote-debug.log" {
		t.Fatalf("NatsSubject = %q", got)
	}
}

func TestParseCommand_AllowsNullOptionalFields(t *testing.T) {
	cmd, err := ParseCommand(map[string]any{
		"action":       "stop",
		"sessionId":    "sess-2",
		"logLevel":     nil,
		"startedAtUtc": nil,
		"expiresAtUtc": nil,
		"stoppedAtUtc": nil,
		"stream": map[string]any{
			"natsSubject": "tenant.client-1.site.site-1.agent.agent-1.remote-debug.log",
			"natsWssUrl":  nil,
		},
	})
	if err != nil {
		t.Fatalf("ParseCommand with nulls: %v", err)
	}
	if cmd.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", cmd.LogLevel)
	}
	if cmd.ExpiresAtUTC != "" {
		t.Fatalf("ExpiresAtUTC = %q, want empty", cmd.ExpiresAtUTC)
	}
	if cmd.Stream.NatsWssURL != "" {
		t.Fatalf("NatsWssURL = %q, want empty", cmd.Stream.NatsWssURL)
	}
}

func TestParseCommand_AcceptsJSONStringPayload(t *testing.T) {
	raw := `{"action":"start","sessionId":"sess-3","logLevel":"debug","stream":{"natsSubject":"tenant.client-1.site.site-1.agent.agent-1.remote-debug.log"}}`
	cmd, err := ParseCommand(raw)
	if err != nil {
		t.Fatalf("ParseCommand string payload: %v", err)
	}
	if cmd.Action != "start" {
		t.Fatalf("Action = %q, want start", cmd.Action)
	}
	if cmd.SessionID != "sess-3" {
		t.Fatalf("SessionID = %q, want sess-3", cmd.SessionID)
	}
	if cmd.LogLevel != "debug" {
		t.Fatalf("LogLevel = %q, want debug", cmd.LogLevel)
	}
}

func TestBuildPublishers_RequiresCanonicalNATSSubject(t *testing.T) {
	_, err := BuildPublishers(Config{}, StreamConfig{}, "token", "", "")
	if err == nil {
		t.Fatalf("expected error when natsSubject is missing")
	}
	if !strings.Contains(err.Error(), "subject NATS ausente") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildPublishers_RejectsNonCanonicalNATSSubject(t *testing.T) {
	_, err := BuildPublishers(Config{}, StreamConfig{
		NatsSubject: "tenant.client-1.site.site-1.agent.agent-1.remote.debug",
	}, "token", "", "")
	if err == nil {
		t.Fatalf("expected error for non-canonical remote debug subject")
	}
	if !strings.Contains(err.Error(), ".remote-debug.log") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFormatMessageWithOrigin_UI(t *testing.T) {
	if got := FormatMessageWithOrigin("ui", "erro xyz"); got != "[ui] erro xyz" {
		t.Fatalf("FormatMessageWithOrigin = %q", got)
	}
	if got := FormatMessageWithOrigin("ui", "[ui] erro xyz"); got != "[ui] erro xyz" {
		t.Fatalf("FormatMessageWithOrigin should keep existing prefix, got %q", got)
	}
	if got := FormatMessageWithOrigin("", "mensagem sem origem"); got != "mensagem sem origem" {
		t.Fatalf("FormatMessageWithOrigin with empty origin = %q", got)
	}
}

func TestDetectLevel_DefaultsToInfo(t *testing.T) {
	if got := DetectLevel("linha sem tag de nivel"); got != "info" {
		t.Fatalf("DetectLevel default = %q, want info", got)
	}
}

func TestDetectLevel_ErrorKeywords(t *testing.T) {
	cases := []string{
		"[sync] falha ao sincronizar apps: timeout",
		"[sync] falha ao salvar snapshot local",
		"[agent-sync] POST hardware falhou: connection refused",
		"[agent-sync] erro ao verificar diff",
		"[PANIC] goroutine panicou: nil pointer dereference",
		"[CONTRACT_VIOLATION] component=Agent field=authToken",
		"[zero-touch] falha na tentativa imediata apos peer novo xyz",
		"[p2p] erro ao iniciar servidor local: port already in use",
		"[transport][nats] FALHA ao criar publishers",
		"stream: falha (EOF), fallback para endpoint sincrono",
		"acesso negado ao executar comando",
	}
	for _, tc := range cases {
		if got := DetectLevel(tc); got != "error" {
			t.Fatalf("DetectLevel(%q) = %q, want error", tc, got)
		}
	}
}

func TestDetectLevel_WarnKeywords(t *testing.T) {
	cases := []string{
		"[sync] aviso: configuracao indisponivel",
		"[sync] ping ignorado: eventId duplicado",
		"[sync] fila cheia, descartando: apps",
		"[agent-sync] ignorado: apiScheme invalido",
		"[heartbeat][force] timeout aguardando envio do heartbeat forcado",
		"[p2p][api] envio adiado por sobrecarga do servidor",
		"[zero-touch] limite de tentativas atingido, loop encerrado",
		"[zero-touch] oferta rejeitada de agente xyz",
		"configuracao de agente ausente: nenhum servidor configurado",
		"[heartbeat][status] conexao perdida: disconnect",
		"[agent-sync] envio remoto cancelado durante janela de adiamento",
	}
	for _, tc := range cases {
		if got := DetectLevel(tc); got != "warn" {
			t.Fatalf("DetectLevel(%q) = %q, want warn", tc, got)
		}
	}
}

func TestDetectLevel_InfoDefault(t *testing.T) {
	cases := []string{
		"[sync] coordinator iniciado",
		"[sync] recurso sincronizado: apps variant=stable",
		"[sync] full resync concluido: source=server",
		"[p2p] coordinator iniciado",
		"[p2p] peer descoberto: agentId=xyz source=mdns",
		"[zero-touch] configuracao recebida com sucesso, loop encerrado",
		"[config] contexto canonico persistido: clientId=... siteId=...",
		"[startup] modo debug ativo por tecla de atalho",
		"[heartbeat][nats] heartbeat publicado com sucesso",
		"mensagem recebida (150 chars)",
		"[agent] iniciando sessao",
		"[cmd] recebido: cmdType=powershell",
	}
	for _, tc := range cases {
		if got := DetectLevel(tc); got != "info" {
			t.Fatalf("DetectLevel(%q) = %q, want info", tc, got)
		}
	}
}

func TestDetectLevel_ExplicitTags(t *testing.T) {
	cases := []struct {
		line  string
		level string
	}{
		{"[error] algo deu errado", "error"},
		{"[warn] cuidado com isso", "warn"},
		{"[debug] valor da variavel x=42", "debug"},
		{"[trace] entrando na funcao foo", "trace"},
		{"[info] operacao concluida", "info"},
		{"[error] falha critica", "error"},
		{"[warn] timeout detectado", "warn"},
		{"error something broke", "error"},
		{"warn disk space low", "warn"},
	}
	for _, tc := range cases {
		if got := DetectLevel(tc.line); got != tc.level {
			t.Fatalf("DetectLevel(%q) = %q, want %q", tc.line, got, tc.level)
		}
	}
}

func TestNormalizeStreamLevel_MapsInvalidLevelsToInfo(t *testing.T) {
	if got := NormalizeStreamLevel("verbose"); got != "info" {
		t.Fatalf("NormalizeStreamLevel = %q, want info", got)
	}
}

func TestHandleCommand_StopWithJSONStringPayloadDoesNotReturnParseError(t *testing.T) {
	m := New(Deps{})
	raw := `{"action":"stop","sessionId":"sess-raw","logLevel":"info","expiresAtUtc":null,"stream":{"natsSubject":"tenant.client-1.site.site-1.agent.agent-1.remote-debug.log"}}`
	handled, code, _, errText := m.HandleCommand(context.Background(), "remotedebug", raw)
	if !handled {
		t.Fatalf("expected remotedebug command to be handled")
	}
	if code == 2 {
		t.Fatalf("expected no parse error (code=2), got err=%q", errText)
	}
	if code != 0 {
		t.Fatalf("expected stop to return code=0, got code=%d err=%q", code, errText)
	}
}

func TestHandleCommand_StartWithJSONStringPayloadDoesNotReturnParseError(t *testing.T) {
	m := New(Deps{})
	raw := `{"action":"start","sessionId":"sess-raw","logLevel":"debug","expiresAtUtc":"2026-05-12T02:35:31.6225487Z","stream":{"natsSubject":"tenant.client-1.site.site-1.agent.agent-1.remote-debug.log"}}`
	handled, code, _, errText := m.HandleCommand(context.Background(), "remotedebug", raw)
	if !handled {
		t.Fatalf("expected remotedebug command to be handled")
	}
	if code == 2 {
		t.Fatalf("expected no parse error (code=2), got err=%q", errText)
	}
	if code != 1 {
		t.Fatalf("expected start without config to fail as business error (code=1), got code=%d err=%q", code, errText)
	}
}

// TestStartSession_AutoInitializesLifecycleWhenStartupWasNotCalled cobre a
// causa raiz do bug do debug remoto no modo serviço (2026-09-24): o manager só
// recebia Startup pelo adapter Wails v3, que não existe em
// discovery-service.exe. O comando era recusado com exitCode=1 e SEM nenhum
// log, deixando o console vazio e o diagnóstico impossível.
//
// Aqui o Start NÃO chama Startup. Ele deve mesmo assim chegar até
// "iniciando sessao" (prova de que o lifecycle foi auto-inicializado) e falhar
// apenas por falta de transporte NATS — com o erro registrado no log.
func TestStartSession_AutoInitializesLifecycleWhenStartupWasNotCalled(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	m := New(Deps{
		Logf: func(line string) {
			mu.Lock()
			logs = append(logs, line)
			mu.Unlock()
		},
		GetConfig: func() Config {
			// Servidor inválido: o nats.Connect falha imediatamente, sem os 5s
			// de timeout do dial real.
			return Config{AuthToken: "token-1", AgentID: "agent-1", NatsServer: "not-a-valid-url"}
		},
		GetAgentConfig: func() AgentConfig {
			return AgentConfig{ClientID: "client-1", SiteID: "site-1"}
		},
	})

	raw := `{"action":"start","sessionId":"sess-auto","logLevel":"debug","stream":{"natsSubject":"tenant.client-1.site.site-1.agent.agent-1.remote-debug.log"}}`
	handled, code, _, errText := m.HandleCommand(context.Background(), "remotedebug", raw)
	if !handled {
		t.Fatalf("expected remotedebug command to be handled")
	}
	if code != 1 {
		t.Fatalf("expected code=1 (sem transporte NATS), got code=%d err=%q", code, errText)
	}
	if !strings.Contains(errText, "nenhum transporte remoto disponivel") {
		t.Fatalf("expected transport failure, got err=%q", errText)
	}

	mu.Lock()
	joined := strings.Join(logs, "\n")
	mu.Unlock()

	if strings.Contains(joined, "nao inicializado") {
		t.Fatalf("lifecycle deveria ter sido auto-inicializado, logs=%q", joined)
	}
	if !strings.Contains(joined, "[remote-debug] iniciando sessao") {
		t.Fatalf("esperava log de inicio de sessao; logs=%q", joined)
	}
	if !strings.Contains(joined, "FALHA ao criar publishers") {
		t.Fatalf("esperava log da falha de publishers; logs=%q", joined)
	}
}

// TestStartSession_RefusedAfterShutdown garante que o domínio não "ressuscita"
// num processo que já está encerrando (shutdown marca mesmo sem Startup prévio).
func TestStartSession_RefusedAfterShutdown(t *testing.T) {
	raw := `{"action":"start","sessionId":"sess-down","logLevel":"debug","stream":{"natsSubject":"tenant.client-1.site.site-1.agent.agent-1.remote-debug.log"}}`

	cases := []struct {
		name string
		stop func(*Manager)
	}{
		{name: "startup_depois_shutdown", stop: func(m *Manager) {
			_ = m.Startup(context.Background())
			m.Shutdown()
		}},
		{name: "shutdown_sem_startup", stop: func(m *Manager) { m.Shutdown() }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Deps{Logf: func(string) {}})
			tc.stop(m)

			_, code, _, errText := m.HandleCommand(context.Background(), "remotedebug", raw)
			if code != 1 {
				t.Fatalf("expected code=1, got code=%d err=%q", code, errText)
			}
			if !strings.Contains(errText, "nao inicializado") {
				t.Fatalf("expected refusal after shutdown, got err=%q", errText)
			}
		})
	}
}

// failingPublisher simula um transporte que sempre recusa a publicação.
type failingPublisher struct {
	calls atomic.Int64
}

func (f *failingPublisher) Name() string { return "fake" }
func (f *failingPublisher) Publish(context.Context, LogMessage) error {
	f.calls.Add(1)
	return errors.New("boom")
}
func (f *failingPublisher) PublishRaw(context.Context, string, []byte) error {
	f.calls.Add(1)
	return errors.New("boom")
}
func (f *failingPublisher) Subscribe(string, func([]byte)) (func(), error) {
	return func() {}, nil
}
func (f *failingPublisher) Close() error { return nil }

// recordingPublisher captura os frames de controle publicados e permite
// disparar manualmente um handler de assinatura (sem NATS real).
type recordingPublisher struct {
	mu       sync.Mutex
	subject  string
	payloads [][]byte
	handler  func([]byte)
	closed   bool
}

func (p *recordingPublisher) Name() string                              { return "recording" }
func (p *recordingPublisher) Publish(context.Context, LogMessage) error { return nil }
func (p *recordingPublisher) PublishRaw(_ context.Context, subject string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subject = subject
	cp := append([]byte(nil), payload...)
	p.payloads = append(p.payloads, cp)
	return nil
}
func (p *recordingPublisher) Subscribe(_ string, handler func([]byte)) (func(), error) {
	p.mu.Lock()
	p.handler = handler
	p.mu.Unlock()
	return func() {}, nil
}
func (p *recordingPublisher) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}
func (p *recordingPublisher) deliver(data []byte) {
	p.mu.Lock()
	handler := p.handler
	p.mu.Unlock()
	if handler != nil {
		handler(data)
	}
}
func (p *recordingPublisher) frames() []ControlEnvelope {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ControlEnvelope, 0, len(p.payloads))
	for _, raw := range p.payloads {
		env, err := DecodeControl(raw)
		if err == nil {
			out = append(out, env)
		}
	}
	return out
}

// TestPublishLoop_SignalsFatalFailureOnlyOnce garante que, com todos os
// transportes mortos, o operador recebe UMA linha clara e acionável em vez de
// uma linha por mensagem (o agent-service.log já passa de 8 MB) — e que o log
// continua existindo, porque a ausência total de log foi o que tornou o bug do
// debug remoto invisível.
func TestPublishLoop_SignalsFatalFailureOnlyOnce(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	m := New(Deps{Logf: func(line string) {
		mu.Lock()
		logs = append(logs, line)
		mu.Unlock()
	}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pub := &failingPublisher{}
	session := &Session{
		sessionID:  "sess-fail",
		agentID:    "agent-1",
		minLevel:   LevelValue("info"),
		deadline:   time.Now().Add(time.Hour),
		logQueue:   make(chan queuedLine, 64),
		cancel:     cancel,
		publishers: []Publisher{pub},
	}

	go m.publishLoop(ctx, session)
	for i := 0; i < 5; i++ {
		session.logQueue <- queuedLine{message: fmt.Sprintf("linha %d", i), level: "info"}
	}

	// Espera a fila ser drenada (comprimento de channel é seguro de ler).
	deadline := time.Now().Add(2 * time.Second)
	for len(session.logQueue) > 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if len(session.logQueue) > 0 {
		t.Fatalf("publishLoop nao drenou a fila (restante=%d)", len(session.logQueue))
	}
	cancel()

	mu.Lock()
	failures := 0
	for _, line := range logs {
		if strings.Contains(line, "PUBLICACAO FALHOU") {
			failures++
		}
	}
	joined := strings.Join(logs, "\n")
	mu.Unlock()

	if failures != 1 {
		t.Fatalf("esperava exatamente 1 sinal de falha, got %d\nlogs=%q", failures, joined)
	}

	// Com todos os transportes mortos, o publisher que falhou é aposentado
	// (activeIndex avança): não ficamos martelando a conexão a cada linha.
	if got := pub.calls.Load(); got != 1 {
		t.Fatalf("esperava 1 tentativa de publish (transporte aposentado), got %d", got)
	}
}

func TestShutdown_NoActiveSessionIsSafe(t *testing.T) {
	m := New(Deps{})
	// Sem sessão ativa: Shutdown não deve entrar em pânico.
	m.Shutdown()
	m.Shutdown() // idempotente
}

func TestShutdown_StopsActiveSession(t *testing.T) {
	var stopped bool
	m := New(Deps{
		Logf: func(string) {},
		ReplayLogs: func(fn func(string)) func() {
			return func() { stopped = true }
		},
	})

	// Inicializa o ciclo de vida (Startup) antes de injetar a sessão ativa.
	if err := m.Startup(context.Background()); err != nil {
		t.Fatalf("Startup falhou: %v", err)
	}

	// Injeta uma sessão ativa diretamente (evita depender de conexão NATS real).
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx
	m.mu.Lock()
	m.activeSession = &Session{
		sessionID:   "sess-shutdown",
		agentID:     "agent-1",
		minLevel:    LevelValue("info"),
		deadline:    time.Now().Add(time.Hour),
		logQueue:    make(chan queuedLine, QueueSize),
		cancel:      cancel,
		unsubscribe: func() { stopped = true },
		publishers:  []Publisher{},
	}
	m.mu.Unlock()

	m.Shutdown()
	if !stopped {
		t.Fatalf("expected active session unsubscribe to be called on Shutdown")
	}

	// Após shutdown, stop não deve encontrar sessão ativa.
	if m.stopSession("sess-shutdown", "stop") {
		t.Fatalf("expected no active session after Shutdown")
	}
}

// TestHandleControlFrame_PingRegistraPeerERespondePong cobre o caminho real do
// canal de controle: o ping do viewer registra presença e o agente responde
// pong no MESMO subject.
func TestHandleControlFrame_PingRegistraPeerERespondePong(t *testing.T) {
	m := New(Deps{Logf: func(string) {}})
	pub := &recordingPublisher{}
	startedAt := time.Now().UTC()
	session := &Session{
		sessionID:      "sess-ctrl",
		agentID:        "agent-1",
		controlSubject: "tenant.c.site.s.agent.a.remote-debug.control",
		minLevel:       LevelValue("info"),
		deadline:       startedAt.Add(20 * time.Minute),
		ttl:            20 * time.Minute,
		startedAt:      startedAt,
		live: sessionlive.NewPeer(sessionlive.Config{
			Interval:      5 * time.Second,
			MissesAllowed: 3,
			InitialGrace:  60 * time.Second,
			MaxDeadline:   startedAt.Add(time.Hour),
		}, startedAt),
		publishers: []Publisher{pub},
	}

	ping, err := EncodeControl(NewControlEnvelope(RoleViewer, ControlTypePing, "sess-ctrl", 1, nil))
	if err != nil {
		t.Fatalf("EncodeControl: %v", err)
	}
	m.handleControlFrame(session, ping)

	if !session.live.PeerSeen() {
		t.Fatalf("ping do viewer deveria registrar presenca do peer")
	}
	frames := pub.frames()
	if len(frames) != 1 || frames[0].Type != ControlTypePong || frames[0].From != RoleAgent {
		t.Fatalf("esperava 1 pong do agente, got %+v", frames)
	}
}

// TestHandleControlFrame_IgnoraEcoDoProprioAgente garante que o agente não
// conta o próprio frame como sinal do viewer (o subject tem pub+sub nos dois
// sentidos, então o eco é esperado).
func TestHandleControlFrame_IgnoraEcoDoProprioAgente(t *testing.T) {
	m := New(Deps{Logf: func(string) {}})
	startedAt := time.Now().UTC()
	session := &Session{
		sessionID: "sess-eco",
		live: sessionlive.NewPeer(sessionlive.Config{
			Interval:      5 * time.Second,
			MissesAllowed: 3,
			InitialGrace:  60 * time.Second,
		}, startedAt),
	}
	raw, _ := EncodeControl(NewControlEnvelope(RoleAgent, ControlTypePing, "sess-eco", 1, nil))
	m.handleControlFrame(session, raw)
	if session.live.PeerSeen() {
		t.Fatalf("eco do proprio agente nao deveria contar como sinal do peer")
	}
}

// TestHandleControlFrame_SetLevelAplicaSemRestart cobre a troca de nível em
// tempo real: sem novo sessionId, sem matar a sessão, com confirmação.
func TestHandleControlFrame_SetLevelAplicaSemRestart(t *testing.T) {
	m := New(Deps{Logf: func(string) {}})
	pub := &recordingPublisher{}
	startedAt := time.Now().UTC()
	session := &Session{
		sessionID:      "sess-level",
		agentID:        "agent-1",
		controlSubject: "tenant.c.site.s.agent.a.remote-debug.control",
		minLevel:       LevelValue("info"),
		ttl:            20 * time.Minute,
		startedAt:      startedAt,
		live: sessionlive.NewPeer(sessionlive.Config{
			Interval:      5 * time.Second,
			MissesAllowed: 3,
			InitialGrace:  60 * time.Second,
		}, startedAt),
		publishers: []Publisher{pub},
	}
	raw, _ := EncodeControl(NewControlEnvelope(RoleServer, ControlTypeSetLevel, "sess-level", 2, map[string]any{"logLevel": "warn"}))
	m.handleControlFrame(session, raw)

	if got := session.SessionMinLevel(); got != LevelValue("warn") {
		t.Fatalf("minLevel = %d, want %d", got, LevelValue("warn"))
	}
	if session.sessionID != "sess-level" {
		t.Fatalf("a troca de nivel NAO deve trocar o sessionId")
	}
	frames := pub.frames()
	if len(frames) != 1 || frames[0].Type != ControlTypeLevelChanged {
		t.Fatalf("esperava levelChanged, got %+v", frames)
	}
}

// TestHandleControlFrame_DescartaSessionIdDiferente evita que frames de outra
// sessão contaminem a liveness desta.
func TestHandleControlFrame_DescartaSessionIdDiferente(t *testing.T) {
	m := New(Deps{Logf: func(string) {}})
	startedAt := time.Now().UTC()
	session := &Session{
		sessionID: "sess-a",
		live:      sessionlive.NewPeer(sessionlive.Config{Interval: 5 * time.Second, MissesAllowed: 3, InitialGrace: 60 * time.Second}, startedAt),
	}
	raw, _ := EncodeControl(NewControlEnvelope(RoleViewer, ControlTypePing, "sess-b", 1, nil))
	m.handleControlFrame(session, raw)
	if session.live.PeerSeen() {
		t.Fatalf("frame de outra sessao nao deveria contar")
	}
}

// TestExtendDeadline_ClampaNoTetoEEncerraComMaxDuration garante o teto da
// sessão (duração configurada na instalação) mesmo com o viewer ativo.
func TestExtendDeadline_ClampaNoTetoEEncerraComMaxDuration(t *testing.T) {
	var logs []string
	var mu sync.Mutex
	m := New(Deps{Logf: func(line string) { mu.Lock(); logs = append(logs, line); mu.Unlock() }})
	startedAt := time.Now().UTC()
	pub := &recordingPublisher{}
	session := &Session{
		sessionID:      "sess-max",
		controlSubject: "tenant.c.site.s.agent.a.remote-debug.control",
		minLevel:       LevelValue("info"),
		ttl:            20 * time.Minute,
		startedAt:      startedAt,
		maxDeadline:    startedAt.Add(time.Hour),
		live: sessionlive.NewPeer(sessionlive.Config{
			Interval:      5 * time.Second,
			MissesAllowed: 3,
			InitialGrace:  60 * time.Second,
			MaxDeadline:   startedAt.Add(time.Hour),
		}, startedAt),
		publishers: []Publisher{pub},
	}
	session.live.NotePeerSignal(startedAt)
	m.mu.Lock()
	m.activeSession = session
	m.mu.Unlock()

	// Antes do teto: estende e permanece ativa.
	m.extendDeadline(session)
	m.mu.Lock()
	active := m.activeSession
	m.mu.Unlock()
	if active == nil {
		t.Fatalf("sessao nao deveria encerrar antes do teto")
	}
	if session.deadline.After(session.maxDeadline) {
		t.Fatalf("deadline nao pode ultrapassar maxDeadline")
	}

	// Forca o teto no passado e verifica o encerramento por max-duration.
	session.maxDeadline = startedAt.Add(-time.Second)
	session.live = sessionlive.NewPeer(sessionlive.Config{
		Interval:      5 * time.Second,
		MissesAllowed: 3,
		InitialGrace:  60 * time.Second,
		MaxDeadline:   startedAt.Add(-time.Second),
	}, startedAt)
	session.live.NotePeerSignal(startedAt)
	m.extendDeadline(session)
	m.mu.Lock()
	active = m.activeSession
	m.mu.Unlock()
	if active != nil {
		t.Fatalf("sessao deveria encerrar ao atingir o teto")
	}
	mu.Lock()
	joined := strings.Join(logs, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "max-duration") {
		t.Fatalf("esperava log de max-duration, logs=%q", joined)
	}
}
