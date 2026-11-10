package mcp

// ─────────────────────────────────────────────────────────────────────────────
// Testes da tool de LEITURA de sensores (get_sensors).
//
// O que precisa ficar provado:
//   - a tool está registrada com o nome e o schema corretos (4 parâmetros
//     OPCIONAIS — nenhum required);
//   - sem sensores → hasSensors=false COM mensagem, SEM erro (o caso que o
//     usuário citou explicitamente);
//   - com sensores → lista, lastResult e histórico; filtro por sensorType;
//     filtro por sensorId; historyLimit respeitado E com teto;
//   - includeHistory=false → sem histórico (mas com lastResult);
//   - sensorId inexistente → resposta vazia coerente, não erro;
//   - snapshot nil/zerado → não entra em panic;
//   - declaredStatus/declaredMessage/channels saem CRUS (nenhum limiar ou
//     severidade calculados pela tool);
//   - NÃO é mutante: não pede consentimento (ToolConsentFor == nil) e não
//     aparece em ipcKnownMutatingMethods (checado no pacote app).
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"discovery/app/core/automation"
)

// fakeSensorBridge é um AppBridge mínimo que devolve um snapshot fixo. Só o
// método de sensores é exercitado; o embedding nil cobre o resto (qualquer
// outra tool chamada aqui entraria em panic — e nenhuma entra).
type fakeSensorBridge struct {
	AppBridge
	snapshot automation.SensorDebugSnapshot
}

func (f fakeSensorBridge) SensorDebugSnapshotForMCP() automation.SensorDebugSnapshot {
	return f.snapshot
}

// sensorToolRegistry monta o registry com o snapshot dado.
func sensorToolRegistry(snapshot automation.SensorDebugSnapshot) *Registry {
	reg := NewRegistry()
	RegisterDiscoveryTools(reg, fakeSensorBridge{snapshot: snapshot})
	return reg
}

