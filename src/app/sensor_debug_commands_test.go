package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"discovery/app/core/automation"
)

// ── Painel de debug de sensores: testes do contrato do bridge ───────────────

// TestSensorDebugNilAutomationSvc garante que os três métodos do painel não
// entram em pânico com o serviço de automação ausente e devolvem mensagem
// explicativa em PT-BR. O snapshot é o único que NÃO devolve erro: o painel
// precisa renderizar mesmo sem o serviço.
//
// A mensagem dos dois comandos precisa ser ACIONÁVEL (a corrida de boot não
// pode ser um beco sem saída): diz que a automação ainda está subindo e manda
// usar "Atualizar policy" — ver sensorDebugService.
func TestSensorDebugNilAutomationSvc(t *testing.T) {
	a := &App{}

	snapshot, err := a.GetSensorDebugSnapshot()
	if err != nil {
		t.Fatalf("GetSensorDebugSnapshot com AutomationSvc nil devolveu erro: %v", err)
	}
	if snapshot == nil {
		t.Fatal("GetSensorDebugSnapshot com AutomationSvc nil devolveu snapshot nil")
	}

	for _, tc := range []struct {
		nome string
		call func() (any, error)
	}{
		{"RunSensorsNowDebug", func() (any, error) { return a.RunSensorsNowDebug("") }},
		{"RefreshSensorPolicyDebug", func() (any, error) { return a.RefreshSensorPolicyDebug() }},
	} {
		result, err := tc.call()
		if err == nil {
			t.Fatalf("%s com AutomationSvc nil deveria devolver erro", tc.nome)
		}
		if result != nil {
			t.Fatalf("%s com AutomationSvc nil deveria devolver resultado nil (contrato (result, error)), veio %#v", tc.nome, result)
		}
		if !strings.Contains(err.Error(), "inicializacao") || !strings.Contains(err.Error(), "Atualizar policy") {
			t.Fatalf("%s: mensagem deveria ser acionavel (inicializacao + Atualizar policy): %v", tc.nome, err)
		}
	}
}

// TestSensorDebugSnapshotIndisponivelValido valida o snapshot de fallback: o
// painel sempre tem o que renderizar, com o motivo em Engine.LastError.
func TestSensorDebugSnapshotIndisponivelValido(t *testing.T) {
	a := &App{}
	a.RuntimeFlags.DebugMode = true
	// RPC mudo: exercita o fallback local (o retorno é o envelope da camada App).
	stubSensorDebugIPC(t, nil, false)

	snapshot, err := a.GetSensorDebugSnapshot()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	view, ok := snapshot.(sensorDebugSnapshotView)
	if !ok {
		t.Fatalf("tipo do snapshot inesperado: %T", snapshot)
	}
	if view.GeneratedAt == "" {
		t.Fatal("GeneratedAt deveria estar preenchido no snapshot de fallback")
	}
	if _, err := time.Parse(time.RFC3339, view.GeneratedAt); err != nil {
		t.Fatalf("GeneratedAt deveria estar em RFC3339: %q (%v)", view.GeneratedAt, err)
	}
	if view.Sensors == nil {
		t.Fatal("Sensors deveria ser não-nil no snapshot de fallback")
	}
	if !view.DebugMode {
		t.Fatal("DebugMode deveria refletir a.RuntimeFlags.DebugMode")
	}
	if !strings.Contains(view.Engine.LastError, "inicializacao") {
		t.Fatalf("Engine.LastError inesperado: %q", view.Engine.LastError)
	}
}

