//go:build windows

package remotesession

import (
	"encoding/json"
	"strings"
	"testing"

	"discovery/app/core/winregistry"
)

// newRegistrySessionForTest cria uma SessionRegistry SEM stream NATS.
//
// O handler e puro (nao usa natsStream), entao o decode/roteamento/classificacao
// de erro pode ser testado sem broker. E o equivalente exato de como
// SessionProcesses e testado: a logica de protocolo nao deve depender de NATS
// para ser verificavel.
func newRegistrySessionForTest() *SessionRegistry {
	return &SessionRegistry{sessionID: "sess-teste", stopCh: make(chan struct{}), doneCh: make(chan struct{})}
}

// decodeResponse desserializa a resposta do handler.
func decodeResponse(t *testing.T, raw []byte) registryResponse {
	t.Helper()
	var resp registryResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("resposta nao e JSON valido: %v (raw=%s)", err, raw)
	}
	return resp
}

// TestRegistrySession_RejectsUnknownVersion garante que uma versao de protocolo
// diferente e recusada ANTES de interpretar campos.
//
// E o que transforma uma incompatibilidade silenciosa entre console novo e
// agente antigo em um erro explicito que o operador entende.
func TestRegistrySession_RejectsUnknownVersion(t *testing.T) {
	sr := newRegistrySessionForTest()

	for _, version := range []int{0, 2, 99, -1} {
		req, _ := json.Marshal(registryRequest{
			Version:   version,
			RequestID: "req-1",
			Action:    ActionListKey,
			Hive:      "HKLM",
		})
		resp := decodeResponse(t, sr.handleRequest(req))

		if resp.Success {
			t.Errorf("versao %d deveria ser recusada", version)
		}
		if resp.ErrorKind != "version_unsupported" {
			t.Errorf("versao %d: errorKind = %q; queria version_unsupported", version, resp.ErrorKind)
		}
		// O requestId PRECISA voltar mesmo no erro: sem ele o viewer nao
		// correlaciona a falha com a requisicao que a originou.
		if resp.RequestID != "req-1" {
			t.Errorf("versao %d: requestId perdido na resposta de erro (=%q)", version, resp.RequestID)
		}
	}
}

// TestRegistrySession_RejectsUnknownAction confirma que uma acao desconhecida
// nao cai em default permissivo nem e "adivinhada".
func TestRegistrySession_RejectsUnknownAction(t *testing.T) {
	sr := newRegistrySessionForTest()

	for _, action := range []string{"", "deleteEverything", "LISTKEY", "exec", "format"} {
		req, _ := json.Marshal(registryRequest{
			Version:   registryProtocolVersion,
			RequestID: "req-x",
			Action:    action,
			Hive:      "HKLM",
			Path:      "SOFTWARE",
		})
		resp := decodeResponse(t, sr.handleRequest(req))

		if resp.Success {
			t.Errorf("acao %q deveria ser recusada", action)
		}
		if resp.ErrorKind != "unknown_action" {
			t.Errorf("acao %q: errorKind = %q; queria unknown_action", action, resp.ErrorKind)
		}
	}
}

// TestRegistrySession_EchoesRequestID garante a correlacao em TODOS os caminhos
// de erro que o viewer pode provocar sem tocar no registro.
func TestRegistrySession_EchoesRequestID(t *testing.T) {
	sr := newRegistrySessionForTest()

	cases := []struct {
		name string
		req  registryRequest
	}{
		{"hive invalido", registryRequest{Version: 1, RequestID: "r1", Action: ActionListKey, Hive: "HKPD"}},
		{"hive vazio", registryRequest{Version: 1, RequestID: "r2", Action: ActionListKey, Hive: ""}},
		{"acao desconhecida", registryRequest{Version: 1, RequestID: "r3", Action: "nope", Hive: "HKLM"}},
		{"tipo invalido no set", registryRequest{Version: 1, RequestID: "r4", Action: ActionSetValue, Hive: "HKLM", Path: "SOFTWARE\\X", Type: "REG_INVENTADO", Confirmed: true}},
		{"caminho com traversal", registryRequest{Version: 1, RequestID: "r5", Action: ActionListKey, Hive: "HKLM", Path: "SOFTWARE\\..\\SYSTEM"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.req)
			resp := decodeResponse(t, sr.handleRequest(raw))
			if resp.Success {
				t.Fatalf("deveria falhar")
			}
			if resp.RequestID != tc.req.RequestID {
				t.Errorf("requestId = %q; queria %q", resp.RequestID, tc.req.RequestID)
			}
			if resp.ErrorKind == "" {
				t.Error("erro sem errorKind; a UI precisa do codigo estavel, nao so do texto")
			}
		})
	}
}

