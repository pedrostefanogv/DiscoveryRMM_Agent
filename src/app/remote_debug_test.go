package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestComputeRemoteDebugDeadline_DefaultOneHourCap(t *testing.T) {
	now := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	got := computeRemoteDebugDeadline("", now)
	want := now.Add(time.Hour)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestComputeRemoteDebugDeadline_UsesSoonerExpiry(t *testing.T) {
	now := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	expires := now.Add(20 * time.Minute).Format(time.RFC3339)
	got := computeRemoteDebugDeadline(expires, now)
	want := now.Add(20 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestComputeRemoteDebugDeadline_CapsLongExpiryToOneHour(t *testing.T) {
	now := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	expires := now.Add(3 * time.Hour).Format(time.RFC3339)
	got := computeRemoteDebugDeadline(expires, now)
	want := now.Add(time.Hour)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestIsRemoteDebugCommandType(t *testing.T) {
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
		if got := isRemoteDebugCommandType(tc.in); got != tc.want {
			t.Fatalf("isRemoteDebugCommandType(%q) = %t, want %t", tc.in, got, tc.want)
		}
	}
}

func TestParseRemoteDebugCommand_UsesCanonicalNATSSubject(t *testing.T) {
	cmd, err := parseRemoteDebugCommand(map[string]any{
		"action":    "start",
		"sessionId": "sess-1",
		"stream": map[string]any{
			"natsSubject": "tenant.client-1.site.site-1.agent.agent-1.remote-debug.log",
		},
	})
	if err != nil {
		t.Fatalf("parseRemoteDebugCommand: %v", err)
	}
	if got := strings.TrimSpace(cmd.Stream.NatsSubject); got != "tenant.client-1.site.site-1.agent.agent-1.remote-debug.log" {
		t.Fatalf("NatsSubject = %q", got)
	}
}

func TestParseRemoteDebugCommand_AllowsNullOptionalFields(t *testing.T) {
	cmd, err := parseRemoteDebugCommand(map[string]any{
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
		t.Fatalf("parseRemoteDebugCommand with nulls: %v", err)
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

func TestParseRemoteDebugCommand_AcceptsJSONStringPayload(t *testing.T) {
	raw := `{"action":"start","sessionId":"sess-3","logLevel":"debug","stream":{"natsSubject":"tenant.client-1.site.site-1.agent.agent-1.remote-debug.log"}}`
	cmd, err := parseRemoteDebugCommand(raw)
	if err != nil {
		t.Fatalf("parseRemoteDebugCommand string payload: %v", err)
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

func TestBuildRemoteDebugPublishers_RequiresCanonicalNATSSubject(t *testing.T) {
	_, err := buildRemoteDebugPublishers(DebugConfig{}, remoteDebugStreamConfig{}, "token", "", "")
	if err == nil {
		t.Fatalf("expected error when natsSubject is missing")
	}
	if !strings.Contains(err.Error(), "subject NATS ausente") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildRemoteDebugPublishers_RejectsNonCanonicalNATSSubject(t *testing.T) {
	_, err := buildRemoteDebugPublishers(DebugConfig{}, remoteDebugStreamConfig{
		NatsSubject: "tenant.client-1.site.site-1.agent.agent-1.remote.debug",
	}, "token", "", "")
	if err == nil {
		t.Fatalf("expected error for non-canonical remote debug subject")
	}
	if !strings.Contains(err.Error(), ".remote-debug.log") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFormatRemoteDebugMessageWithOrigin_UI(t *testing.T) {
	if got := formatRemoteDebugMessageWithOrigin("ui", "erro xyz"); got != "[ui] erro xyz" {
		t.Fatalf("formatRemoteDebugMessageWithOrigin = %q", got)
	}
	if got := formatRemoteDebugMessageWithOrigin("ui", "[ui] erro xyz"); got != "[ui] erro xyz" {
		t.Fatalf("formatRemoteDebugMessageWithOrigin should keep existing prefix, got %q", got)
	}
	if got := formatRemoteDebugMessageWithOrigin("", "mensagem sem origem"); got != "mensagem sem origem" {
		t.Fatalf("formatRemoteDebugMessageWithOrigin with empty origin = %q", got)
	}
}

func TestDetectRemoteDebugLevel_DefaultsToInfo(t *testing.T) {
	if got := detectRemoteDebugLevel("linha sem tag de nivel"); got != "info" {
		t.Fatalf("detectRemoteDebugLevel default = %q, want info", got)
	}
}

func TestDetectRemoteDebugLevel_ErrorKeywords(t *testing.T) {
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
		if got := detectRemoteDebugLevel(tc); got != "error" {
			t.Fatalf("detectRemoteDebugLevel(%q) = %q, want error", tc, got)
		}
	}
}

func TestDetectRemoteDebugLevel_WarnKeywords(t *testing.T) {
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
		if got := detectRemoteDebugLevel(tc); got != "warn" {
			t.Fatalf("detectRemoteDebugLevel(%q) = %q, want warn", tc, got)
		}
	}
}

func TestDetectRemoteDebugLevel_InfoDefault(t *testing.T) {
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
		if got := detectRemoteDebugLevel(tc); got != "info" {
			t.Fatalf("detectRemoteDebugLevel(%q) = %q, want info", tc, got)
		}
	}
}

func TestDetectRemoteDebugLevel_ExplicitTags(t *testing.T) {
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
		if got := detectRemoteDebugLevel(tc.line); got != tc.level {
			t.Fatalf("detectRemoteDebugLevel(%q) = %q, want %q", tc.line, got, tc.level)
		}
	}
}

func TestNormalizeRemoteDebugStreamLevel_MapsInvalidLevelsToInfo(t *testing.T) {
	if got := normalizeRemoteDebugStreamLevel("verbose"); got != "info" {
		t.Fatalf("normalizeRemoteDebugStreamLevel = %q, want info", got)
	}
}

func TestHandleAgentRuntimeCommand_UpdatePending(t *testing.T) {
	a := &App{updateTrigger: make(chan struct{}, 1)}
	handled, code, output, errText := a.handleAgentRuntimeCommand(context.Background(), "update", map[string]any{"action": "check-update"})
	if !handled {
		t.Fatalf("expected update command to be handled")
	}
	if code != 0 {
		t.Fatalf("expected update command code=0, got code=%d err=%q", code, errText)
	}
	if strings.TrimSpace(output) == "" {
		t.Fatalf("expected non-empty output on success, got output=%q err=%q", output, errText)
	}
}

func TestRemoteDebugHandleCommand_StopWithJSONStringPayloadDoesNotReturnParseError(t *testing.T) {
	m := newRemoteDebugManager(nil, nil, nil, nil, nil)
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

func TestRemoteDebugHandleCommand_StartWithJSONStringPayloadDoesNotReturnParseError(t *testing.T) {
	m := newRemoteDebugManager(nil, nil, nil, nil, nil)
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