// TestSensorDebugParsePayload cobre as quatro formas toleradas de idsJSON e o
// lixo (erro claro em PT-BR, sem pânico).
func TestSensorDebugParsePayload(t *testing.T) {
	t.Run("vazio", func(t *testing.T) {
		ids, forceSync, err := parseSensorDebugRunPayload("")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if len(ids) != 0 || forceSync {
			t.Fatalf("esperado todos os sensores sem forceSync; ids=%v forceSync=%t", ids, forceSync)
		}
	})

	t.Run("array vazio", func(t *testing.T) {
		ids, forceSync, err := parseSensorDebugRunPayload("[]")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if len(ids) != 0 || forceSync {
			t.Fatalf("esperado todos os sensores sem forceSync; ids=%v forceSync=%t", ids, forceSync)
		}
	})

	t.Run("espacos em volta", func(t *testing.T) {
		ids, _, err := parseSensorDebugRunPayload("   []  ")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if len(ids) != 0 {
			t.Fatalf("esperado lista vazia; ids=%v", ids)
		}
	})

	t.Run("array de ids", func(t *testing.T) {
		ids, forceSync, err := parseSensorDebugRunPayload("[\"guid1\",\"guid2\"]")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if !reflect.DeepEqual(ids, []string{"guid1", "guid2"}) {
			t.Fatalf("ids inesperados: %v", ids)
		}
		if forceSync {
			t.Fatal("forceSync deveria ser false no array puro")
		}
	})

	t.Run("objeto com ids e forceSync", func(t *testing.T) {
		ids, forceSync, err := parseSensorDebugRunPayload("{\"ids\":[\"guid1\"],\"forceSync\":true}")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if !reflect.DeepEqual(ids, []string{"guid1"}) {
			t.Fatalf("ids inesperados: %v", ids)
		}
		if !forceSync {
			t.Fatal("forceSync deveria ser true")
		}
	})

	t.Run("objeto sem ids", func(t *testing.T) {
		ids, forceSync, err := parseSensorDebugRunPayload("{\"forceSync\":true}")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if len(ids) != 0 || !forceSync {
			t.Fatalf("esperado todos com forceSync; ids=%v forceSync=%t", ids, forceSync)
		}
	})

	t.Run("array com id em branco e ruido", func(t *testing.T) {
		ids, _, err := parseSensorDebugRunPayload("[\" guid1 \",\"\",\"   \"]")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if !reflect.DeepEqual(ids, []string{"guid1"}) {
			t.Fatalf("ids inesperados: %v", ids)
		}
	})

	invalidos := map[string]string{
		"texto solto":           "not-json",
		"numero":                "42",
		"array com numero":      "[1,2]",
		"array com objeto":      "[{\"id\":\"guid1\"}]",
		"objeto malformado":     "{\"ids\":[\"guid1\",}",
		"array malformado":      "[\"guid1\"",
		"booleano":              "true",
	}
	for nome, entrada := range invalidos {
		t.Run("invalido/"+nome, func(t *testing.T) {
			ids, forceSync, err := parseSensorDebugRunPayload(entrada)
			if err == nil {
				t.Fatalf("entrada %q deveria devolver erro (ids=%v forceSync=%t)", entrada, ids, forceSync)
			}
			if !strings.Contains(err.Error(), "idsJSON invalido") {
				t.Fatalf("mensagem de erro inesperada: %v", err)
			}
		})
	}
}

// TestSensorDebugIDsJSONLimite garante que um corpo absurdamente grande é
// rejeitado de forma limpa (o painel nunca manda isso; um cliente errado não
// pode fazer o agente alocar memória à toa).
func TestSensorDebugIDsJSONLimite(t *testing.T) {
	grande := strings.Repeat("a", sensorDebugJSONMaxBytes+1)
	if _, _, err := parseSensorDebugRunPayload(grande); err == nil {
		t.Fatal("payload acima do limite deveria devolver erro")
	}
}