// TestRegistrySession_InvalidJSON garante que payload corrompido nao derruba a
// sessao nem devolve sucesso.
func TestRegistrySession_InvalidJSON(t *testing.T) {
	sr := newRegistrySessionForTest()

	// "null" e um caso interessante: o json.Unmarshal aceita e deixa a struct
	// zerada (sem erro). O request resultante tem version=0, entao a recusa
	// acontece na checagem de versao — e nao em bad_request. Ambos sao recusas
	// corretas; o teste aceita qualquer uma, mas exige que NUNCA haja sucesso.
	for _, raw := range []string{"", "{", "nao-e-json", "[1,2,3]", "null"} {
		resp := decodeResponse(t, sr.handleRequest([]byte(raw)))
		if resp.Success {
			t.Errorf("payload %q deveria falhar", raw)
		}
		if resp.ErrorKind != "bad_request" && resp.ErrorKind != "version_unsupported" {
			t.Errorf("payload %q: errorKind = %q; queria bad_request ou version_unsupported", raw, resp.ErrorKind)
		}
	}
}

// TestRegistrySession_RealReadAgainstLiveRegistry faz uma leitura REAL no
// registro desta maquina.
//
// Os demais testes sao de logica pura (nao tocam o SO). Este existe para provar
// que a integracao com golang.org/x/sys/windows/registry funciona de ponta a
// ponta: o guard deixa passar, a chave abre, o valor e decodificado. Sem ele,
// um erro de API do pacote x/sys so apareceria em producao.
//
// Le HKLM\SOFTWARE, que existe em qualquer Windows e e leitura permitida.
func TestRegistrySession_RealReadAgainstLiveRegistry(t *testing.T) {
	if testing.Short() {
		t.Skip("leitura real no registro; pulado em -short")
	}
	sr := newRegistrySessionForTest()

	req, _ := json.Marshal(registryRequest{
		Version: registryProtocolVersion, RequestID: "live-1",
		Action: ActionListKey, Hive: "HKLM", Path: "SOFTWARE",
	})
	resp := decodeResponse(t, sr.handleRequest(req))

	if !resp.Success {
		// Em contextos com ACL muito restrita a leitura pode ser negada; isso
		// nao e falha do codigo. Mas a recusa NAO pode ser do guard.
		if resp.ErrorKind == "protected" {
			t.Fatalf("HKLM\\SOFTWARE nao e chave protegida e foi recusada: %s", resp.Error)
		}
		t.Skipf("leitura de HKLM\\SOFTWARE negada neste contexto: %s", resp.Error)
	}

	if resp.Page == nil {
		t.Fatal("resposta de listKey sem page")
	}
	if resp.Page.Hive != "HKLM" {
		t.Errorf("hive = %q; queria HKLM", resp.Page.Hive)
	}
	// HKLM\SOFTWARE sempre tem subchaves (Microsoft, Classes, Policies...).
	if len(resp.Page.Subkeys) == 0 {
		t.Error("HKLM\\SOFTWARE deveria ter subchaves")
	}
	// E a paginacao precisa reportar o total real.
	if resp.Page.SubkeyCount == 0 {
		t.Error("subkeyCount deveria ser > 0")
	}
	// O stat da chave raiz tambem precisa funcionar e reportar writable.
	statReq, _ := json.Marshal(registryRequest{
		Version: registryProtocolVersion, RequestID: "live-2",
		Action: ActionStatKey, Hive: "HKLM", Path: "SOFTWARE",
	})
	statResp := decodeResponse(t, sr.handleRequest(statReq))
	if !statResp.Success {
		t.Fatalf("statKey falhou: %s", statResp.Error)
	}
	if statResp.Key == nil {
		t.Fatal("statKey sem key info")
	}
	if !statResp.Key.Writable {
		t.Errorf("HKLM\\SOFTWARE deveria ser gravavel; motivo=%s", statResp.Key.DenyReason)
	}
}

