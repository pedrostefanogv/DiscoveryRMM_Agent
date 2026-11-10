package mcp

// ─────────────────────────────────────────────────────────────────────────────
// Tool de LEITURA dos sensores do agente (MCP).
//
// Pedido do usuário: "inclua nos agentes um MCP para consultar os sensores e
// histórico dos mesmos se tiver algum sensor definido para o agent".
//
// O que ela responde: quais sensores ESTE agente tem, como está o agendamento
// (intervalo, próxima execução, atraso), qual foi a última amostra de cada um e
// o histórico recente.
//
// O que ela NÃO faz (de propósito):
//   - não avalia limiar, não calcula status nem severidade. Devolve
//     declaredStatus/declaredMessage/channels CRUS, exatamente como vieram da
//     coleta. O agente MEDE; o servidor DECIDE (SensorEvaluator no API). Se a
//     tool "traduzisse" isso, o chat passaria a afirmar severidade sem lastro.
//   - não coleta, não agenda e não muda nada: é somente leitura, não exige canal
//     admin e não entra em ipcKnownMutatingMethods.
//
// A coleta continua sendo do núcleo: o snapshot vem pronto de
// App.SensorDebugSnapshotForMCP() (que reaproveita SensorDebugSnapshotCore e o
// bridge companion), e o histórico é o RING LOCAL de 100 amostras mantido pelo
// motor — independente da outbox do servidor. A descrição da tool diz isso ao
// modelo para ele não vender amostra local como série histórica do servidor.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"strings"

	"discovery/app/core/automation"
)

// Nome da tool. Escolhido curto e coerente com o catálogo (snake_case, sem
// prefixo de família): o modelo chama get_sensors para "quais sensores este
// agente tem".
const sensorToolName = "get_sensors"

// Defaults e tetos de historyLimit.
const (
	// sensorToolDefaultHistoryLimit é quantas amostras por sensor a tool devolve
	// quando o modelo não pede um número.
	sensorToolDefaultHistoryLimit = 10
	// sensorToolMaxHistoryLimit é o teto DURO do parâmetro historyLimit. É o
	// mesmo teto do ring local (sensorDebugHistoryMax = 100 no núcleo): pedir
	// mais que isso só devolveria o que existe, mas o teto explícito evita que
	// um modelo curioso peça 1e9 e force o núcleo a montar um slice gigante.
	sensorToolMaxHistoryLimit = 100
)

// getSensorsDescription é a descrição exposta ao LLM. Ela carrega três
// instruções que o modelo precisa seguir:
//  1. dizer claramente quando NÃO há sensores (hasSensors=false), sem tratar
//     isso como erro nem inventar sensores;
//  2. NUNCA afirmar status/severidade além do declarado — o declarado é o que o
//     PRÓPRIO sensor/script informou, não um veredito;
//  3. não vender o histórico como série histórica do servidor: é amostra LOCAL
//     recente (ring de 100), independente do que o servidor tem.
const getSensorsDescription = "Consulta os SENSORES configurados para ESTE agente e o histórico recente de amostras. " +
	"Devolve: hasSensors; a lista de sensores (tipo, alvo, intervalo, última execução, próxima execução, overdue); " +
	"a ÚLTIMA amostra de cada um; e o histórico recente. " +
	"IMPORTANTE: se hasSensors=false, o agente NÃO tem sensores definidos — diga isso ao usuário com a mensagem devolvida e NÃO invente sensores nem trate como erro (é uma resposta legítima). " +
	"Os campos declaredStatus/declaredMessage/channels são o que o sensor DECLAROU na coleta, crus: NÃO avalie limiar, NÃO calcule severidade e NÃO afirme que algo está 'crítico'/'ok' além do que foi declarado. " +
	"O histórico é uma AMOSTRA LOCAL RECENTE (ring de até 100 amostras mantido pelo agente), NÃO a série histórica do servidor — deixe isso claro se o usuário pedir 'histórico'. " +
	"Ferramenta SOMENTE LEITURA: não coleta agora, não agenda e não altera configuração. " +
	"Use os filtros (sensorId, sensorType) para não despejar todos os sensores quando o usuário pergunta de um só."

// sensorToolOptions são os parâmetros já normalizados da tool.
type sensorToolOptions struct {
	// SensorID limita a resposta a UM sensor (comparado com o sensorId e, como
	// conveniência, com a key do sensor).
	SensorID string
	// SensorType filtra por tipo (comparação case-insensitive).
	SensorType string
	// IncludeHistory diz se as amostras recentes entram na resposta.
	IncludeHistory bool
	// HistoryLimit é quantas amostras por sensor devolver (já limitado ao teto).
	HistoryLimit int
}