// TestSensorDebugModoIndisponivelMensagem confere que o erro dos métodos de
// execução é exatamente o da convenção do agente.
func TestSensorDebugModoIndisponivelMensagem(t *testing.T) {
	a := &App{}
	for _, tc := range []struct {
		nome string
		call func() (any, error)
	}{
		{"RunSensorsNowDebug", func() (any, error) { return a.RunSensorsNowDebug("[]") }},
		{"RefreshSensorPolicyDebug", func() (any, error) { return a.RefreshSensorPolicyDebug() }},
	} {
		_, err := tc.call()
		if err == nil {
			t.Fatalf("%s: erro esperado com o servico ausente", tc.nome)
		}
		// Mensagem ACIONÁVEL: a corrida de boot nao pode ser um beco sem saida.
		if !strings.Contains(err.Error(), "inicializacao") || !strings.Contains(err.Error(), "Atualizar policy") {
			t.Fatalf("%s: erro inesperado: %v", tc.nome, err)
		}
	}
}

// TestSensorDebugMensagemAgendamento cobre a mensagem PT-BR, inclusive o caso
// "X de Y id(s) encontrados na policy local" (contract de agent_commands_sensors.go).
func TestSensorDebugMensagemAgendamento(t *testing.T) {
	if msg := sensorDebugScheduledMessage(3, 0); msg != "coleta agendada para 3 sensor(es)" {
		t.Fatalf("mensagem inesperada (todos): %q", msg)
	}
	msg := sensorDebugScheduledMessage(1, 2)
	if !strings.Contains(msg, "coleta agendada para 1 sensor(es)") {
		t.Fatalf("mensagem inesperada: %q", msg)
	}
	if !strings.Contains(msg, "1 de 2 id(s) encontrados na policy local") {
		t.Fatalf("mensagem deveria explicar os ids não encontrados: %q", msg)
	}
	if msg := sensorDebugScheduledMessage(2, 2); strings.Contains(msg, "encontrados na policy local") {
		t.Fatalf("mensagem não deveria citar contagem quando todos existem: %q", msg)
	}
}

// TestSensorDebugBridgeReflection trava o CONTRATO do servidor de debug
// (app/debug_http.go): ele descobre rotas por reflexão, então a assinatura dos
// métodos é a interface pública do painel.
func TestSensorDebugBridgeReflection(t *testing.T) {
	appType := reflect.TypeOf(&App{})

	casos := []struct {
		nome      string
		numIn     int
		primeiroIn reflect.Kind
	}{
		{"GetSensorDebugSnapshot", 0, reflect.Invalid},
		{"RefreshSensorPolicyDebug", 0, reflect.Invalid},
		{"RunSensorsNowDebug", 1, reflect.String},
	}
	for _, tc := range casos {
		m, ok := appType.MethodByName(tc.nome)
		if !ok {
			t.Fatalf("método %s não encontrado em *App (o bridge não o exporia)", tc.nome)
		}
		// m.Type inclui o RECEIVER como primeiro parâmetro (NumIn conta +1);
		// o bridge itera t.NumMethod()/t.Method(i), cujo Type também inclui o
		// receiver — por isso os dois lados usam a mesma contagem.
		params := m.Type.NumIn() - 1
		if params != tc.numIn {
			t.Fatalf("%s: %d parâmetro(s), esperado %d", tc.nome, params, tc.numIn)
		}
		if params == 1 && m.Type.In(1).Kind() != tc.primeiroIn {
			t.Fatalf("%s: parâmetro 1 tem Kind()=%s, esperado %s", tc.nome, m.Type.In(1).Kind(), tc.primeiroIn)
		}
		if m.Type.NumOut() != 2 {
			t.Fatalf("%s: NumOut()=%d, esperado 2 (contrato (result, error))", tc.nome, m.Type.NumOut())
		}
		// O bridge trata o 2º retorno como error (errVal.IsNil()).
		if !m.Type.Out(1).Implements(reflect.TypeOf((*error)(nil)).Elem()) {
			t.Fatalf("%s: Out(1)=%s não implementa error", tc.nome, m.Type.Out(1))
		}
	}
}

// ── Run-now: agendar ZERO sensores é resultado VÁLIDO, não erro ────────────
//
// Regressão do bug de produção: POST /api/RunSensorsNowDebug devolvia SEMPRE
// 500 com {"error":"nenhum sensor para executar"} — o caso mais comum do
// operador (painel aberto antes do primeiro sync da policy) caía no erro.

