package app

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"discovery/app/logs"
	"discovery/app/supportchat"
)

// captureAppLogs liga um buffer de log no App e devolve a fatia de linhas
// (compartilhada, sem mutex: o handler roda na mesma goroutine do teste).
func captureAppLogs(a *App) *[]string {
	lines := &[]string{}
	a.Logs.Buffer = logs.New()
	a.Logs.Subscribe(func(line string) { *lines = append(*lines, line) })
	return lines
}

// ── Fase 4: convite sem UI + watchdog com desfecho ─────────────────────────
//
// RELATO (teste real, agent fechado no tray): o convite chegou e o watchdog não
// logou ABSOLUTAMENTE NADA por 70 s — nem pedido, nem relançada, nem erro. O
// evento supportchat:invite era emitido ANTES da checagem hasSupportChatUI() e
// evaporava (não havia pipe/webview para recebê-lo).

// 4.1: sem UI, o convite NÃO é emitido como evento (ele evaporaria) e o fluxo
// vai direto para "pedir a UI" + aguardar.
func TestHandleTicketChatInvite_SemUINaoEmiteEvento(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1") // nenhum servidor: o POST falha e não interessa
	lines := captureAppLogs(a)

	// a.app == nil e sem IPC => hasSupportChatUI() == false.
	if a.hasSupportChatUI() {
		t.Fatal("pré-condição: este App de teste não tem UI")
	}

	ok, code, body, errText := a.handleTicketChatInviteCommand(nil, map[string]any{
		"sessionId":   "sessao-sem-ui",
		"ticketTitle": "Notebook lento",
		"supportUserName": "Ana",
		"expiresAtUtc":    time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
	})
	if !ok || code != 0 || errText != "" {
		t.Fatalf("o comando precisa ser reconhecido: ok=%v code=%d err=%q", ok, code, errText)
	}
	if !strings.Contains(body, "awaitingUi") {
		t.Fatalf("a resposta precisa marcar que o convite aguarda a UI: %s", body)
	}

	snapshot := append([]string(nil), (*lines)...) // cópia (a goroutine de redelivery também escreve)
	found := false
	for _, line := range snapshot {
		if strings.Contains(line, "SEM UI para apresentar") {
			found = true
		}
		if strings.Contains(line, "UI disponivel") {
			t.Fatalf("sem UI não pode haver reemissão imediata: %q", line)
		}
	}
	if !found {
		t.Fatalf("o desfecho 'convite sem UI' precisa ser logado (era o silêncio de 70s): %v", snapshot)
	}

	// E o convite fica registrado como pendente para a UI que subir.
	if got := a.supportChatService().State(); got.Status != supportchat.StatusWaiting || got.SessionID != "sessao-sem-ui" {
		t.Fatalf("o convite precisa ficar pendente aguardando a UI; obtido %+v", got)
	}
}

// 4.3: depois do prazo sem UI, o desfecho no_ui é registrado no log E o estado
// local só é limpo quando o servidor confirma a recusa.
func TestRespondSupportChatInviteWithOutcome_SemUIRegistraDesfecho(t *testing.T) {
	hits := &int32Locked{}
	a := newInviteResponseApp(t, 200, hits)
	a.supportChatService().PrepareInvite(supportchat.Invite{SessionID: "sessao-no-ui"})
	lines := captureAppLogs(a)

	a.respondSupportChatInviteWithOutcome("sessao-no-ui", false, supportChatInviteNoUIReason, true)
	logs := *lines

	if hits.get() != 1 {
		t.Fatalf("a recusa por no_ui precisa de UM POST; obtidos %d", hits.get())
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "desfecho no_ui reportado") {
		t.Fatalf("o desfecho no_ui precisa ser logado: %v", logs)
	}
	if got := a.supportChatService().State(); got.Status == supportchat.StatusWaiting {
		t.Fatalf("recusa entregue precisa limpar o convite; obtido %+v", got)
	}
}

