package app

// Testes da camada de autenticação/limites do IPC (S1/S2/S3/C3).
// Este arquivo é independente de plataforma; os testes de servidor/pipe real
// ficam em ipc_auth_windows_test.go.

import (
	"context"
	"testing"
	"time"
)

func TestIPCMethodClassification(t *testing.T) {
	mutants := []string{
		"debug:set", "config:set", "memory:add", "memory:delete",
		"updates:scan", "knowledge:refresh", "support:create",
		"support:comment_add", "support:comment_add_legacy", "support:close",
		"support:reopen", "support:rate", "support:agent_ticket_create",
		"support:agent_ticket_close", "support:agent_comment_add",
		"support:ticket_attachment_upload", "support:comment_attachment_upload",
		"support:ticket_attachment_save", "support:comment_attachment_save",
		// Ações da loja executadas pelo SERVIÇO (SYSTEM): mutantes por
		// definição (instalam software). Sem a tabela explícita o sufixo
		// "install"/"upgrade" não é reconhecido e o método vazaria para o
		// canal de leitura sem token.
		"store:install", "store:uninstall", "store:upgrade", "store:upgrade_all",
		// sufixos genéricos (métodos novos não precisam editar a tabela)
		"qualquer:set", "foo:write", "foo:delete", "foo:create", "foo:add",
		"foo:upload", "foo.save", "x:refresh", "y:scan",
	}
	for _, method := range mutants {
		if !isIPCMutatingMethod(method) {
			t.Fatalf("%q deveria ser classificado como MUTANTE", method)
		}
		if !ipcMethodRequiresAdmin(method) {
			t.Fatalf("%q deveria exigir canal admin", method)
		}
	}

	reads := []string{
		"", "status:pending_counts", "config:get", "debug:get", "memory:list",
		"logs:tail", "store:catalog", "inventory:snapshot", "automation:state",
		"support:tickets", "support:ticket", "support:comments", "support:options",
		"support:ticket_attachment_download", "support:comment_attachment_download",
		"knowledge:list", "knowledge:article", "p2p:peers", "metodo:desconhecido",
	}
	for _, method := range reads {
		if isIPCMutatingMethod(method) {
			t.Fatalf("%q deveria ser classificado como LEITURA", method)
		}
	}
}

func TestIPCTokenStoreIssueAndValidate(t *testing.T) {
	store := newIPCTokenStore()
	id := ipcAuthIdentity{SID: "S-1-5-21-1", PID: 1234, SessionID: 2, Valid: true}

	token, expires, err := store.issue("conn-a", id)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if len(token) != 64 {
		t.Fatalf("token deveria ter 64 hex chars (32 bytes), got %d", len(token))
	}
	if time.Until(expires) <= 0 {
		t.Fatal("expiração deveria estar no futuro")
	}
	if _, err := store.validate(token, "conn-a", id); err != nil {
		t.Fatalf("token recém-emitido deveria validar: %v", err)
	}

	// ausente / inválido
	if _, err := store.validate("", "conn-a", id); err != errIPCTokenMissing {
		t.Fatalf("token vazio: esperado errIPCTokenMissing, got %v", err)
	}
	if _, err := store.validate("nao-existe", "conn-a", id); err != errIPCTokenInvalid {
		t.Fatalf("token inválido: esperado errIPCTokenInvalid, got %v", err)
	}
	// outra conexão
	if _, err := store.validate(token, "conn-b", id); err != errIPCTokenOther {
		t.Fatalf("outra conexão: esperado errIPCTokenOther, got %v", err)
	}
	// binding divergente (mesma conn, outro pid/sessão)
	other := id
	other.PID = 999
	if _, err := store.validate(token, "conn-a", other); err != errIPCTokenBinding {
		t.Fatalf("binding divergente: esperado errIPCTokenBinding, got %v", err)
	}
	other = id
	other.SessionID = 9
	if _, err := store.validate(token, "conn-a", other); err != errIPCTokenBinding {
		t.Fatalf("sessão divergente: esperado errIPCTokenBinding, got %v", err)
	}
}

func TestIPCTokenStoreExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newIPCTokenStore()
	store.now = func() time.Time { return now }
	id := ipcAuthIdentity{SID: "S-1-5-21-1", PID: 1, SessionID: 1, Valid: true}
	token, _, err := store.issue("conn-a", id)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	now = now.Add(ipcAuthTokenTTL + time.Second)
	if _, err := store.validate(token, "conn-a", id); err != errIPCTokenExpired {
		t.Fatalf("token expirado: esperado errIPCTokenExpired, got %v", err)
	}
	// token expirado é removido do store
	if _, err := store.validate(token, "conn-a", id); err != errIPCTokenInvalid {
		t.Fatalf("token expirado deveria ter sido removido, got %v", err)
	}
}

