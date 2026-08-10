package remotedebug

import (
	"context"
	"strings"
	"testing"
	"time"
)

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

func TestAutoStopAtDeadline_DoesNotStopSessionWithDifferentDeadline(t *testing.T) {
	m := New(Deps{Logf: func(string) {}})

	// Sessão ativa com deadline T2 (mais longo).
	deadlineT2 := time.Now().Add(2 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx
	m.mu.Lock()
	m.activeSession = &Session{
		sessionID:   "sess-same-id",
		agentID:     "agent-1",
		minLevel:    LevelValue("info"),
		deadline:    deadlineT2,
		logQueue:    make(chan queuedLine, QueueSize),
		cancel:      cancel,
		unsubscribe: func() {},
		publishers:  []Publisher{},
	}
	m.mu.Unlock()

	// Timer antigo com deadline T1 (mais curto, já expirado) e mesmo sessionID.
	// Não deve parar a sessão ativa (deadline diferente).
	m.autoStopAtDeadline("sess-same-id", time.Now().Add(-time.Hour))

	m.mu.Lock()
	stillActive := m.activeSession != nil
	m.mu.Unlock()
	if !stillActive {
		t.Fatalf("expected session to remain active when deadline differs")
	}
}

func TestAutoStopAtDeadline_StopsSessionWithSameDeadline(t *testing.T) {
	m := New(Deps{Logf: func(string) {}})

	// Deadline já expirado para o timer disparar imediatamente.
	deadline := time.Now().Add(-time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx
	m.mu.Lock()
	m.activeSession = &Session{
		sessionID:   "sess-same-id",
		agentID:     "agent-1",
		minLevel:    LevelValue("info"),
		deadline:    deadline,
		logQueue:    make(chan queuedLine, QueueSize),
		cancel:      cancel,
		unsubscribe: func() {},
		publishers:  []Publisher{},
	}
	m.mu.Unlock()

	// Timer com o MESMO deadline (expirado) e mesmo sessionID → deve parar.
	m.autoStopAtDeadline("sess-same-id", deadline)

	m.mu.Lock()
	stillActive := m.activeSession != nil
	m.mu.Unlock()
	if stillActive {
		t.Fatalf("expected session to be stopped when deadline matches")
	}
}