// callSensorTool chama get_sensors e devolve o payload como map (falha o teste
// se o resultado não for um mapa JSON).
func callSensorTool(t *testing.T, reg *Registry, args map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	result, err := reg.Call(context.Background(), "get_sensors", raw)
	if err != nil {
		t.Fatalf("get_sensors devolveu erro: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("resultado deveria ser map[string]any, veio %T", result)
	}
	return payload
}

// sensorViewFixture monta uma view de sensor com n amostras (MAIS RECENTES
// PRIMEIRO, como o núcleo entrega).
func sensorViewFixture(id, key string, sensorType automation.SensorType, n int) automation.SensorDebugView {
	results := make([]automation.SensorRunView, 0, n)
	for i := 0; i < n; i++ {
		duration := 100 + i
		results = append(results, automation.SensorRunView{
			RunID:           "run-" + id + "-" + string(rune('a'+i)),
			StartedAt:       "2026-01-0" + string(rune('1'+i)) + "T10:00:00Z",
			FinishedAt:      "2026-01-0" + string(rune('1'+i)) + "T10:00:01Z",
			DurationMs:      &duration,
			DeclaredStatus:  "Warning",
			DeclaredMessage: "uso alto declarado pelo sensor",
			Channels:        map[string]float64{"percent": float64(90 + i)},
		})
	}
	lastRun := "2026-01-01T10:00:00Z"
	nextRun := "2026-01-01T10:01:00Z"
	return automation.SensorDebugView{
		SensorID:        id,
		Key:             key,
		Name:            "Sensor " + key,
		SensorType:      sensorType,
		Target:          "C:",
		IntervalSeconds: 60,
		TimeoutSeconds:  30,
		ChannelCount:    1,
		LastRunAt:       &lastRun,
		NextRunAt:       &nextRun,
		Overdue:         false,
		LastResults:     results,
	}
}

// snapshotFixture monta um snapshot com dois sensores de tipos diferentes e
// amostras de sobra para exercitar historyLimit.
func snapshotFixture() automation.SensorDebugSnapshot {
	lastSync := "2026-01-01T09:59:00Z"
	return automation.SensorDebugSnapshot{
		AgentID:       "agent-123",
		GeneratedAt:   "2026-01-01T10:00:00Z",
		Engine:        automation.SensorEngineStatus{PolicyLoaded: true, LastSyncAt: &lastSync, MaxConcurrency: 4},
		HistoryWindow: 100,
		Sensors: []automation.SensorDebugView{
			sensorViewFixture("disco-1", "disk-c", automation.SensorTypeDiskSpace, 25),
			sensorViewFixture("cpu-1", "cpu-load", automation.SensorTypeCpu, 3),
		},
	}
}

// TestSensorToolRegisteredWithSchema garante o registro e o schema: todos os
// parâmetros são OPCIONAIS (a tool precisa funcionar sem nenhum argumento).
func TestSensorToolRegisteredWithSchema(t *testing.T) {
	reg := sensorToolRegistry(automation.SensorDebugSnapshot{})
	tool := reg.Find("get_sensors")
	if tool == nil {
		t.Fatal("tool get_sensors nao registrada")
	}
	want := map[string]string{
		"sensorId":       "string",
		"sensorType":     "string",
		"includeHistory": "boolean",
		"historyLimit":   "integer",
	}
	if len(tool.Params) != len(want) {
		t.Fatalf("get_sensors tem %d params, esperado %d", len(tool.Params), len(want))
	}
	for _, p := range tool.Params {
		expected, ok := want[p.Name]
		if !ok {
			t.Fatalf("parametro inesperado: %q", p.Name)
		}
		if p.Type != expected {
			t.Errorf("parametro %q tipo=%q, esperado %q", p.Name, p.Type, expected)
		}
		if p.Required {
			t.Errorf("parametro %q deveria ser OPCIONAL (tool de leitura)", p.Name)
		}
	}
	// O schema exposto ao LLM nao pode exigir nada: uma chamada sem argumentos
	// deve ser valida.
	schema := tool.InputSchema()
	if required, ok := schema["required"]; ok {
		t.Fatalf("get_sensors nao deveria ter required no schema, veio %v", required)
	}
}

// TestSensorToolDescriptionInstructsModel trava as instruções que a descrição
// precisa carregar: não afirmar severidade, dizer quando não há sensores e
// deixar claro que o histórico é amostra LOCAL.
func TestSensorToolDescriptionInstructsModel(t *testing.T) {
	reg := sensorToolRegistry(automation.SensorDebugSnapshot{})
	tool := reg.Find("get_sensors")
	desc := strings.ToLower(tool.Description)
	for _, trecho := range []string{"hassensors=false", "severidade", "amostra local", "somente leitura"} {
		if !strings.Contains(desc, trecho) {
			t.Errorf("descricao deveria conter %q: %q", trecho, tool.Description)
		}
	}
}

// TestSensorToolSemSensores é o caso que o usuário citou: sem sensor definido a
// tool responde hasSensors=false COM mensagem e SEM erro.
func TestSensorToolSemSensores(t *testing.T) {
	casos := map[string]automation.SensorDebugSnapshot{
		"snapshot_zerado": {},
		"sensores_nil": {
			GeneratedAt:   "2026-01-01T10:00:00Z",
			HistoryWindow: 100,
		},
		"sensores_vazio": {
			GeneratedAt:   "2026-01-01T10:00:00Z",
			HistoryWindow: 100,
			Sensors:       []automation.SensorDebugView{},
		},
		"motor_indisponivel": {
			GeneratedAt: "2026-01-01T10:00:00Z",
			Engine:      automation.SensorEngineStatus{LastError: "motor de sensores ainda nao esta pronto"},
		},
	}
	for nome, snapshot := range casos {
		t.Run(nome, func(t *testing.T) {
			payload := callSensorTool(t, sensorToolRegistry(snapshot), map[string]any{})
			if has, _ := payload["hasSensors"].(bool); has {
				t.Fatalf("hasSensors deveria ser false, veio %#v", payload["hasSensors"])
			}
			message, _ := payload["message"].(string)
			if !strings.Contains(message, "nao tem sensores definidos") {
				t.Fatalf("mensagem amigavel ausente: %q", message)
			}
			// sensors precisa ser lista (nunca null) para o modelo nao tratar nil.
			sensors, ok := payload["sensors"].([]any)
			if !ok {
				t.Fatalf("sensors deveria ser []any, veio %T", payload["sensors"])
			}
			if len(sensors) != 0 {
				t.Fatalf("sensors deveria estar vazio, veio %d", len(sensors))
			}
			if _, ok := payload["engine"].(map[string]any); !ok {
				t.Fatalf("engine deveria ser objeto, veio %T", payload["engine"])
			}
			if payload["historyWindow"] != 100 {
				t.Fatalf("historyWindow = %#v, esperado 100 (teto do ring)", payload["historyWindow"])
			}
		})
	}
}

// TestSensorToolMotorIndisponivelExplicaMotivo: quando o motor não subiu, a
// mensagem precisa carregar o motivo — é o que impede o modelo de afirmar
// "não há sensores" quando na verdade o motor ainda está inicializando.
func TestSensorToolMotorIndisponivelExplicaMotivo(t *testing.T) {
	snapshot := automation.SensorDebugSnapshot{
		Engine: automation.SensorEngineStatus{LastError: "motor de sensores ainda nao esta pronto (automacao em inicializacao)"},
	}
	message, _ := callSensorTool(t, sensorToolRegistry(snapshot), map[string]any{})["message"].(string)
	if !strings.Contains(message, "inicializacao") {
		t.Fatalf("mensagem deveria citar o motivo do motor indisponivel: %q", message)
	}
}

// TestSensorToolComSensores cobre o caso feliz: lista, contagem, engine,
// lastResult e histórico.
func TestSensorToolComSensores(t *testing.T) {
	payload := callSensorTool(t, sensorToolRegistry(snapshotFixture()), map[string]any{})

	if has, _ := payload["hasSensors"].(bool); !has {
		t.Fatalf("hasSensors deveria ser true: %#v", payload)
	}
	if payload["count"] != 2 {
		t.Fatalf("count = %#v, esperado 2", payload["count"])
	}
	if payload["agentId"] != "agent-123" {
		t.Fatalf("agentId = %#v", payload["agentId"])
	}
	if payload["generatedAt"] != "2026-01-01T10:00:00Z" {
		t.Fatalf("generatedAt = %#v", payload["generatedAt"])
	}
	if _, hasMessage := payload["message"]; hasMessage {
		t.Fatalf("resposta com sensores nao deveria trazer message: %#v", payload["message"])
	}

	sensors, _ := payload["sensors"].([]any)
	if len(sensors) != 2 {
		t.Fatalf("sensors = %d, esperado 2", len(sensors))
	}
	first, _ := sensors[0].(map[string]any)
	if first["sensorId"] != "disco-1" || first["sensorType"] != "DiskSpace" || first["target"] != "C:" {
		t.Fatalf("primeiro sensor inesperado: %#v", first)
	}
	if first["intervalSeconds"] != 60 {
		t.Fatalf("intervalSeconds = %#v", first["intervalSeconds"])
	}
	if first["lastRunAt"] != "2026-01-01T10:00:00Z" || first["nextRunAt"] != "2026-01-01T10:01:00Z" {
		t.Fatalf("agendamento inesperado: lastRunAt=%#v nextRunAt=%#v", first["lastRunAt"], first["nextRunAt"])
	}
	if overdue, _ := first["overdue"].(bool); overdue {
		t.Fatalf("overdue deveria ser false: %#v", first["overdue"])
	}

	// lastResult é a amostra MAIS RECENTE (results[0] do núcleo).
	lastResult, ok := first["lastResult"].(map[string]any)
	if !ok {
		t.Fatalf("lastResult ausente: %#v", first)
	}
	if lastResult["startedAt"] != "2026-01-01T10:00:00Z" {
		t.Fatalf("lastResult deveria ser a amostra mais recente: %#v", lastResult)
	}
	// Histórico default = 10 amostras por sensor.
	history, _ := first["history"].([]any)
	if len(history) != sensorToolDefaultHistoryLimit {
		t.Fatalf("history default = %d, esperado %d", len(history), sensorToolDefaultHistoryLimit)
	}
}

// TestSensorToolDeclaradoCru prova a regra central: a tool NÃO avalia limiar
// nem calcula severidade — declaredStatus/declaredMessage/channels saem como o
// sensor declarou, e as chaves existem mesmo vazias.
func TestSensorToolDeclaradoCru(t *testing.T) {
	duration := 42
	snapshot := automation.SensorDebugSnapshot{
		GeneratedAt:   "2026-01-01T10:00:00Z",
		HistoryWindow: 100,
		Sensors: []automation.SensorDebugView{{
			SensorID:   "s1",
			SensorType: automation.SensorTypeScript,
			// Sensor que NÃO declarou nada: os campos precisam sair vazios (e não
			// inventados como "Unknown"/"Critical").
			LastResults: []automation.SensorRunView{{
				StartedAt: "2026-01-01T10:00:00Z",
				DurationMs: &duration,
				Channels:  map[string]float64{"livre_gb": 12.5},
			}},
		}},
	}
	payload := callSensorTool(t, sensorToolRegistry(snapshot), map[string]any{})
	sensors, _ := payload["sensors"].([]any)
	item, _ := sensors[0].(map[string]any)
	lastResult, _ := item["lastResult"].(map[string]any)

	for _, chave := range []string{"declaredStatus", "declaredMessage", "channels", "error"} {
		if _, ok := lastResult[chave]; !ok {
			t.Fatalf("lastResult deveria conter %q mesmo vazio: %#v", chave, lastResult)
		}
	}
	if lastResult["declaredStatus"] != "" || lastResult["declaredMessage"] != "" || lastResult["error"] != "" {
		t.Fatalf("declarado deveria sair CRU (vazio), veio %#v", lastResult)
	}
	channels, ok := lastResult["channels"].(map[string]float64)
	if !ok {
		t.Fatalf("channels deveria ser map[string]float64, veio %T", lastResult["channels"])
	}
	if channels["livre_gb"] != 12.5 {
		t.Fatalf("canal deveria sair CRU: %#v", channels)
	}
	if lastResult["durationMs"] != 42 {
		t.Fatalf("durationMs = %#v, esperado 42", lastResult["durationMs"])
	}

	// A resposta não pode conter nenhum veredito calculado pela tool.
	encoded, _ := json.Marshal(payload)
	for _, proibido := range []string{"severity", "severidade", "threshold", "limiar", "evaluatedStatus"} {
		if strings.Contains(strings.ToLower(string(encoded)), proibido) {
			t.Fatalf("resposta nao pode conter %q (a tool nao avalia nada): %s", proibido, encoded)
		}
	}
}

// TestSensorToolFiltroPorTipo garante o filtro sensorType (case-insensitive).
func TestSensorToolFiltroPorTipo(t *testing.T) {
	reg := sensorToolRegistry(snapshotFixture())

	payload := callSensorTool(t, reg, map[string]any{"sensorType": "cpu"})
	if payload["count"] != 1 {
		t.Fatalf("count com filtro cpu = %#v, esperado 1", payload["count"])
	}
	sensors, _ := payload["sensors"].([]any)
	item, _ := sensors[0].(map[string]any)
	if item["sensorId"] != "cpu-1" {
		t.Fatalf("sensor filtrado inesperado: %#v", item)
	}

	// Nome exato do enum continua casando.
	if got := callSensorTool(t, reg, map[string]any{"sensorType": "DiskSpace"})["count"]; got != 1 {
		t.Fatalf("count com filtro DiskSpace = %#v, esperado 1", got)
	}

	// Tipo que não existe: recorte vazio, SEM erro, e a mensagem explica que o
	// agente TEM sensores (para o modelo não dizer "não há sensores").
	payload = callSensorTool(t, reg, map[string]any{"sensorType": "BitLocker"})
	if has, _ := payload["hasSensors"].(bool); has {
		t.Fatalf("filtro sem resultado deveria devolver hasSensors=false: %#v", payload)
	}
	message, _ := payload["message"].(string)
	if !strings.Contains(message, "TEM sensores definidos") || !strings.Contains(message, "sensorType=BitLocker") {
		t.Fatalf("mensagem deveria explicar o filtro sem resultados: %q", message)
	}
}

// TestSensorToolSensorID cobre o detalhamento de UM sensor pelo sensorId e o
// caso do id inexistente (resposta vazia coerente, NÃO erro).
func TestSensorToolSensorID(t *testing.T) {
	reg := sensorToolRegistry(snapshotFixture())

	payload := callSensorTool(t, reg, map[string]any{"sensorId": "cpu-1"})
	if payload["count"] != 1 {
		t.Fatalf("count por sensorId = %#v, esperado 1", payload["count"])
	}
	sensors, _ := payload["sensors"].([]any)
	item, _ := sensors[0].(map[string]any)
	if item["sensorId"] != "cpu-1" {
		t.Fatalf("sensor detalhado inesperado: %#v", item)
	}

	// A key legível também casa (conveniência para o modelo).
	payload = callSensorTool(t, reg, map[string]any{"sensorId": "disk-c"})
	if payload["count"] != 1 {
		t.Fatalf("count por key = %#v, esperado 1", payload["count"])
	}

	// Id inexistente: SEM erro, hasSensors=false e mensagem coerente.
	payload = callSensorTool(t, reg, map[string]any{"sensorId": "nao-existe"})
	if has, _ := payload["hasSensors"].(bool); has {
		t.Fatalf("id inexistente deveria devolver hasSensors=false: %#v", payload)
	}
	message, _ := payload["message"].(string)
	if !strings.Contains(message, "sensorId=nao-existe") || !strings.Contains(message, "TEM sensores definidos") {
		t.Fatalf("mensagem deveria citar o filtro: %q", message)
	}
	if sensors, _ := payload["sensors"].([]any); len(sensors) != 0 {
		t.Fatalf("sensors deveria estar vazio, veio %d", len(sensors))
	}
}

// TestSensorToolHistoryLimit garante que historyLimit é respeitado E limitado
// ao teto do ring local.
func TestSensorToolHistoryLimit(t *testing.T) {
	reg := sensorToolRegistry(snapshotFixture())

	casos := []struct {
		nome  string
		valor any
		want  int
	}{
		{"tres", 3, 3},
		{"um", 1, 1},
		{"acima_do_disponivel", 999, 25}, // o sensor disco-1 tem 25 amostras
		{"teto_do_ring", 100, 25},
		{"acima_do_teto", 100000, 25},
		{"zero_usa_default", 0, sensorToolDefaultHistoryLimit},
		{"negativo_usa_default", -5, sensorToolDefaultHistoryLimit},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			payload := callSensorTool(t, reg, map[string]any{"historyLimit": tc.valor})
			sensors, _ := payload["sensors"].([]any)
			item, _ := sensors[0].(map[string]any)
			history, _ := item["history"].([]any)
			if len(history) != tc.want {
				t.Fatalf("history com limit=%v = %d, esperado %d", tc.valor, len(history), tc.want)
			}
		})
	}
}