// TestRegistrySession_ProtectedKeyNeverReachesOS e o teste de seguranca central.
//
// Uma leitura de HKLM\SAM precisa ser recusada pelo GUARD, com errorKind
// "protected" — e nao por acaso (erro de ACL do SO, chave inexistente). A
// diferenca importa: se o guard nao estivesse no caminho, o teste passaria
// "por sorte" na maquina de CI e falharia em producao com privilegio elevado.
func TestRegistrySession_ProtectedKeyNeverReachesOS(t *testing.T) {
	sr := newRegistrySessionForTest()

	protected := []registryRequest{
		{Version: 1, RequestID: "p1", Action: ActionListKey, Hive: "HKLM", Path: "SAM"},
		{Version: 1, RequestID: "p2", Action: ActionListKey, Hive: "HKLM", Path: "SECURITY"},
		{Version: 1, RequestID: "p3", Action: ActionGetValue, Hive: "HKLM", Path: "SECURITY\\Policy\\Secrets", Name: "x"},
		{Version: 1, RequestID: "p4", Action: ActionSetValue, Hive: "HKLM", Path: "SAM\\SAM\\Domains", Name: "v", Type: "REG_SZ", Data: "x", Confirmed: true},
		{Version: 1, RequestID: "p5", Action: ActionDeleteKey, Hive: "HKLM", Path: "SECURITY", Confirmed: true},
	}

	for _, req := range protected {
		raw, _ := json.Marshal(req)
		resp := decodeResponse(t, sr.handleRequest(raw))

		if resp.Success {
			t.Errorf("action=%s em %s\\%s deveria ser RECUSADA pelo guard", req.Action, req.Hive, req.Path)
		}
		if resp.ErrorKind != "protected" {
			t.Errorf("action=%s em %s\\%s: errorKind = %q; queria protected (o guard tem de decidir, nao o SO)",
				req.Action, req.Hive, req.Path, resp.ErrorKind)
		}
	}
}

// TestRegistrySession_RunKeysWriteRequiresPolicy cobre a decisao de produto:
// leitura de Run permitida, escrita recusada.
func TestRegistrySession_RunKeysWriteRequiresPolicy(t *testing.T) {
	sr := newRegistrySessionForTest()

	setReq := registryRequest{
		Version: 1, RequestID: "rk1", Action: ActionSetValue,
		Hive: "HKLM", Path: "SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run",
		Name: "Malicioso", Type: "REG_SZ", Data: "cmd.exe", Confirmed: true,
	}
	raw, _ := json.Marshal(setReq)
	resp := decodeResponse(t, sr.handleRequest(raw))
	if resp.Success {
		t.Error("escrita em Run deveria ser recusada mesmo com confirmed=true")
	}
	if resp.ErrorKind != "protected" {
		t.Errorf("errorKind = %q; queria protected", resp.ErrorKind)
	}
}

// TestRegistrySession_MutationWithoutConfirmation garante a barreira do agente.
//
// A UI e a API ja exigem confirmacao; repetir no agente e defesa em
// profundidade — um viewer adulterado que publique direto no NATS ainda nao
// consegue escrever.
func TestRegistrySession_MutationWithoutConfirmation(t *testing.T) {
	sr := newRegistrySessionForTest()

	mutations := []registryRequest{
		{Version: 1, RequestID: "m1", Action: ActionSetValue, Hive: "HKLM", Path: "SOFTWARE\\MinhaEmpresa", Name: "v", Type: "REG_SZ", Data: "x"},
		{Version: 1, RequestID: "m2", Action: ActionDeleteValue, Hive: "HKLM", Path: "SOFTWARE\\MinhaEmpresa", Name: "v"},
		{Version: 1, RequestID: "m3", Action: ActionCreateKey, Hive: "HKLM", Path: "SOFTWARE\\MinhaEmpresa\\Nova"},
		{Version: 1, RequestID: "m4", Action: ActionDeleteKey, Hive: "HKLM", Path: "SOFTWARE\\MinhaEmpresa\\Velha"},
		{Version: 1, RequestID: "m5", Action: ActionRenameKey, Hive: "HKLM", Path: "SOFTWARE\\MinhaEmpresa\\A", NewName: "B"},
	}

	for _, req := range mutations {
		raw, _ := json.Marshal(req)
		resp := decodeResponse(t, sr.handleRequest(raw))

		if resp.Success {
			t.Errorf("action=%s SEM confirmed deveria ser recusada", req.Action)
		}
		if resp.ErrorKind != "confirmation_required" {
			t.Errorf("action=%s: errorKind = %q; queria confirmation_required", req.Action, resp.ErrorKind)
		}
	}
}