// newSensorDebugAppForRun monta um App real com um automation.Service real, mas
// com a policy local VAZIA (nenhum sensor aplicado) — é o estado do agente logo
// após o boot, antes do primeiro sync com o servidor.
//
// Não usa takeSensorBatch (ele consome a fila): a verificação do agendamento é
// feita pelo snapshot do motor (SensorDebugSnapshot), que é somente leitura.
func newSensorDebugAppForRun(t *testing.T) *App {
	t.Helper()
	a := &App{}
	a.AutomationSvc = automation.NewService(func() automation.RuntimeConfig {
		return automation.RuntimeConfig{AgentID: "agent-test"}
	}, nil)
	if a.AutomationSvc == nil {
		t.Fatal("automation.NewService devolveu nil")
	}
	return a
}

// sensorDebugScheduledFrom extrai o campo "scheduled" do resultado do bridge.
func sensorDebugScheduledFrom(t *testing.T, result any) int {
	t.Helper()
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("resultado deveria ser map[string]any, veio %T", result)
	}
	scheduled, ok := payload["scheduled"].(int)
	if !ok {
		t.Fatalf("campo scheduled ausente ou de tipo inesperado: %#v", payload["scheduled"])
	}
	return scheduled
}

// sensorDebugMessageFrom extrai a mensagem PT-BR devolvida ao painel.
func sensorDebugMessageFrom(t *testing.T, result any) string {
	t.Helper()
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("resultado deveria ser map[string]any, veio %T", result)
	}
	message, ok := payload["message"].(string)
	if !ok || strings.TrimSpace(message) == "" {
		t.Fatalf("campo message ausente ou vazio: %#v", payload)
	}
	return message
}

// TestSensorDebugRunNowPolicyVazia reproduz o 500 de produção: com a policy
// local vazia o run-now NÃO pode devolver erro — devolve scheduled=0 com
// mensagem PT-BR coerente (policy ainda não sincronizada).
func TestSensorDebugRunNowPolicyVazia(t *testing.T) {
	a := newSensorDebugAppForRun(t)

	result, err := a.RunSensorsNowDebug("[]")
	if err != nil {
		t.Fatalf("run-now com policy vazia devolveu erro (era o 500 de producao): %v", err)
	}
	if got := sensorDebugScheduledFrom(t, result); got != 0 {
		t.Fatalf("scheduled com policy vazia = %d, want 0", got)
	}
	message := sensorDebugMessageFrom(t, result)
	if !strings.Contains(message, "nenhum sensor agendado") || !strings.Contains(message, "policy local de sensores esta vazia") {
		t.Fatalf("mensagem deveria explicar a policy vazia: %q", message)
	}

	// Mesmos caminhos sem payload ("" => todos) e com forceSync: é o que o
	// operador faz ao clicar em "coletar agora" sem escolher sensor.
	for _, corpo := range []string{"", "{}", "{\"forceSync\":true}"} {
		result, err := a.RunSensorsNowDebug(corpo)
		if err != nil {
			t.Fatalf("run-now com policy vazia e corpo %q devolveu erro: %v", corpo, err)
		}
		if got := sensorDebugScheduledFrom(t, result); got != 0 {
			t.Fatalf("corpo %q: scheduled = %d, want 0", corpo, got)
		}
	}
}

// TestSensorDebugRunNowIDsInexistentes cobre o outro caso do 500: ids que não
// existem na policy local. Devolve scheduled=0 e a mensagem cita explicitamente
// os ids não encontrados — mensagem que antes era inalcançável, porque o núcleo
// devolvia erro antes de montá-la.
func TestSensorDebugRunNowIDsInexistentes(t *testing.T) {
	a := newSensorDebugAppForRun(t)

	result, err := a.RunSensorsNowDebug(`["inexistente-1","inexistente-2"]`)
	if err != nil {
		t.Fatalf("run-now com ids inexistentes devolveu erro: %v", err)
	}
	if got := sensorDebugScheduledFrom(t, result); got != 0 {
		t.Fatalf("scheduled com ids inexistentes = %d, want 0", got)
	}
	message := sensorDebugMessageFrom(t, result)
	if !strings.Contains(message, "0 de 2 id(s) encontrados na policy local") {
		t.Fatalf("mensagem deveria citar os ids nao encontrados: %q", message)
	}
}