func TestIPCTokenRenewInvalidatesPrevious(t *testing.T) {
	store := newIPCTokenStore()
	id := ipcAuthIdentity{SID: "s", PID: 1, SessionID: 1, Valid: true}
	first, _, _ := store.issue("conn-a", id)
	second, _, err := store.issue("conn-a", id)
	if err != nil {
		t.Fatalf("issue renovado: %v", err)
	}
	if first == second {
		t.Fatal("renovação deveria gerar token novo")
	}
	if _, err := store.validate(first, "conn-a", id); err == nil {
		t.Fatal("token anterior deveria ser invalidado na renovação")
	}
	if _, err := store.validate(second, "conn-a", id); err != nil {
		t.Fatalf("token novo deveria validar: %v", err)
	}
}

func TestIPCRequestLimiterRateAndInFlight(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newIPCRequestLimiter()
	l.now = func() time.Time { return now }

	for i := 0; i < ipcMaxRequestsPerSecond; i++ {
		release, err := l.begin()
		if err != nil {
			t.Fatalf("request %d deveria ser permitido: %v", i+1, err)
		}
		release() // libera o in-flight; a janela de 1s continua contando
	}
	if _, err := l.begin(); err != errIPCRateLimited {
		t.Fatalf("51º request em 1s deveria ser rate limited, got %v", err)
	}
	// Passada a janela, volta a permitir.
	now = now.Add(1100 * time.Millisecond)
	release, err := l.begin()
	if err != nil {
		t.Fatalf("após a janela deveria permitir: %v", err)
	}
	release()
}

func TestIPCRequestLimiterInFlight(t *testing.T) {
	l := newIPCRequestLimiter()
	releases := make([]func(), 0, ipcMaxRequestsInFlight)
	for i := 0; i < ipcMaxRequestsInFlight; i++ {
		release, err := l.begin()
		if err != nil {
			t.Fatalf("in-flight %d deveria ser permitido: %v", i+1, err)
		}
		releases = append(releases, release)
	}
	if _, err := l.begin(); err != errIPCInFlight {
		t.Fatalf("9º request em voo deveria ser recusado, got %v", err)
	}
	releases[0]()
	if release, err := l.begin(); err != nil {
		t.Fatalf("após liberar uma vaga deveria permitir: %v", err)
	} else {
		release()
	}
}

func TestIPCDedupCache(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	d := newIPCDedupCache()
	d.now = func() time.Time { return now }

	if _, dup := d.begin("cid-1"); dup {
		t.Fatal("primeiro cid não é duplicado")
	}
	if cached, dup := d.begin("cid-1"); !dup || cached != nil {
		t.Fatalf("cid em voo deveria ser duplicado sem resposta (cached nil), got dup=%t cached=%v", dup, cached)
	}
	response := map[string]any{"ok": true, "data": "resposta"}
	d.finish("cid-1", response)
	cached, dup := d.begin("cid-1")
	if !dup || cached == nil || cached["data"] != "resposta" {
		t.Fatalf("duplicado deveria devolver a resposta emitida, got dup=%t cached=%v", dup, cached)
	}
	now = now.Add(ipcDedupWindow + time.Second)
	if _, dup := d.begin("cid-1"); dup {
		t.Fatal("após 60s o cid não conta mais como duplicado")
	}
}

func TestIPCRemoteSessionAllowed(t *testing.T) {
	id := ipcAuthIdentity{SID: "s", PID: 42, SessionID: 3, Valid: true}
	if err := ipcRemoteSessionAllowed(id, true, true, map[string]any{}); err != nil {
		t.Fatalf("admin+token deveria permitir: %v", err)
	}
	if err := ipcRemoteSessionAllowed(id, false, true, nil); err != errIPCWrongChannel {
		t.Fatalf("canal de leitura deveria recusar: %v", err)
	}
	if err := ipcRemoteSessionAllowed(id, true, false, nil); err == nil {
		t.Fatal("sem token deveria recusar")
	}
	if err := ipcRemoteSessionAllowed(ipcAuthIdentity{}, true, true, nil); err == nil {
		t.Fatal("sem identidade validada deveria recusar")
	}
	if err := ipcRemoteSessionAllowed(id, true, true, map[string]any{"pid": float64(43)}); err != errIPCTokenBinding {
		t.Fatalf("pid divergente deveria recusar: %v", err)
	}
	if err := ipcRemoteSessionAllowed(id, true, true, map[string]any{"sessionId": float64(4)}); err != errIPCTokenBinding {
		t.Fatalf("sessão divergente deveria recusar: %v", err)
	}
	if err := ipcRemoteSessionAllowed(id, true, true, map[string]any{"pid": float64(42), "targetSessionId": float64(3)}); err != nil {
		t.Fatalf("pid/sessão iguais deveriam permitir: %v", err)
	}
	// sessionId textual (UUID da sessão remota) não é identidade Windows.
	if err := ipcRemoteSessionAllowed(id, true, true, map[string]any{"sessionId": "uuid-remoto"}); err != nil {
		t.Fatalf("sessionId textual não deveria ser tratado como sessão Windows: %v", err)
	}
}

