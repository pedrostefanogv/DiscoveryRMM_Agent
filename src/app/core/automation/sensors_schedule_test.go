package automation

import (
	"encoding/json"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Testes do contrato JSON (SensorDtos.cs do API) e do agendamento.
//
// O contrato é camelCase e serializa enums como STRING (JsonStringEnumConverter
// global do MVC) — mas o agente aceita também o valor numérico, porque um item
// com tipo numérico derrubaria o slice inteiro no unmarshal.
// ─────────────────────────────────────────────────────────────────────────────

const sensorSyncResponseFixture = `{` +
	`"upToDate": false,` +
	`"sensorFingerprint": "f1a2b3",` +
	`"generatedAt": "2026-11-03T12:00:00Z",` +
	`"sensorCount": 2,` +
	`"defaultIntervalSeconds": 300,` +
	`"sensors": [` +
	`{"sensorId": "11111111-1111-1111-1111-111111111111", "key": "disco-c", "name": "Disco C:",` +
	` "sensorType": "DiskSpace", "target": "C:", "intervalSeconds": 60, "timeoutSeconds": 30,` +
	` "optionsJson": "{\"suffix\":\"%\"}", "channels": [{"key": "free_percent", "label": "Livre", "unit": "Percent", "display": "gauge", "isPrimary": true}],` +
	` "extractorsJson": "[]"},` +
	`{"sensorId": "22222222-2222-2222-2222-222222222222", "name": "Script", "sensorType": 13,` +
	` "intervalSeconds": 0, "timeoutSeconds": 900, "channels": [{"key": "Download_MBPS"}],` +
	` "extractorsJson": "[{\"type\":\"json\",\"channel\":\"Download_MBPS\",\"jsonPath\":\"download.bandwidth\",\"scale\":8e-6}]",` +
	` "script": {"scriptId": "33333333-3333-3333-3333-333333333333", "name": "sensor.ps1", "version": "1.0.0", "scriptType": "PowerShell", "contentHashSha256": "abc", "content": "Write-Output 'MDZ_STATUS: OK'"}}` +
	`]` +
	`}`

func TestSensorSyncResponseUnmarshal(t *testing.T) {
	var response SensorSyncResponse
	if err := json.Unmarshal([]byte(sensorSyncResponseFixture), &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if response.SensorCount != 2 || len(response.Sensors) != 2 {
		t.Fatalf("sensors = %d (count %d), want 2", len(response.Sensors), response.SensorCount)
	}
	if response.Sensors[0].SensorType != SensorTypeDiskSpace {
		t.Fatalf("sensorType[0] = %q, want DiskSpace (enum como string)", response.Sensors[0].SensorType)
	}
	if response.Sensors[1].SensorType != SensorTypeScript {
		t.Fatalf("sensorType[1] = %q, want Script (enum como numero 13)", response.Sensors[1].SensorType)
	}
	if len(response.Sensors[0].Channels) != 1 || !response.Sensors[0].Channels[0].IsPrimary {
		t.Fatalf("channels[0] nao decodificados: %+v", response.Sensors[0].Channels)
	}
	if response.Sensors[1].Script == nil || response.Sensors[1].Script.ScriptType != ScriptPowerShell {
		t.Fatalf("script nao decodificado: %+v", response.Sensors[1].Script)
	}
	if response.Sensors[1].Script.Content == "" {
		t.Fatal("content do script deveria vir no sync com includeScriptContent=true")
	}
}

// TestBuildSensorConfigNormalizes prova os defaults do agendamento:
// intervalo 0 herda o default, intervalo < 30s sobe para 30s, timeout acima de
// 300s é cortado e canais declarados ficam em minúsculas.
func TestBuildSensorConfigNormalizes(t *testing.T) {
	var response SensorSyncResponse
	if err := json.Unmarshal([]byte(sensorSyncResponseFixture), &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	defaultInterval := 5 * time.Minute

	disk := buildSensorConfig(response.Sensors[0], defaultInterval)
	if disk.Interval != time.Minute {
		t.Fatalf("disco interval = %v, want 1m", disk.Interval)
	}
	if disk.Timeout != 30*time.Second {
		t.Fatalf("disco timeout = %v, want 30s", disk.Timeout)
	}
	if !disk.DeclaredChannels["free_percent"] {
		t.Fatalf("canal declarado nao registrado: %+v", disk.DeclaredChannels)
	}

	script := buildSensorConfig(response.Sensors[1], defaultInterval)
	if script.Interval != defaultInterval {
		t.Fatalf("script interval = %v, want default 5m", script.Interval)
	}
	if script.Timeout != sensorMaxTimeout {
		t.Fatalf("script timeout = %v, want teto de 300s", script.Timeout)
	}
	if !script.DeclaredChannels["download_mbps"] {
		t.Fatalf("canal declarado deveria ser normalizado: %+v", script.DeclaredChannels)
	}
	if len(script.Extractors) != 1 || script.Extractors[0].Scale != 8e-6 {
		t.Fatalf("extratores nao parseados: %+v", script.Extractors)
	}

	// Intervalo abaixo do mínimo aceito pelo servidor sobe para 30s.
	tight := buildSensorConfig(SensorPolicy{SensorID: "x", SensorType: SensorTypeCpu, IntervalSeconds: 5}, defaultInterval)
	if tight.Interval != sensorMinInterval {
		t.Fatalf("interval = %v, want 30s (minimo)", tight.Interval)
	}
}

// TestSensorDueSchedule cobre o agendamento puro.
func TestSensorDueSchedule(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	interval := 5 * time.Minute
	jitter := 10 * time.Second

	// Sensor novo/pendente roda no primeiro ciclo.
	if !sensorDue(now, time.Time{}, interval, jitter) {
		t.Fatal("sensor novo deveria estar devido")
	}

	lastRun := now.Add(-4 * time.Minute)
	if sensorDue(now, lastRun, interval, jitter) {
		t.Fatal("sensor nao deveria estar devido antes de interval+jitter")
	}

	lastRun = now.Add(-interval - jitter)
	if !sensorDue(now, lastRun, interval, jitter) {
		t.Fatal("sensor deveria estar devido em lastRun+interval+jitter")
	}

	if next := sensorNextRun(lastRun, interval, jitter); !next.Equal(now) {
		t.Fatalf("nextRun = %v, want %v", next, now)
	}
}

// TestSensorJitterDeterministic garante jitter estável (mesmo id = mesmo
// desvio) e dentro da janela de 0..29s.
func TestSensorJitterDeterministic(t *testing.T) {
	first := sensorJitterFor("11111111-1111-1111-1111-111111111111")
	second := sensorJitterFor("11111111-1111-1111-1111-111111111111")
	if first != second {
		t.Fatalf("jitter nao deterministico: %v != %v", first, second)
	}
	if first < 0 || first >= sensorJitterMax {
		t.Fatalf("jitter fora da janela: %v", first)
	}
	if zero := sensorJitterFor(""); zero != 0 {
		t.Fatalf("jitter de sensor sem id = %v, want 0", zero)
	}
}

// TestSensorMarkerKey formato exigido pela arquitetura.
func TestSensorMarkerKey(t *testing.T) {
	if got := sensorLastRunMarkerKey(" s1 ", " C: "); got != "sensor:lastrun:s1:C:" {
		t.Fatalf("marker key = %q", got)
	}
	if got := sensorLastRunMarkerKey("s1", ""); got != "sensor:lastrun:s1:" {
		t.Fatalf("marker key sem alvo = %q", got)
	}
}

// TestApplySensorPolicyAndRunNow cobre a troca de policy e o run-now.
func TestApplySensorPolicyAndRunNow(t *testing.T) {
	service := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-1"} }, func(string) {})

	var response SensorSyncResponse
	if err := json.Unmarshal([]byte(sensorSyncResponseFixture), &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	service.applySensorPolicy("agent-1", &response, false)

	sensors, _, disabled := service.sensorStateForTests()
	if len(sensors) != 2 {
		t.Fatalf("sensores aplicados = %d, want 2", len(sensors))
	}
	if disabled {
		t.Fatal("loop nao deveria estar desabilitado apos sync bem-sucedido")
	}

	// Run-now com id explícito marca só o sensor pedido.
	count, err := service.RunSensorsNow(t.Context(), []string{"11111111-1111-1111-1111-111111111111"}, false)
	if err != nil {
		t.Fatalf("RunSensorsNow: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	_, lastRun, _ := service.sensorStateForTests()
	key := sensorRunKey("11111111-1111-1111-1111-111111111111", "")
	if value, ok := lastRun[key]; !ok || !value.IsZero() {
		t.Fatalf("sensor pedido deveria estar devido (zero), got %v (presente=%t)", value, ok)
	}

	// Id inexistente não agenda nada.
	if _, err := service.RunSensorsNow(t.Context(), []string{"inexistente"}, false); err == nil {
		t.Fatal("run-now com id inexistente deveria falhar")
	}
}

// TestApplySensorPolicyUpToDateKeepsSensors garante que up-to-date NÃO zera o
// monitoramento (o servidor responde sem a lista quando o fingerprint confere).
func TestApplySensorPolicyUpToDateKeepsSensors(t *testing.T) {
	service := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-1"} }, func(string) {})

	var response SensorSyncResponse
	if err := json.Unmarshal([]byte(sensorSyncResponseFixture), &response); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	service.applySensorPolicy("agent-1", &response, false)
	service.applySensorPolicy("agent-1", &SensorSyncResponse{UpToDate: true, SensorFingerprint: response.SensorFingerprint}, false)

	sensors, _, _ := service.sensorStateForTests()
	if len(sensors) != 2 {
		t.Fatalf("sensores apos up-to-date = %d, want 2 (nao pode zerar)", len(sensors))
	}
}

// TestSensorResultBatchJSON cobre o envelope enviado ao servidor: camelCase e
// protocolo MDZ (declaredStatus/declaredMessage) no nível do resultado.
func TestSensorResultBatchJSON(t *testing.T) {
	batch := SensorResultBatch{
		AgentID:       "agent-1",
		CorrelationID: "corr-1",
		Samples: []SensorResult{{
			SensorID:        "11111111-1111-1111-1111-111111111111",
			TargetKey:       "C:",
			RunID:           "44444444-4444-4444-4444-444444444444",
			StartedAt:       "2026-11-03T12:00:00Z",
			FinishedAt:      "2026-11-03T12:00:01Z",
			DurationMs:      sensorInt(1000),
			Measure:         SensorMeasure{FreePercent: sensorFloat(12.5), TotalBytes: sensorFloat(100), FreeBytes: sensorFloat(12.5), ExitCode: sensorInt(20)},
			Channels:        map[string]float64{"free_percent": 12.5},
			DeclaredStatus:  "WARNING",
			DeclaredMessage: "disco cheio",
		}},
	}

	payload, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"agentId", "correlationId", "samples"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("envelope sem a chave camelCase %q: %s", key, payload)
		}
	}
	sample, ok := decoded["samples"].([]any)[0].(map[string]any)
	if !ok {
		t.Fatalf("sample invalido: %s", payload)
	}
	for _, key := range []string{"sensorId", "targetKey", "runId", "startedAt", "finishedAt", "durationMs", "measure", "channels", "declaredStatus", "declaredMessage"} {
		if _, ok := sample[key]; !ok {
			t.Fatalf("amostra sem a chave camelCase %q: %s", key, payload)
		}
	}
	measure, ok := sample["measure"].(map[string]any)
	if !ok {
		t.Fatalf("measure invalido: %s", payload)
	}
	for _, key := range []string{"freePercent", "totalBytes", "freeBytes", "exitCode"} {
		if _, ok := measure[key]; !ok {
			t.Fatalf("measure sem a chave camelCase %q: %s", key, payload)
		}
	}

	// Campos nil NÃO vão no payload (o servidor distingue ausente de zero).
	if _, ok := measure["usedPercent"]; ok {
		t.Fatalf("usedPercent nil nao deveria ser serializado: %s", payload)
	}
}