// TestSensorDebugRunNowIDsValidos preserva o caso feliz: com ids que existem na
// policy local o agendamento continua contando os sensores marcados.
func TestSensorDebugRunNowIDsValidos(t *testing.T) {
	a := newSensorDebugAppForRun(t)
	aplicarPolicyDeTeste(t, a)

	const idValido = "11111111-1111-1111-1111-111111111111"

	t.Run("id_valido", func(t *testing.T) {
		result, err := a.RunSensorsNowDebug(`["11111111-1111-1111-1111-111111111111"]`)
		if err != nil {
			t.Fatalf("run-now com id valido devolveu erro: %v", err)
		}
		if got := sensorDebugScheduledFrom(t, result); got != 1 {
			t.Fatalf("scheduled com id valido = %d, want 1", got)
		}
		if msg := sensorDebugMessageFrom(t, result); !strings.Contains(msg, "coleta agendada para 1 sensor(es)") {
			t.Fatalf("mensagem inesperada: %q", msg)
		}
	})

	t.Run("todos_os_sensores", func(t *testing.T) {
		result, err := a.RunSensorsNowDebug("")
		if err != nil {
			t.Fatalf("run-now de todos devolveu erro: %v", err)
		}
		if got := sensorDebugScheduledFrom(t, result); got == 0 {
			t.Fatal("run-now de todos deveria agendar os sensores da policy local")
		}
	})

	t.Run("misto_valido_e_inexistente", func(t *testing.T) {
		result, err := a.RunSensorsNowDebug(`["11111111-1111-1111-1111-111111111111","nao-existe"]`)
		if err != nil {
			t.Fatalf("run-now misto devolveu erro: %v", err)
		}
		if got := sensorDebugScheduledFrom(t, result); got != 1 {
			t.Fatalf("scheduled no misto = %d, want 1", got)
		}
		if msg := sensorDebugMessageFrom(t, result); !strings.Contains(msg, "1 de 2 id(s) encontrados na policy local") {
			t.Fatalf("mensagem do misto inesperada: %q", msg)
		}
	})
}

// TestSensorDebugRunMessage cobre a tabela de mensagens do run-now em unidade:
// contagem > 0, ids não encontrados, policy vazia, policy em dia e contagem
// desconhecida.
func TestSensorDebugRunMessage(t *testing.T) {
	casos := []struct {
		nome      string
		count     int
		requested int
		known     int
		contem    []string
	}{
		{"agendou", 3, 0, 3, []string{"coleta agendada para 3 sensor(es)"}},
		{"ids_nao_encontrados", 0, 2, 3, []string{"nenhum sensor agendado", "0 de 2 id(s) encontrados na policy local"}},
		{"policy_vazia", 0, 0, 0, []string{"nenhum sensor agendado", "policy local de sensores esta vazia"}},
		{"tudo_em_dia", 0, 0, 4, []string{"nenhum sensor agendado", "ja estao em dia"}},
		{"contagem_desconhecida", 0, 0, -1, []string{"nenhum sensor agendado"}},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			message := sensorDebugRunMessage(tc.count, tc.requested, tc.known)
			for _, esperado := range tc.contem {
				if !strings.Contains(message, esperado) {
					t.Fatalf("mensagem %q nao contem %q", message, esperado)
				}
			}
		})
	}
}