// parseSensorToolOptions lê os argumentos da tool. TODOS são opcionais: uma
// chamada sem argumento nenhum devolve o retrato completo com histórico.
//
// A normalização é defensiva de propósito (valores de tipos inesperados não
// derrubam a leitura): o argumento vem do LLM, que erra tipo com frequência.
func parseSensorToolOptions(args map[string]any) sensorToolOptions {
	options := sensorToolOptions{
		SensorID:       strings.TrimSpace(optionalStringArg(args, "sensorId")),
		SensorType:     strings.TrimSpace(optionalStringArg(args, "sensorType")),
		IncludeHistory: true,
		HistoryLimit:   sensorToolDefaultHistoryLimit,
	}
	// includeHistory: default TRUE. Só desliga quando o modelo manda um valor
	// explicitamente falso — um argumento ausente NÃO pode ser lido como false
	// (era o jeito mais fácil de a tool "esquecer" o histórico sem querer).
	if _, present := args["includeHistory"]; present {
		options.IncludeHistory = optionalBoolArg(args, "includeHistory")
	}
	if limit := optionalIntArg(args, "historyLimit"); limit > 0 {
		options.HistoryLimit = limit
	}
	if options.HistoryLimit > sensorToolMaxHistoryLimit {
		options.HistoryLimit = sensorToolMaxHistoryLimit
	}
	if options.HistoryLimit <= 0 {
		options.HistoryLimit = sensorToolDefaultHistoryLimit
	}
	return options
}