// TestRegistrySession_HiveNormalization confirma que as grafias longas do
// Windows sao aceitas e canonizadas.
func TestRegistrySession_HiveNormalization(t *testing.T) {
	sr := newRegistrySessionForTest()

	// HKEY_LOCAL_MACHINE deve resolver para HKLM. A leitura em si pode falhar
	// por ACL dependendo do contexto, mas NAO pode falhar por hive invalido.
	for _, hiveName := range []string{"HKEY_LOCAL_MACHINE", "hklm", "HKLM"} {
		req, _ := json.Marshal(registryRequest{
			Version: 1, RequestID: "h1", Action: ActionListKey, Hive: hiveName, Path: "SOFTWARE",
		})
		resp := decodeResponse(t, sr.handleRequest(req))
		if resp.ErrorKind == "invalid_hive" {
			t.Errorf("hive %q deveria ser aceito e canonizado", hiveName)
		}
	}
}

// TestRegistryErrorClassification garante que cada sentinela vira um codigo
// estavel — a UI decide pelo codigo, nao pelo texto em portugues.
func TestRegistryErrorClassification(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{winregistry.ErrInvalidHive, "invalid_hive"},
		{winregistry.ErrInvalidPath, "invalid_path"},
		{winregistry.ErrProtectedKey, "protected"},
		// ACL do SO e politica do guard sao diagnosticos DIFERENTES: o primeiro
		// pede elevacao, o segundo e uma decisao que o operador nao contorna.
		{winregistry.ErrAccessDenied, "access_denied"},
		{winregistry.ErrKeyNotEmpty, "key_not_empty"},
		{winregistry.ErrKeyNotFound, "not_found"},
		{winregistry.ErrValueNotFound, "value_not_found"},
		{winregistry.ErrUnsupportedType, "invalid_type"},
		{winregistry.ErrInvalidData, "invalid_data"},
		{winregistry.ErrConfirmationRequired, "confirmation_required"},
		{winregistry.ErrLimitExceeded, "limit_exceeded"},
	}
	for _, tc := range cases {
		if got := classifyRegistryError(tc.err); got != tc.want {
			t.Errorf("classifyRegistryError(%v) = %q; queria %q", tc.err, got, tc.want)
		}
	}
	if got := classifyRegistryError(nil); got != "" {
		t.Errorf("classifyRegistryError(nil) = %q; queria vazio", got)
	}
	if got := classifyRegistryError(errUnmapped); got != "internal" {
		t.Errorf("erro desconhecido = %q; queria internal", got)
	}

	// Erros embrulhados com %w tambem devem ser classificados: e assim que o
	// guard devolve a recusa (com o motivo anexado).
	wrapped := wrapErr(winregistry.ErrProtectedKey, "motivo da regra")
	if got := classifyRegistryError(wrapped); got != "protected" {
		t.Errorf("erro embrulhado = %q; queria protected", got)
	}
}

// TestRegistrySession_HiveNote: HKCU informa o usuario conectado (quando ha)
// ou o fallback SYSTEM (quando nao ha sessao ativa).
func TestRegistrySession_HiveNote(t *testing.T) {
	userNote := registryHiveNote(winregistry.HiveCurrentUser, &winregistry.UserContext{
		SID:      "S-1-5-21-1000-2000-3000-1001",
		Username: "pedro",
		Domain:   "WORKGROUP",
	})
	if !strings.Contains(userNote, "pedro") || !strings.Contains(userNote, "S-1-5-21-1000-2000-3000-1001") {
		t.Errorf("nota de HKCU com usuario deveria mencionar usuario e SID; veio: %q", userNote)
	}

	fallbackNote := registryHiveNote(winregistry.HiveCurrentUser, nil)
	if fallbackNote == "" {
		t.Fatal("HKCU sem usuario deveria trazer nota explicando o fallback SYSTEM")
	}
	if !strings.Contains(fallbackNote, "SYSTEM") {
		t.Errorf("nota de HKCU sem usuario deveria mencionar que a chave e do SYSTEM; veio: %q", fallbackNote)
	}

	// Hives de maquina nao precisam de nota.
	if got := registryHiveNote(winregistry.HiveLocalMachine, nil); got != "" {
		t.Errorf("HKLM nao deveria trazer nota; veio: %q", got)
	}
}