// TestSensorDebugContainerCount garante que a contagem da policy local é -1
// (desconhecida) com o serviço ausente — nunca 0, que seria lido como policy
// vazia e produziria a mensagem errada.
func TestSensorDebugContainerCount(t *testing.T) {
	if got := sensorDebugContainerCount(nil); got != -1 {
		t.Fatalf("contagem com servico nil = %d, want -1", got)
	}
	a := newSensorDebugAppForRun(t)
	if got := sensorDebugContainerCount(a.AutomationSvc); got != 0 {
		t.Fatalf("contagem com policy nao sincronizada = %d, want 0", got)
	}
}

// aplicarPolicyDeTeste aplica na policy local um sensor válido, sem passar pela
// rede (o sync HTTP não é exercitado aqui).
func aplicarPolicyDeTeste(t *testing.T, a *App) {
	t.Helper()
	var response automation.SensorSyncResponse
	if err := json.Unmarshal([]byte(sensorDebugPolicyFixture), &response); err != nil {
		t.Fatalf("unmarshal da fixture de policy: %v", err)
	}
	a.AutomationSvc.SetSensorPolicyForTesting("agent-test", &response)
	if got := sensorDebugContainerCount(a.AutomationSvc); got != 1 {
		t.Fatalf("policy de teste deveria ter 1 sensor, veio %d", got)
	}
}

// sensorDebugPolicyFixture é uma policy com um sensor (o mesmo guid usado nos
// testes de agendamento do core) para o caso feliz do run-now.
const sensorDebugPolicyFixture = `{
  "agentId": "agent-test",
  "sensorFingerprint": "fp-debug-1",
  "defaultIntervalSeconds": 300,
  "sensors": [{
    "sensorId": "11111111-1111-1111-1111-111111111111",
    "key": "ping-gw",
    "name": "Ping gateway",
    "sensorType": "Ping",
    "target": "10.0.0.1",
    "intervalSeconds": 60,
    "timeoutSeconds": 30,
    "channels": [{"key": "latency_ms", "label": "Latencia", "isPrimary": true}]
  }]
}`

// ── Origem do snapshot: RPC do serviço vs. motor local (causa raiz M5) ─────
//
// O painel ficava SEMPRE vazio porque a UI é companion e não roda o loop de
// sensores: lia um AutomationSvc que existe mas nunca coleta. Estes testes
// fixam o roteamento novo — RPC primeiro, local como fallback.

// stubSensorDebugIPC troca o seam do RPC por um fake e restaura no cleanup.
func stubSensorDebugIPC(t *testing.T, resp map[string]any, ok bool) {
	t.Helper()
	anterior := sensorDebugIPCRequest
	t.Cleanup(func() { sensorDebugIPCRequest = anterior })
	sensorDebugIPCRequest = func(_ *App, method string, _ map[string]any) (map[string]any, bool) {
		if method != "sensors:debug_snapshot" {
			t.Fatalf("método RPC inesperado: %q", method)
		}
		return resp, ok
	}
}

// snapshotDoServico monta a resposta como ela chega do serviço: o snapshot
// viaja em data.snapshot como JSON bruto.
func snapshotDoServico(t *testing.T, snapshot automation.SensorDebugSnapshot) map[string]any {
	t.Helper()
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal do snapshot: %v", err)
	}
	return map[string]any{"ok": true, "data": map[string]any{"snapshot": json.RawMessage(raw)}}
}

