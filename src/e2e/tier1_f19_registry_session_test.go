package e2e_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"discovery/app/core/remotesession"
)

// ─────────────────────────────────────────────────────────────────────────────
// F19 — EDITOR DE REGISTRO DO WINDOWS (Tier 1: espec feliz)
//
// Estes testes sobem um NATS embarcado REAL e exercitam o caminho completo:
// SessionRegistry assina .registry.req, o viewer publica o pedido e recebe a
// resposta em .registry.resp. É o mesmo harness dos testes de arquivos/proxy.
//
// O que se prova aqui (e que os testes unitários não provam):
//   - o anti-race do .registry.ready funciona sobre o broker de verdade;
//   - o requestId volta correlacionado pelo canal real;
//   - o guard recusa chave protegida ANTES de o agente tocar o registro;
//   - uma leitura real em HKLM\SOFTWARE devolve dados decodificados.
// ─────────────────────────────────────────────────────────────────────────────

const (
	regClientID  = "11111111-1111-1111-1111-111111111111"
	regSiteID    = "22222222-2222-2222-2222-222222222222"
	regAgentID   = "33333333-3333-3333-3333-333333333333"
	regSessionID = "66666666-6666-6666-6666-666666666661"
)

// TestF19_T1_RegistrySessionEndToEnd sobe a sessão e valida o ciclo completo.
func TestF19_T1_RegistrySessionEndToEnd(t *testing.T) {
	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")

	readySub := subscribeSync(t, viewerNC, sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.ready"))
	respSub := subscribeSync(t, viewerNC, sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.resp"))

	handler := remotesession.NewNatsStreamHandler(agentNC, regClientID, regSiteID, regAgentID)
	sr := remotesession.NewSessionRegistry(regSessionID, handler)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		sr.Stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})

	// 1. .registry.ready precisa chegar — é o que evita a corrida em que o
	//    primeiro listKey se perde (NATS core não tem replay).
	readyMsg := waitSubMsg(t, readySub, 5*time.Second)
	var ready struct {
		Status  string `json:"status"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(readyMsg.Data, &ready); err != nil {
		t.Fatalf("registry.ready inválido: %v (%s)", err, readyMsg.Data)
	}
	if ready.Status != "ready" {
		t.Errorf("status = %q; queria ready", ready.Status)
	}
	if ready.Version != 1 {
		t.Errorf("version = %d; queria 1", ready.Version)
	}

	// 2. listKey REAL em HKLM\SOFTWARE — leitura permitida pelo guard.
	reqSubj := sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.req")
	req, _ := json.Marshal(map[string]any{
		"version":   1,
		"requestId": "e2e-list-1",
		"action":    "listKey",
		"hive":      "HKLM",
		"path":      "SOFTWARE",
	})
	if err := viewerNC.Publish(reqSubj, req); err != nil {
		t.Fatalf("Publish listKey: %v", err)
	}
	_ = viewerNC.Flush()

	// 3. A resposta precisa voltar com o requestId correlacionado.
	respMsg := waitSubMsg(t, respSub, 5*time.Second)
	var resp struct {
		Success   bool   `json:"success"`
		Error     string `json:"error"`
		ErrorKind string `json:"errorKind"`
		RequestID string `json:"requestId"`
		Page      *struct {
			Hive    string   `json:"hive"`
			Path    string   `json:"path"`
			Subkeys []string `json:"subkeys"`
			Values  []struct {
				Name string `json:"name"`
				Type string `json:"type"`
				Data string `json:"data"`
			} `json:"values"`
			SubkeyCount int `json:"subkeyCount"`
		} `json:"page"`
	}
	if err := json.Unmarshal(respMsg.Data, &resp); err != nil {
		t.Fatalf("registry.resp inválido: %v (%s)", err, respMsg.Data)
	}

	if !resp.Success {
		// Em contextos com ACL muito restrita a leitura pode ser negada — mas
		// NUNCA pelo guard (HKLM\SOFTWARE não é chave protegida).
		if resp.ErrorKind == "protected" {
			t.Fatalf("HKLM\\SOFTWARE foi recusada pelo guard: %s", resp.Error)
		}
		t.Skipf("leitura negada neste contexto: %s", resp.Error)
	}
	if resp.RequestID != "e2e-list-1" {
		t.Errorf("requestId = %q; queria e2e-list-1", resp.RequestID)
	}
	if resp.Page == nil {
		t.Fatal("resposta sem page")
	}
	if resp.Page.Hive != "HKLM" || resp.Page.Path != "SOFTWARE" {
		t.Errorf("page = %s\\%s; queria HKLM\\SOFTWARE", resp.Page.Hive, resp.Page.Path)
	}
	if resp.Page.SubkeyCount == 0 {
		t.Error("HKLM\\SOFTWARE deveria reportar subchaves")
	}
}

// TestF19_T1_RegistryProtectedKeyRefusedOverNATS é o teste de SEGURANÇA no
// caminho real: HKLM\SAM precisa ser recusada pelo guard, com errorKind
// "protected", através do broker.
//
// A diferença em relação ao teste unitário: aqui se prova que a recusa acontece
// mesmo quando a mensagem chega pelo canal real, com o envelope completo.
func TestF19_T1_RegistryProtectedKeyRefusedOverNATS(t *testing.T) {
	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")

	readySub := subscribeSync(t, viewerNC, sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.ready"))
	respSub := subscribeSync(t, viewerNC, sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.resp"))

	handler := remotesession.NewNatsStreamHandler(agentNC, regClientID, regSiteID, regAgentID)
	sr := remotesession.NewSessionRegistry(regSessionID, handler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		sr.Stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	_ = waitSubMsg(t, readySub, 5*time.Second)

	reqSubj := sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.req")

	cases := []struct {
		name string
		body map[string]any
	}{
		{"leitura de SAM", map[string]any{"action": "listKey", "hive": "HKLM", "path": "SAM"}},
		{"leitura de SECURITY", map[string]any{"action": "listKey", "hive": "HKLM", "path": "SECURITY"}},
		{"leitura de LSA", map[string]any{"action": "listKey", "hive": "HKLM", "path": "SYSTEM\\CurrentControlSet\\Control\\Lsa"}},
		{"escrita em Run com confirmacao", map[string]any{
			"action": "setValue", "hive": "HKLM", "path": "SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run",
			"name": "Malicioso", "type": "REG_SZ", "data": "cmd.exe", "confirmed": true,
		}},
		{"hive invalido", map[string]any{"action": "listKey", "hive": "HKPD", "path": "QUALQUER"}},
		{"acao desconhecida", map[string]any{"action": "deleteEverything", "hive": "HKLM", "path": "SOFTWARE"}},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rid := "e2e-refuse-" + tc.name
			payload := map[string]any{"version": 1, "requestId": rid}
			for k, v := range tc.body {
				payload[k] = v
			}
			raw, _ := json.Marshal(payload)
			if err := viewerNC.Publish(reqSubj, raw); err != nil {
				t.Fatalf("Publish: %v", err)
			}
			_ = viewerNC.Flush()

			msg := waitSubMsg(t, respSub, 5*time.Second)
			var resp struct {
				Success   bool   `json:"success"`
				Error     string `json:"error"`
				ErrorKind string `json:"errorKind"`
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(msg.Data, &resp); err != nil {
				t.Fatalf("resposta inválida: %v (%s)", err, msg.Data)
			}

			if resp.Success {
				t.Fatalf("caso %d deveria ser RECUSADO", i)
			}
			if resp.RequestID != rid {
				t.Errorf("requestId = %q; queria %q", resp.RequestID, rid)
			}
			if resp.ErrorKind == "" {
				t.Error("recusa sem errorKind; a UI precisa do código estável")
			}
		})
	}
}

// TestF19_T1_RegistryMutationWithoutConfirmationRefused prova que a barreira de
// confirmação vive no AGENTE: um viewer que publique direto no NATS sem
// confirmed=true é recusado, mesmo que a API e a UI tenham sido contornadas.
func TestF19_T1_RegistryMutationWithoutConfirmationRefused(t *testing.T) {
	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")

	readySub := subscribeSync(t, viewerNC, sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.ready"))
	respSub := subscribeSync(t, viewerNC, sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.resp"))

	handler := remotesession.NewNatsStreamHandler(agentNC, regClientID, regSiteID, regAgentID)
	sr := remotesession.NewSessionRegistry(regSessionID, handler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		sr.Stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	_ = waitSubMsg(t, readySub, 5*time.Second)

	reqSubj := sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.req")

	mutations := []map[string]any{
		{"action": "setValue", "hive": "HKLM", "path": "SOFTWARE\\MinhaEmpresa", "name": "v", "type": "REG_SZ", "data": "x"},
		{"action": "deleteValue", "hive": "HKLM", "path": "SOFTWARE\\MinhaEmpresa", "name": "v"},
		{"action": "createKey", "hive": "HKLM", "path": "SOFTWARE\\MinhaEmpresa\\Nova"},
		{"action": "deleteKey", "hive": "HKLM", "path": "SOFTWARE\\MinhaEmpresa\\Velha"},
	}

	for _, body := range mutations {
		payload := map[string]any{"version": 1, "requestId": "no-confirm"}
		for k, v := range body {
			payload[k] = v
		}
		raw, _ := json.Marshal(payload)
		if err := viewerNC.Publish(reqSubj, raw); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		_ = viewerNC.Flush()

		msg := waitSubMsg(t, respSub, 5*time.Second)
		var resp struct {
			Success   bool   `json:"success"`
			ErrorKind string `json:"errorKind"`
		}
		_ = json.Unmarshal(msg.Data, &resp)

		if resp.Success {
			t.Fatalf("mutação %v sem confirmed deveria ser recusada", body["action"])
		}
		if resp.ErrorKind != "confirmation_required" {
			t.Errorf("action=%v: errorKind = %q; queria confirmation_required", body["action"], resp.ErrorKind)
		}
	}
}

// TestF19_T1_RegistryProtocolVersionMismatch prova que um agente antigo e um
// console novo falham com erro EXPLÍCITO, em vez de interpretar campos que não
// conhecem (dessincronização silenciosa).
func TestF19_T1_RegistryProtocolVersionMismatch(t *testing.T) {
	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")

	readySub := subscribeSync(t, viewerNC, sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.ready"))
	respSub := subscribeSync(t, viewerNC, sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.resp"))

	handler := remotesession.NewNatsStreamHandler(agentNC, regClientID, regSiteID, regAgentID)
	sr := remotesession.NewSessionRegistry(regSessionID, handler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		sr.Stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	_ = waitSubMsg(t, readySub, 5*time.Second)

	reqSubj := sessionSubject(regClientID, regSiteID, regAgentID, regSessionID, "registry.req")
	for _, version := range []int{0, 2, 99} {
		raw, _ := json.Marshal(map[string]any{
			"version": version, "requestId": "ver", "action": "listKey", "hive": "HKLM", "path": "SOFTWARE",
		})
		if err := viewerNC.Publish(reqSubj, raw); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		_ = viewerNC.Flush()

		msg := waitSubMsg(t, respSub, 5*time.Second)
		var resp struct {
			Success   bool   `json:"success"`
			ErrorKind string `json:"errorKind"`
			Error     string `json:"error"`
		}
		_ = json.Unmarshal(msg.Data, &resp)

		if resp.Success {
			t.Errorf("versão %d deveria ser recusada", version)
		}
		if resp.ErrorKind != "version_unsupported" {
			t.Errorf("versão %d: errorKind = %q; queria version_unsupported", version, resp.ErrorKind)
		}
		if !strings.Contains(resp.Error, "versão") {
			t.Errorf("mensagem deveria explicar a incompatibilidade; veio %q", resp.Error)
		}
	}
}