// registerSensorTools registra a tool de consulta de sensores.
func registerSensorTools(reg *Registry, app AppBridge) {
	reg.Register(Tool{
		Name:        sensorToolName,
		Description: getSensorsDescription,
		Params: []ToolParam{
			{Name: "sensorId", Type: "string", Description: "Detalha UM sensor específico pelo sensorId (ou pela key). Omita para listar todos.", Required: false},
			{Name: "sensorType", Type: "string", Description: "Filtra por tipo de sensor: DiskSpace, Cpu, Memory, Uptime, Service, Process, Ping, TcpPort, HttpProbe, HttpThroughput, DnsLatency, Bandwidth, DiskIo, Script, EventLogRate, SmartStatus, CertificateExpiry, DefenderStatus, BitLocker, ADConsistency. Omita para todos.", Required: false},
			{Name: "includeHistory", Type: "boolean", Description: "Inclui as amostras recentes de cada sensor (padrão true). Use false para uma visão só da configuração/agendamento.", Required: false},
			{Name: "historyLimit", Type: "integer", Description: "Quantas amostras por sensor devolver (padrão 10, máximo 100 = teto do ring local).", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			// Nunca devolve erro por ausência de sensores: "não tenho sensores" é
			// uma resposta legítima (é o caso que o usuário citou). Só o snapshot
			// vem da App; a montagem/filtragem é pura e local.
			return buildSensorToolResponse(app.SensorDebugSnapshotForMCP(), parseSensorToolOptions(args)), nil
		},
	})
}

// buildSensorToolResponse monta a resposta da tool a partir do snapshot do
// motor. É PURA (não toca em App nem em I/O) para os testes exercitarem todos
// os recortes sem subir o motor de sensores.
//
// Contrato:
//
//	sem sensores → {"hasSensors": false, "message": "Este agente não tem sensores definidos.", "sensors": [], "engine": {...}}
//	com sensores → {"hasSensors": true, "agentId", "generatedAt", "engine", "count", "sensors": [...], "historyWindow"}
//
// NUNCA entra em pânico: snapshot zerado, lista nula, amostra nula ou campos com
// ponteiro nil são todos tratados.
func buildSensorToolResponse(snapshot automation.SensorDebugSnapshot, options sensorToolOptions) map[string]any {
	sensors := filterSensorViews(snapshot.Sensors, options)

	engine := map[string]any{"disabled": snapshot.Engine.Disabled, "policyLoaded": snapshot.Engine.PolicyLoaded}
	// O engine vai inteiro (sem os campos vazios) quando o núcleo preencheu algo
	// — é o que explica "por que não coletou" (backoff, último erro, fila).
	if engineView := sensorEngineView(snapshot.Engine); len(engineView) > 0 {
		engine = engineView
	}

	response := map[string]any{
		"hasSensors":    len(sensors) > 0,
		"engine":        engine,
		"sensors":       sensorViewsToJSON(sensors, options),
		"historyWindow": sensorToolHistoryWindow(snapshot),
	}
	if response["sensors"] == nil {
		response["sensors"] = []any{}
	}

	// Sem sensores: mensagem amigável e o MOTIVO quando o motor souber explicar.
	// Não devolvemos erro: é resposta legítima, e o modelo precisa poder dizer ao
	// usuário "este agente não tem sensores definidos".
	if !response["hasSensors"].(bool) {
		response["message"] = sensorToolEmptyMessage(snapshot, options)
		return response
	}

	response["agentId"] = strings.TrimSpace(snapshot.AgentID)
	response["generatedAt"] = strings.TrimSpace(snapshot.GeneratedAt)
	response["count"] = len(sensors)
	return response
}

// filterSensorViews aplica os filtros opcionais (sensorId/key e sensorType)
// preservando a ordem do snapshot. Um filtro que não casa com nada devolve lista
// vazia — o que a resposta traduz como hasSensors=false COM mensagem.
func filterSensorViews(sensors []automation.SensorDebugView, options sensorToolOptions) []automation.SensorDebugView {
	if len(sensors) == 0 {
		return nil
	}
	wantID := strings.ToLower(options.SensorID)
	wantType := strings.ToLower(options.SensorType)

	out := make([]automation.SensorDebugView, 0, len(sensors))
	for _, sensor := range sensors {
		if wantID != "" && !sensorMatchesID(sensor, wantID) {
			continue
		}
		if wantType != "" && !strings.EqualFold(string(sensor.SensorType), options.SensorType) {
			continue
		}
		out = append(out, sensor)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sensorMatchesID casa o filtro sensorId com o sensorId OU com a key do sensor.
// A key é o apelido legível ("ping-gw") que o operador vê no console — aceitá-la
// evita que o modelo tenha que descobrir o GUID só para detalhar um sensor.
// wantID já vem minúsculo.
func sensorMatchesID(sensor automation.SensorDebugView, wantID string) bool {
	if strings.EqualFold(strings.TrimSpace(sensor.SensorID), wantID) {
		return true
	}
	key := strings.TrimSpace(sensor.Key)
	return key != "" && strings.EqualFold(key, wantID)
}

// sensorViewsToJSON converte as views do núcleo no contrato da tool: cada sensor
// com os campos de configuração/agendamento, o lastResult e o histórico.
//
// Os campos declaredStatus/declaredMessage/channels são copiados CRUS — nenhuma
// normalização, nenhum limiar, nenhuma severidade.
func sensorViewsToJSON(sensors []automation.SensorDebugView, options sensorToolOptions) []any {
	if len(sensors) == 0 {
		return nil
	}
	out := make([]any, 0, len(sensors))
	for _, sensor := range sensors {
		item := map[string]any{
			"sensorId":        strings.TrimSpace(sensor.SensorID),
			"name":            sensor.Name,
			"sensorType":      string(sensor.SensorType),
			"target":          sensor.Target,
			"intervalSeconds": sensor.IntervalSeconds,
			"overdue":         sensor.Overdue,
		}
		// lastRunAt/nextRunAt são *string no núcleo porque nil significa "nunca
		// rodou" — diferente de "" e diferente de uma data zero. Desreferenciamos
		// aqui para o payload expor o INSTANTE (ou nada), e não um ponteiro: a
		// chave ausente é a informação "nunca aconteceu".
		if sensor.LastRunAt != nil && *sensor.LastRunAt != "" {
			item["lastRunAt"] = *sensor.LastRunAt
		}
		if sensor.NextRunAt != nil && *sensor.NextRunAt != "" {
			item["nextRunAt"] = *sensor.NextRunAt
		}
		if key := strings.TrimSpace(sensor.Key); key != "" {
			item["key"] = key
		}
		if sensor.TimeoutSeconds > 0 {
			item["timeoutSeconds"] = sensor.TimeoutSeconds
		}
		if sensor.ChannelCount > 0 {
			item["channelCount"] = sensor.ChannelCount
		}

		// lastResults vem do núcleo MAIS RECENTES PRIMEIRO. A primeira amostra é
		// o lastResult; as demais são o histórico — nessa ordem, sem reordenar.
		results := sensor.LastResults
		if len(results) > 0 {
			item["lastResult"] = sensorRunViewToJSON(results[0])
			history := results
			if options.IncludeHistory {
				history = limitSensorRuns(results, options.HistoryLimit)
			} else {
				history = nil
			}
			if len(history) > 0 {
				item["history"] = sensorRunsToJSON(history)
			}
		}
		out = append(out, item)
	}
	return out
}

// limitSensorRuns corta a lista de amostras no teto pedido.
func limitSensorRuns(runs []automation.SensorRunView, limit int) []automation.SensorRunView {
	if limit <= 0 || len(runs) <= limit {
		return runs
	}
	return runs[:limit]
}

// sensorRunsToJSON converte uma lista de amostras.
func sensorRunsToJSON(runs []automation.SensorRunView) []any {
	if len(runs) == 0 {
		return nil
	}
	out := make([]any, 0, len(runs))
	for _, run := range runs {
		out = append(out, sensorRunViewToJSON(run))
	}
	return out
}

// sensorRunViewToJSON converte UMA amostra no contrato da tool.
//
// Devolve SEMPRE as chaves declaradas (mesmo vazias) para o modelo não confundir
// "sensor não declarou status" com "campo não existe": declaredStatus vazio é
// informação — significa que o script não declarou nada, e o veredito é do
// servidor.
func sensorRunViewToJSON(run automation.SensorRunView) map[string]any {
	item := map[string]any{
		"startedAt":       run.StartedAt,
		"declaredStatus":  run.DeclaredStatus,
		"declaredMessage": run.DeclaredMessage,
		"channels":        run.Channels,
		"error":           run.Error,
	}
	if item["channels"] == nil {
		item["channels"] = map[string]float64{}
	}
	if run.FinishedAt != "" {
		item["finishedAt"] = run.FinishedAt
	}
	if run.DurationMs != nil {
		item["durationMs"] = *run.DurationMs
	}
	if run.TargetKey != "" {
		item["targetKey"] = run.TargetKey
	}
	return item
}

// sensorEngineView monta o estado do motor para a resposta.
//
// O núcleo preenche tudo; copiamos os campos que explicam o comportamento do
// motor (fila, backoff, sync, erro). NÃO calculamos nada.
func sensorEngineView(engine automation.SensorEngineStatus) map[string]any {
	view := map[string]any{
		"disabled":     engine.Disabled,
		"policyLoaded": engine.PolicyLoaded,
	}
	if engine.BackoffActive {
		view["backoffActive"] = true
	}
	if engine.BackoffUntil != nil && *engine.BackoffUntil != "" {
		view["backoffUntil"] = *engine.BackoffUntil
	}
	if engine.LastSyncAt != nil && *engine.LastSyncAt != "" {
		view["lastSyncAt"] = *engine.LastSyncAt
	}
	if engine.FingerprintShort != "" {
		view["fingerprintShort"] = engine.FingerprintShort
	}
	if engine.InFlight > 0 {
		view["inFlight"] = engine.InFlight
	}
	if engine.MaxConcurrency > 0 {
		view["maxConcurrency"] = engine.MaxConcurrency
	}
	if engine.PendingSamples > 0 {
		view["pendingSamples"] = engine.PendingSamples
	}
	if engine.DefaultIntervalSeconds > 0 {
		view["defaultIntervalSeconds"] = engine.DefaultIntervalSeconds
	}
	if engine.LastError != "" {
		view["lastError"] = engine.LastError
	}
	return view
}

// sensorToolHistoryWindow devolve o teto do ring local. O núcleo preenche
// HistoryWindow; quando vier zerado (snapshot de fallback), usamos o teto
// conhecido do contrato em vez de devolver 0, que sugeriria "sem histórico".
func sensorToolHistoryWindow(snapshot automation.SensorDebugSnapshot) int {
	if snapshot.HistoryWindow > 0 {
		return snapshot.HistoryWindow
	}
	return sensorToolMaxHistoryLimit
}

// sensorToolEmptyMessage explica POR QUE não há sensores na resposta.
//
// Distinguir os três casos é o que impede o modelo de afirmar "este agente não
// tem sensores" quando na verdade ele só não achou o filtro pedido:
//   - motor ainda subindo (Engine.LastError preenchido pelo núcleo/App);
//   - filtro que não casou com nada (há sensores, mas nenhum bate);
//   - agente realmente sem sensores definidos (caso pedido pelo usuário).
func sensorToolEmptyMessage(snapshot automation.SensorDebugSnapshot, options sensorToolOptions) string {
	// Filtro que não casou: o snapshot TEM sensores, o recorte é que ficou vazio.
	if len(snapshot.Sensors) > 0 {
		return sensorToolFilteredEmptyMessage(options)
	}
	if lastError := strings.TrimSpace(snapshot.Engine.LastError); lastError != "" {
		return "Este agente nao tem sensores definidos. " + lastError
	}
	return "Este agente nao tem sensores definidos."
}

// sensorToolFilteredEmptyMessage descreve o recorte vazio citando os filtros
// usados, para o modelo poder corrigir o filtro em vez de concluir "não há
// sensores".
func sensorToolFilteredEmptyMessage(options sensorToolOptions) string {
	filtros := make([]string, 0, 2)
	if options.SensorID != "" {
		filtros = append(filtros, "sensorId="+options.SensorID)
	}
	if options.SensorType != "" {
		filtros = append(filtros, "sensorType="+options.SensorType)
	}
	if len(filtros) == 0 {
		return "Este agente nao tem sensores definidos."
	}
	return "Nenhum sensor corresponde ao filtro (" + strings.Join(filtros, ", ") +
		"). Este agente TEM sensores definidos; remova ou ajuste o filtro para vê-los."
}