// TestSensorDebugSnapshotVemDoServico é o teste da correção: com o RPC
// respondendo, o painel usa o snapshot DO SERVIÇO — não o motor local, que
// nesta App de teste está vazio.
func TestSensorDebugSnapshotVemDoServico(t *testing.T) {
	a := newSensorDebugAppForRun(t)

	doServico := automation.SensorDebugSnapshot{
		GeneratedAt: "2026-11-05T00:09:48Z",
		Engine: automation.SensorEngineStatus{
			PolicyLoaded: true,
			LastError:    "backoff de sync", // motivo legítimo do serviço
		},
		Sensors: []automation.SensorDebugView{{SensorID: "s1", Name: "Disco C"}},
	}
	stubSensorDebugIPC(t, snapshotDoServico(t, doServico), true)

	result, err := a.GetSensorDebugSnapshot()
	if err != nil {
		t.Fatalf("GetSensorDebugSnapshot não deve devolver erro: %v", err)
	}
	view, ok := result.(sensorDebugSnapshotView)
	if !ok {
		t.Fatalf("resultado deveria ser sensorDebugSnapshotView, veio %T", result)
	}
	if !view.FromService || view.Origin != sensorDebugOriginService {
		t.Fatalf("origem deveria ser o serviço: fromService=%t origin=%q", view.FromService, view.Origin)
	}
	if len(view.Sensors) != 1 || view.Sensors[0].SensorID != "s1" {
		t.Fatalf("snapshot deveria ser o do serviço, veio %#v", view.Sensors)
	}
	// O LastError do serviço NÃO pode ser trocado por texto local: pode estar
	// preenchido por um motivo legítimo (backoff de sync, falha de entrega).
	if view.Engine.LastError != "backoff de sync" {
		t.Fatalf("LastError do serviço foi sobrescrito: %q", view.Engine.LastError)
	}
}

// TestSensorDebugSnapshotFallbackLocal cobre standalone (ou RPC mudo): sem
// resposta do serviço vale o motor local — comportamento preservado.
func TestSensorDebugSnapshotFallbackLocal(t *testing.T) {
	a := newSensorDebugAppForRun(t)
	stubSensorDebugIPC(t, nil, false) // RPC não respondeu

	result, err := a.GetSensorDebugSnapshot()
	if err != nil {
		t.Fatalf("fallback local não deve devolver erro: %v", err)
	}
	view, ok := result.(sensorDebugSnapshotView)
	if !ok {
		t.Fatalf("resultado deveria ser sensorDebugSnapshotView, veio %T", result)
	}
	if view.FromService || view.Origin != sensorDebugOriginLocal {
		t.Fatalf("origem deveria ser local: fromService=%t origin=%q", view.FromService, view.Origin)
	}
}

// TestSensorDebugSnapshotNuncaFalhaComNenhumaVia garante o contrato do painel:
// sem serviço E sem RPC continua devolvendo snapshot VÁLIDO (nunca erro 500).
func TestSensorDebugSnapshotNuncaFalhaComNenhumaVia(t *testing.T) {
	caminhos := []struct {
		nome string
		ok   bool
	}{{"rpc_mudo_sem_motor_local", false}, {"rpc_responde_snapshot_vazio", true}}

	for _, c := range caminhos {
		t.Run(c.nome, func(t *testing.T) {
			a := &App{} // nem AutomationSvc nem ipcClient
			stubSensorDebugIPC(t, snapshotDoServico(t, automation.SensorDebugSnapshot{}), c.ok)
			result, err := a.GetSensorDebugSnapshot()
			if err != nil {
				t.Fatalf("nunca deve devolver erro: %v", err)
			}
			view, ok := result.(sensorDebugSnapshotView)
			if !ok {
				t.Fatalf("resultado deveria ser sensorDebugSnapshotView, veio %T", result)
			}
			if view.Sensors == nil {
				t.Fatal("Sensors deve ser não-nil para o painel nunca tratar null")
			}
			if view.GeneratedAt == "" {
				t.Fatal("GeneratedAt deve estar preenchido")
			}
		})
	}
}

// TestSensorDebugRPCLeituraNaoExigeAdmin trava a classificação do gate: o
// snapshot é LEITURA. Se alguém renomear para um sufixo mutante, isto avisa.
func TestSensorDebugRPCLeituraNaoExigeAdmin(t *testing.T) {
	if ipcRequestRequiresAdmin("sensors:debug_snapshot", nil) {
		t.Fatal("sensors:debug_snapshot é leitura e não deve exigir admin")
	}
}