// TestSensorToolHistoryTetoAplicadoNaOpcao trava o corte no nível da opção: um
// historyLimit absurdo NÃO chega ao núcleo como pedido gigante.
func TestSensorToolHistoryTetoAplicadoNaOpcao(t *testing.T) {
	options := parseSensorToolOptions(map[string]any{"historyLimit": float64(1 << 30)})
	if options.HistoryLimit != sensorToolMaxHistoryLimit {
		t.Fatalf("historyLimit = %d, esperado teto %d", options.HistoryLimit, sensorToolMaxHistoryLimit)
	}
	// Aceita int e float64 (o unmarshal de JSON produz float64).
	if got := parseSensorToolOptions(map[string]any{"historyLimit": 7}).HistoryLimit; got != 7 {
		t.Fatalf("historyLimit int = %d, esperado 7", got)
	}
	if got := parseSensorToolOptions(map[string]any{}).HistoryLimit; got != sensorToolDefaultHistoryLimit {
		t.Fatalf("historyLimit default = %d, esperado %d", got, sensorToolDefaultHistoryLimit)
	}
}

// TestSensorToolIncludeHistoryFalse garante que includeHistory=false remove o
// histórico mas PRESERVA o lastResult (é a "visão só de configuração").
func TestSensorToolIncludeHistoryFalse(t *testing.T) {
	reg := sensorToolRegistry(snapshotFixture())

	for _, valor := range []any{false, "false"} {
		payload := callSensorTool(t, reg, map[string]any{"includeHistory": valor})
		sensors, _ := payload["sensors"].([]any)
		item, _ := sensors[0].(map[string]any)
		if _, temHistorico := item["history"]; temHistorico {
			t.Fatalf("includeHistory=%v nao deveria trazer history: %#v", valor, item)
		}
		if _, temLast := item["lastResult"].(map[string]any); !temLast {
			t.Fatalf("includeHistory=%v deveria preservar lastResult: %#v", valor, item)
		}
	}

	// includeHistory ausente = true (default): o histórico continua vindo.
	payload := callSensorTool(t, reg, map[string]any{})
	sensors, _ := payload["sensors"].([]any)
	item, _ := sensors[0].(map[string]any)
	if _, tem := item["history"].([]any); !tem {
		t.Fatalf("includeHistory ausente deveria manter o historico (default true): %#v", item)
	}
}