func TestIPCNotificationTargetAllows(t *testing.T) {
	ipcNotificationTargets.mu.Lock()
	ipcNotificationTargets.entries = map[string]ipcNotificationTarget{}
	ipcNotificationTargets.mu.Unlock()

	target := ipcAuthIdentity{SID: "S-1-5-21-7", PID: 100, SessionID: 7, Valid: true}
	ipcRememberNotificationTarget("n-1", target, false)

	if !ipcNotificationTargetAllows("n-1", target) {
		t.Fatal("mesma sessão/SID deveria poder responder")
	}
	other := target
	other.SessionID = 8
	if ipcNotificationTargetAllows("n-1", other) {
		t.Fatal("outra sessão NÃO pode responder")
	}
	other = target
	other.SID = "S-1-5-21-8"
	if ipcNotificationTargetAllows("n-1", other) {
		t.Fatal("outro SID na mesma sessão NÃO pode responder")
	}
	system := ipcAuthIdentity{SID: "S-1-5-18", SessionID: 0, System: true, Valid: true}
	if !ipcNotificationTargetAllows("n-1", system) {
		t.Fatal("SYSTEM sempre pode responder")
	}
	if ipcNotificationTargetAllows("n-1", ipcAuthIdentity{SessionID: 7}) {
		t.Fatal("chamador sem identidade validada NÃO pode responder")
	}
	if ipcNotificationTargetAllows("inexistente", target) {
		t.Fatal("notificationId desconhecido NÃO pode ser respondido")
	}

	ipcRememberNotificationTarget("n-2", ipcAuthIdentity{}, true)
	if !ipcNotificationTargetAllows("n-2", target) {
		t.Fatal("fallback anySession com chamador autenticado deveria permitir")
	}
}

func TestHandleIPCRequestMutatingRequiresAuthContext(t *testing.T) {
	a := &App{}
	payload := map[string]any{"method": "debug:set", "config": map[string]any{}}
	resp := a.handleIPCRequest(context.Background(), payload)
	if resp["ok"] != false || resp["code"] != "ipc_unauthorized" || resp["error"] != "unauthorized" {
		t.Fatalf("mutação sem context autenticado deveria retornar unauthorized, got %v", resp)
	}
	// Com o contexto autenticado o gate deixa passar (e o guard de serviço
	// responde com erro de domínio, não de autorização).
	authCtx := withIPCAuth(context.Background(), ipcRequestAuth{Admin: true, ConnKey: "k", SID: "s", PID: 1, SessionID: 1})
	resp = a.handleIPCRequest(authCtx, map[string]any{"method": "debug:set", "config": map[string]any{}})
	if resp["code"] == "ipc_unauthorized" {
		t.Fatalf("context autenticado não deveria ser recusado pelo gate: %v", resp)
	}
}

// TestHandleIPCMessageRemoteSessionWithoutIPCServerIsDenied cobre o fail-closed
// do dispatch de remote_session (S2): o guard antigo "if a.ipcServer != nil"
// liberava o caminho INTEIRO quando a App ainda não tinha o servidor atribuído
// (janela real entre StartIPCServer — que já aceita e despacha — e
// "a.ipcServer = StartIPCServer(...)" no startup). Sem autorizador não há
// prova de identidade, então o comando tem de ser recusado e NUNCA spawnar
// worker.
func TestHandleIPCMessageRemoteSessionWithoutIPCServerIsDenied(t *testing.T) {
	a := &App{}
	before := ipcAuthStats.RemoteSessionDenied.Load()
	a.handleIPCMessage(nil, IPCMessage{
		Type: IPCMsgRemoteSession,
		Payload: map[string]any{
			"action":    "start",
			"sessionId": "sessao-remota-1",
			"authToken": "token-nao-deve-ser-injetado",
		},
	})
	if got := ipcAuthStats.RemoteSessionDenied.Load(); got != before+1 {
		t.Fatalf("remote_session sem servidor IPC deveria ser NEGADO (fail-closed): denied %d -> %d", before, got)
	}
}

// TestHandleIPCMessageNotificationRespondWithoutIPCServerIsDenied garante que a
// resposta de prompt também é negada sem servidor IPC (não há como provar a
// sessão destinatária).
func TestHandleIPCMessageNotificationRespondWithoutIPCServerIsDenied(t *testing.T) {
	a := &App{}
	// NotificationSvc nil faria handleIPCNotificationRespond devolver false de
	// qualquer forma; o que se verifica aqui é que o gate de autorização roda
	// antes e não há panic (fail-closed, sem nil deref).
	a.handleIPCMessage(nil, IPCMessage{
		Type:    IPCMsgNotificationRespond,
		Payload: map[string]any{"notificationId": "n-1", "result": "approved"},
	})
}

func TestWithIPCAuthRoundTrip(t *testing.T) {
	if _, ok := ipcAuthFromContext(context.Background()); ok {
		t.Fatal("context vazio não deveria ter auth")
	}
	auth := ipcRequestAuth{ConnKey: "k", SID: "s", PID: 7, SessionID: 2, Admin: true}
	got, ok := ipcAuthFromContext(withIPCAuth(context.Background(), auth))
	if !ok || got != auth {
		t.Fatalf("auth round-trip falhou: %+v ok=%t", got, ok)
	}
}