// TestSensorDebugRPCRegistradoNoDispatch garante que o case existe no switch e
// que o gate não o recusa por engano (leitura pura).
func TestSensorDebugRPCRegistradoNoDispatch(t *testing.T) {
	a := newSensorDebugAppForRun(t)
	resp := a.handleIPCRequest(nil, map[string]any{"method": "sensors:debug_snapshot"})
	if code, _ := resp["code"].(string); code == "ipc_unauthorized" {
		t.Fatal("método de leitura não deveria ser recusado pelo gate de admin")
	}
	if msg, _ := resp["error"].(string); msg == "método desconhecido" {
		t.Fatal("sensors:debug_snapshot deve estar registrado no switch de handleIPCRequest")
	}
	if resp["ok"] != true {
		t.Fatalf("com motor presente o RPC deve responder ok=true, veio %#v", resp)
	}
	data, _ := resp["data"].(map[string]any)
	if data == nil || data["snapshot"] == nil {
		t.Fatalf("resposta deve trazer data.snapshot, veio %#v", resp)
	}
}
// ── Recusa honesta em modo companion (mutantes não roteados) ───────────────
//
// Em companion a UI não roda o loop de sensores (M5). Chamar o motor local
// devolveria "coleta agendada" sem coletar nada — pior que um erro, porque o
// operador acredita no sucesso. Estes testes fixam a recusa explícita.

// appCompanion marca a App como companion (ipcClient != nil) sem abrir pipe:
// os métodos mutantes só OLHAM o campo para decidir recusar.
func appCompanion() *App {
	return &App{ipcClient: &IPCClient{}}
}

func TestSensorDebugMutantesRecusamEmCompanion(t *testing.T) {
	a := appCompanion()
	a.AutomationSvc = newSensorDebugAppForRun(t).AutomationSvc

	casos := []struct {
		nome    string
		call    func() (any, error)
		palavra string
	}{
		{"run_now", func() (any, error) { return a.RunSensorsNowDebug("[]") }, "coleta imediata"},
		{"refresh_policy", func() (any, error) { return a.RefreshSensorPolicyDebug() }, "ressincronizacao de policy"},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			result, err := c.call()
			if err != nil {
				t.Fatalf("recusa nao deve ser erro de transporte (senao vira 500): %v", err)
			}
			payload, ok := result.(map[string]any)
			if !ok {
				t.Fatalf("resultado deveria ser map[string]any, veio %T", result)
			}
			if got := payload["scheduled"]; got != 0 {
				t.Fatalf("nao deveria agendar nada em companion: scheduled=%v", got)
			}
			if routed, _ := payload["routedToService"].(bool); routed {
				t.Fatal("routedToService deveria ser false (nada foi roteado nesta rodada)")
			}
			msg, _ := payload["message"].(string)
			if !strings.Contains(msg, "DiscoveryAgent") {
				t.Fatalf("mensagem deveria dizer que roda no servico: %q", msg)
			}
			if !strings.Contains(msg, c.palavra) {
				t.Fatalf("mensagem deveria citar %q: %q", c.palavra, msg)
			}
		})
	}
}

// Em standalone (sem ipcClient) o caminho local continua valendo: a recusa NAO
// pode vazar para quem roda o motor de verdade.
func TestSensorDebugMutantesSeguemLocaisEmStandalone(t *testing.T) {
	a := newSensorDebugAppForRun(t)
	if a.ipcClient != nil {
		t.Fatal("app de teste nao deveria ter ipcClient")
	}

	result, err := a.RunSensorsNowDebug("[]")
	if err != nil {
		t.Fatalf("standalone com policy vazia nao deve devolver erro: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("resultado deveria ser map[string]any, veio %T", result)
	}
	if _, temFlag := payload["routedToService"]; temFlag {
		t.Fatal("standalone nao deve emitir routedToService (isso e da recusa companion)")
	}
	msg, _ := payload["message"].(string)
	if strings.Contains(msg, "DiscoveryAgent") {
		t.Fatalf("standalone nao deveria recusar: %q", msg)
	}
}