// TestSensorToolSnapshotNilNaoEntraEmPanic cobre o snapshot zerado/nil em todos
// os caminhos de parâmetro — incluindo entradas de tipo errado vindas do LLM.
func TestSensorToolSnapshotNilNaoEntraEmPanic(t *testing.T) {
	reg := sensorToolRegistry(automation.SensorDebugSnapshot{})
	args := []map[string]any{
		{},
		{"sensorId": "x"},
		{"sensorType": "Cpu"},
		{"includeHistory": false},
		{"historyLimit": 10},
		// Tipos errados: não podem derrubar a leitura.
		{"sensorId": 42, "sensorType": []any{"Cpu"}, "includeHistory": "talvez", "historyLimit": "dez"},
		{"sensorId": nil, "historyLimit": nil, "includeHistory": nil},
	}
	for i, a := range args {
		payload := callSensorTool(t, reg, a)
		if _, ok := payload["hasSensors"].(bool); !ok {
			t.Fatalf("args[%d]: hasSensors deveria ser bool: %#v", i, payload)
		}
	}

	// Direto na função pura, com snapshot zerado e opções zeradas.
	if payload := buildSensorToolResponse(automation.SensorDebugSnapshot{}, sensorToolOptions{}); payload == nil {
		t.Fatal("buildSensorToolResponse com snapshot zerado devolveu nil")
	}
	// Sensor com LastResults vazio (nunca coletou): sem lastResult, sem panic.
	semAmostra := automation.SensorDebugSnapshot{
		HistoryWindow: 100,
		Sensors:       []automation.SensorDebugView{{SensorID: "s1", SensorType: automation.SensorTypeCpu}},
	}
	payload := buildSensorToolResponse(semAmostra, parseSensorToolOptions(map[string]any{}))
	sensors, _ := payload["sensors"].([]any)
	item, _ := sensors[0].(map[string]any)
	if _, tem := item["lastResult"]; tem {
		t.Fatalf("sensor sem coleta nao deveria trazer lastResult: %#v", item)
	}
}

// TestSensorToolNaoEhMutante prova que a tool de leitura NÃO pede
// consentimento: o gate do chat usa ToolConsentFor, e nil significa "livre".
func TestSensorToolNaoEhMutante(t *testing.T) {
	for _, args := range []map[string]any{
		{}, {"sensorId": "x"}, {"sensorType": "Cpu"}, {"historyLimit": 5},
	} {
		if req := ToolConsentFor("get_sensors", args); req != nil {
			t.Fatalf("get_sensors NAO pode exigir consentimento (tool de leitura); veio %#v", req)
		}
	}
}

// TestSensorToolFiltraPreservandoOrdem garante que a ordem do snapshot é
// preservada no recorte (o modelo lê a lista como o painel mostra).
func TestSensorToolFiltraPreservandoOrdem(t *testing.T) {
	snapshot := snapshotFixture()
	sensors := filterSensorViews(snapshot.Sensors, sensorToolOptions{})
	ids := make([]string, 0, len(sensors))
	for _, s := range sensors {
		ids = append(ids, s.SensorID)
	}
	if !reflect.DeepEqual(ids, []string{"disco-1", "cpu-1"}) {
		t.Fatalf("ordem alterada: %v", ids)
	}
}