// 4.3: se o servidor estiver fora, o desfecho é logado como FALHA e o convite
// NÃO é limpo (não pode morrer em silêncio).
func TestRespondSupportChatInviteWithOutcome_FalhaMantemConvite(t *testing.T) {
	restoreDelay := supportChatInviteNoUIBackoff
	supportChatInviteNoUIBackoff = 0
	restoreTries := supportChatInviteNoUITries
	supportChatInviteNoUITries = 2
	defer func() {
		supportChatInviteNoUIBackoff = restoreDelay
		supportChatInviteNoUITries = restoreTries
	}()

	hits := &int32Locked{}
	a := newInviteResponseApp(t, 500, hits)
	a.supportChatService().PrepareInvite(supportchat.Invite{SessionID: "sessao-no-ui-falha"})
	lines := captureAppLogs(a)

	a.respondSupportChatInviteWithOutcome("sessao-no-ui-falha", false, supportChatInviteNoUIReason, false)
	logs := *lines

	if hits.get() < 2 {
		t.Fatalf("a recusa por no_ui precisa ser retentada; POSTs=%d", hits.get())
	}
	if got := a.supportChatService().State(); got.Status != supportchat.StatusWaiting {
		t.Fatalf("falha na recusa não pode limpar o convite; obtido %+v", got)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "FALHA ao reportar o desfecho no_ui") {
		t.Fatalf("a falha precisa aparecer no log com o desfecho: %v", logs)
	}
}

// 4.2: o watchdog registra o desfecho de um pedido imediato (convite) em TODOS
// os caminhos — antes o cooldown/ausência de sessão eram silenciosos.
func TestUIWatchdog_RequestSpawnLogaDesfechos(t *testing.T) {
	t.Run("sem sessao de usuario ativa", func(t *testing.T) {
		f := &uiWatchdogSessionFake{}
		w := newUIWatchdogSessionFake(f)

		w.RequestSpawn("chat_invite")

		if len(f.spawns) != 0 {
			t.Fatalf("sem sessão não pode relançar; spawns=%v", f.spawns)
		}
		joined := strings.Join(f.logs, "\n")
		if !strings.Contains(joined, "sem sessao de usuario ativa") {
			t.Fatalf("o desfecho 'sem sessão' precisa ser logado: %v", f.logs)
		}
	})

	t.Run("cooldown ativo informa o tempo restante", func(t *testing.T) {
		f := &uiWatchdogSessionFake{sessions: []uint32{1}}
		w := newUIWatchdogSessionFake(f)

		w.RequestSpawn("chat_invite")
		if len(f.spawns) != 1 {
			t.Fatalf("o primeiro pedido deveria relançar; spawns=%v", f.spawns)
		}
		f.logs = nil
		f.advance(5 * time.Second)

		w.RequestSpawn("chat_invite")
		if len(f.spawns) != 1 {
			t.Fatalf("cooldown deveria barrar; spawns=%v", f.spawns)
		}
		joined := strings.Join(f.logs, "\n")
		if !strings.Contains(joined, "cooldown ativo") || !strings.Contains(joined, "restam") {
			t.Fatalf("o cooldown precisa ser logado com o tempo restante: %v", f.logs)
		}
	})

	t.Run("falha ao relancar", func(t *testing.T) {
		f := &uiWatchdogFake{spawnErr: errStr("sem token")}
		w := newUIWatchdogFake(f)

		w.RequestSpawn("chat_invite")

		joined := strings.Join(f.logs, "\n")
		if !strings.Contains(joined, "falha ao relançar a UI") {
			t.Fatalf("a falha precisa ser logada: %v", f.logs)
		}
	})

	t.Run("UI relancada", func(t *testing.T) {
		f := &uiWatchdogFake{}
		w := newUIWatchdogFake(f)

		w.RequestSpawn("chat_invite")

		joined := strings.Join(f.logs, "\n")
		if !strings.Contains(joined, "UI relançada minimizada no tray") {
			t.Fatalf("o sucesso precisa ser logado: %v", f.logs)
		}
	})
}

type errStr string

func (e errStr) Error() string { return string(e) }

// 4.2: o caminho single-session também informa o cooldown de um PEDIDO (o tick
// periódico continua silencioso de propósito).
func TestUIWatchdog_RequestSpawnCooldownLogadoSingleSession(t *testing.T) {
	f := &uiWatchdogFake{}
	w := newUIWatchdogFake(f)

	w.RequestSpawn("chat_invite")
	f.logs = nil
	w.RequestSpawn("chat_invite")

	joined := strings.Join(f.logs, "\n")
	if !strings.Contains(joined, "cooldown ativo") {
		t.Fatalf("pedido barrado pelo cooldown precisa aparecer no log: %v", f.logs)
	}
	_ = strconv.Itoa(0)
}
