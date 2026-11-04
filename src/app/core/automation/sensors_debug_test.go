package automation

import (
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Testes do retrato de diagnóstico (sensors_debug.go).
//
// Cobrem o que o painel depende: policy vazia não entra em pânico, script
// cacheado é distinguido de script ausente, "nunca rodou" devolve overdue com
// NextRunAt nulo (e não 0001-01-01), o ring é FIFO com teto, a fila é refletida
// e o histórico de cada sensor não vaza amostra de outro.
// ─────────────────────────────────────────────────────────────────────────────

// sensorDebugService cria o serviço de teste já com a policy aplicada.
func sensorDebugService(t *testing.T, response *SensorSyncResponse) *Service {
	t.Helper()
	service := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-debug"} }, func(string) {})
	if response != nil {
		service.applySensorPolicy("agent-debug", response, false)
	}
	return service
}

func sensorDebugPolicy(id, name string, intervalSeconds int) SensorPolicy {
	return SensorPolicy{
		SensorID:        id,
		Name:            name,
		SensorType:      SensorTypeDiskSpace,
		IntervalSeconds: intervalSeconds,
		TimeoutSeconds:  30,
		Channels:        []SensorChannel{{Key: "free_percent", IsPrimary: true}},
	}
}

// TestSensorDebugSnapshotEmptyPolicy: sem policy o retrato sai válido (nunca
// nil) e o serviço não pode entrar em pânico.
func TestSensorDebugSnapshotEmptyPolicy(t *testing.T) {
	service := sensorDebugService(t, nil)

	snapshot := service.SensorDebugSnapshot()
	if snapshot.Sensors == nil {
		t.Fatal("Sensors deveria ser slice vazio, nao nil")
	}
	if len(snapshot.Sensors) != 0 {
		t.Fatalf("sensores = %d, want 0", len(snapshot.Sensors))
	}
	if snapshot.AgentID != "agent-debug" {
		t.Fatalf("agentId = %q, want agent-debug", snapshot.AgentID)
	}
	if snapshot.GeneratedAt == "" {
		t.Fatal("generatedAt vazio")
	}
	if _, err := time.Parse(time.RFC3339, snapshot.GeneratedAt); err != nil {
		t.Fatalf("generatedAt nao e RFC3339: %q (%v)", snapshot.GeneratedAt, err)
	}
	if snapshot.HistoryWindow != sensorDebugHistoryMax {
		t.Fatalf("historyWindow = %d, want %d", snapshot.HistoryWindow, sensorDebugHistoryMax)
	}
	if snapshot.Engine.MaxConcurrency != sensorMaxConcurrency {
		t.Fatalf("maxConcurrency = %d, want %d", snapshot.Engine.MaxConcurrency, sensorMaxConcurrency)
	}
	if snapshot.Engine.ScheduleTickSeconds != int(sensorScheduleTick/time.Second) {
		t.Fatalf("scheduleTickSeconds = %d", snapshot.Engine.ScheduleTickSeconds)
	}

	// Service nulo nao pode entrar em panico.
	var nilService *Service
	if got := nilService.SensorDebugSnapshot(); len(got.Sensors) != 0 || got.AgentID != "" {
		t.Fatalf("snapshot de service nulo = %+v", got)
	}
}

// TestSensorDebugSnapshotScriptCached: sensor com script e sensor sem script
// precisam ser distinguíveis (cacheados só quando o CONTEÚDO veio no sync).
func TestSensorDebugSnapshotScriptCached(t *testing.T) {
	response := &SensorSyncResponse{
		SensorFingerprint: "abcdef1234567890",
		Sensors: []SensorPolicy{
			func() SensorPolicy {
				policy := sensorDebugPolicy("script-1", "Script", 60)
				policy.SensorType = SensorTypeScript
				policy.Script = &SensorScriptRef{
					ScriptID: "33333333-3333-3333-3333-333333333333",
					Name:     "sensor.ps1",
					Version:  "1.0.0",
					Content:  "Write-Output 'MDZ_STATUS: OK'",
				}
				return policy
			}(),
			// Script referenciado mas SEM conteúdo: não está cacheado.
			func() SensorPolicy {
				policy := sensorDebugPolicy("script-2", "Script sem conteudo", 60)
				policy.SensorType = SensorTypeScript
				policy.Script = &SensorScriptRef{ScriptID: "44444444-4444-4444-4444-444444444444", Name: "vazio.ps1", Version: "2.0.0"}
				return policy
			}(),
			// Sem script nenhum: os campos de script ficam vazios.
			sensorDebugPolicy("disk-1", "Disco", 60),
		},
	}
	service := sensorDebugService(t, response)

	snapshot := service.SensorDebugSnapshot()
	if len(snapshot.Sensors) != 3 {
		t.Fatalf("sensores = %d, want 3", len(snapshot.Sensors))
	}
	cached := snapshot.Sensors[0]
	if !cached.ScriptCached {
		t.Fatal("sensor com conteudo deveria estar cacheado")
	}
	if cached.ScriptName != "sensor.ps1" || cached.ScriptVersion != "1.0.0" {
		t.Fatalf("script = %q/%q", cached.ScriptName, cached.ScriptVersion)
	}

	empty := snapshot.Sensors[1]
	if empty.ScriptCached {
		t.Fatal("script sem conteudo NAO pode ser considerado cacheado")
	}
	if empty.ScriptName != "vazio.ps1" || empty.ScriptVersion != "2.0.0" {
		t.Fatalf("metadados do script = %q/%q", empty.ScriptName, empty.ScriptVersion)
	}

	none := snapshot.Sensors[2]
	if none.ScriptCached || none.ScriptName != "" || none.ScriptVersion != "" {
		t.Fatalf("sensor sem script = %+v", none)
	}
	if none.ChannelCount != 1 || len(none.Channels) != 1 {
		t.Fatalf("canais = %d/%d, want 1", len(none.Channels), none.ChannelCount)
	}

	if snapshot.Engine.Fingerprint != "abcdef1234567890" {
		t.Fatalf("fingerprint = %q", snapshot.Engine.Fingerprint)
	}
	if snapshot.Engine.FingerprintShort != "abcdef123456…" {
		t.Fatalf("fingerprintShort = %q", snapshot.Engine.FingerprintShort)
	}
}

// TestSensorDebugSnapshotNeverRan: sensor que nunca disparou está DEVIDO agora
// e o NextRunAt é nil (zero time não pode virar "0001-01-01T00:00:00Z").
func TestSensorDebugSnapshotNeverRan(t *testing.T) {
	response := &SensorSyncResponse{Sensors: []SensorPolicy{sensorDebugPolicy("disk-1", "Disco", 60)}}
	service := sensorDebugService(t, response)

	view := service.SensorDebugSnapshot().Sensors[0]
	if !view.Overdue {
		t.Fatal("sensor que nunca rodou deveria estar devido")
	}
	if view.NextRunAt != nil {
		t.Fatalf("nextRunAt = %q, want nil", *view.NextRunAt)
	}
	if view.LastRunAt != nil {
		t.Fatalf("lastRunAt = %q, want nil", *view.LastRunAt)
	}
	if view.IntervalSeconds != 60 || view.TimeoutSeconds != 30 {
		t.Fatalf("interval/timeout = %d/%d", view.IntervalSeconds, view.TimeoutSeconds)
	}
}

// TestSensorDebugSnapshotOverdueWithStaleRun: último disparo antigo => devido e
// próximo disparo no PASSADO (o painel mostra o atraso acumulado).
func TestSensorDebugSnapshotOverdueWithStaleRun(t *testing.T) {
	const sensorID = "disk-1"
	response := &SensorSyncResponse{Sensors: []SensorPolicy{sensorDebugPolicy(sensorID, "Disco", 60)}}
	service := sensorDebugService(t, response)

	stale := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	service.sensorMu.Lock()
	service.sensorLastRun[sensorRunKey(sensorID, "")] = stale
	service.sensorMu.Unlock()

	view := service.SensorDebugSnapshot().Sensors[0]
	if !view.Overdue {
		t.Fatal("sensor com disparo de 2h atras deveria estar devido")
	}
	if view.LastRunAt == nil || *view.LastRunAt != stale.Format(time.RFC3339) {
		t.Fatalf("lastRunAt = %v, want %q", view.LastRunAt, stale.Format(time.RFC3339))
	}
	if view.NextRunAt == nil {
		t.Fatal("nextRunAt nil com disparo registrado")
	}
	next, err := time.Parse(time.RFC3339, *view.NextRunAt)
	if err != nil {
		t.Fatalf("nextRunAt nao e RFC3339: %q", *view.NextRunAt)
	}
	if !next.Before(time.Now()) {
		t.Fatalf("nextRunAt = %v deveria estar no passado", next)
	}

	// Disparo recente: não devido e próximo no futuro.
	service.sensorMu.Lock()
	service.sensorLastRun[sensorRunKey(sensorID, "")] = time.Now().UTC()
	service.sensorMu.Unlock()

	fresh := service.SensorDebugSnapshot().Sensors[0]
	if fresh.Overdue {
		t.Fatal("sensor recem-disparado nao pode estar devido")
	}
	if fresh.NextRunAt == nil {
		t.Fatal("nextRunAt nil com disparo recente")
	}
	nextFresh, _ := time.Parse(time.RFC3339, *fresh.NextRunAt)
	if !nextFresh.After(time.Now()) {
		t.Fatalf("nextRunAt = %v deveria estar no futuro", nextFresh)
	}
}

// TestSensorDebugHistoryFIFO: o ring guarda as 100 amostras MAIS RECENTES,
// descartando as antigas pela cabeça.
func TestSensorDebugHistoryFIFO(t *testing.T) {
	service := sensorDebugService(t, nil)

	total := sensorDebugHistoryMax + 25
	for i := 0; i < total; i++ {
		service.recordSensorHistory([]SensorResult{{
			SensorID:   "disk-1",
			TargetKey:  "C:",
			RunID:      "run-" + string(rune('a'+i%26)) + time.Duration(i).String(),
			StartedAt:  time.Now().UTC().Format(time.RFC3339),
			FinishedAt: time.Now().UTC().Format(time.RFC3339),
			Measure:    SensorMeasure{FreePercent: sensorFloat(float64(i))},
		}})
	}

	history := service.sensorHistorySnapshot()
	if len(history) != sensorDebugHistoryMax {
		t.Fatalf("historico = %d, want %d", len(history), sensorDebugHistoryMax)
	}
	// A primeira amostra guardada deve ser a de índice 25 (as 25 primeiras
	// foram descartadas) e a última deve ser a de índice total-1.
	nome := func(entry sensorHistoryEntry) string {
		return entry.View.RunID
	}
	first := history[0].View.Measure.FreePercent
	if first == nil || *first != 25 {
		t.Fatalf("amostra mais antiga = %v, want 25 (FIFO descarta a cabeca)", first)
	}
	if got := history[len(history)-1].View.Measure.FreePercent; got == nil || *got != float64(total-1) {
		t.Fatalf("amostra mais recente = %v, want %d", got, total-1)
	}
	if nome(history[0]) == "" || nome(history[len(history)-1]) == "" {
		t.Fatal("runId perdido no historico")
	}

	// O snapshot externo é cópia: mutar não afeta o ring interno.
	history[0].SensorID = "intruso"
	if service.sensorHistorySnapshot()[0].SensorID == "intruso" {
		t.Fatal("sensorHistorySnapshot devolveu o slice interno")
	}
}

// TestSensorDebugPendingSamples: pendingSamples reflete a fila de entrega e
// inFlight/maxConcurrency refletem o semáforo.
func TestSensorDebugPendingSamples(t *testing.T) {
	service := sensorDebugService(t, nil)

	if got := service.SensorDebugSnapshot().Engine.PendingSamples; got != 0 {
		t.Fatalf("pendingSamples inicial = %d, want 0", got)
	}

	service.enqueueSensorResults([]SensorResult{{SensorID: "disk-1"}, {SensorID: "disk-2"}})
	if got := service.SensorDebugSnapshot().Engine.PendingSamples; got != 2 {
		t.Fatalf("pendingSamples = %d, want 2", got)
	}

	// Enfileirar vazio não muda nada (o runner chama isto quando não coletou).
	service.enqueueSensorResults(nil)
	if got := service.SensorDebugSnapshot().Engine.PendingSamples; got != 2 {
		t.Fatalf("pendingSamples apos enqueue vazio = %d, want 2", got)
	}

	// Ocupa slots do semáforo: inFlight acompanha.
	if !service.acquireSensorSlot() {
		t.Fatal("deveria adquirir slot")
	}
	if got := service.SensorDebugSnapshot().Engine.InFlight; got != 1 {
		t.Fatalf("inFlight = %d, want 1", got)
	}
	service.releaseSensorSlot()
	if got := service.SensorDebugSnapshot().Engine.InFlight; got != 0 {
		t.Fatalf("inFlight apos release = %d, want 0", got)
	}
}

// TestSensorDebugLastResultsPerSensor: LastResults traz só as amostras do
// sensor, mais recentes primeiro e no máximo sensorDebugLastResultsMax.
func TestSensorDebugLastResultsPerSensor(t *testing.T) {
	response := &SensorSyncResponse{Sensors: []SensorPolicy{
		sensorDebugPolicy("disk-1", "Disco 1", 60),
		sensorDebugPolicy("disk-2", "Disco 2", 60),
	}}
	service := sensorDebugService(t, response)

	// 15 amostras do disk-1 (teto por sensor = 10) intercaladas com disk-2.
	for i := 0; i < 15; i++ {
		service.recordSensorHistory([]SensorResult{
			{SensorID: "disk-1", RunID: "disk1-" + time.Duration(i).String(), Measure: SensorMeasure{FreePercent: sensorFloat(float64(i))}},
			{SensorID: "disk-2", RunID: "disk2-" + time.Duration(i).String(), Measure: SensorMeasure{FreePercent: sensorFloat(float64(100 + i))}},
		})
	}

	snapshot := service.SensorDebugSnapshot()
	if len(snapshot.Sensors) != 2 {
		t.Fatalf("sensores = %d, want 2", len(snapshot.Sensors))
	}

	disk1 := snapshot.Sensors[0]
	if len(disk1.LastResults) != sensorDebugLastResultsMax {
		t.Fatalf("lastResults disk-1 = %d, want %d", len(disk1.LastResults), sensorDebugLastResultsMax)
	}
	// Mais recentes primeiro: 14, 13, ... 5.
	for i, result := range disk1.LastResults {
		want := float64(14 - i)
		if result.Measure.FreePercent == nil || *result.Measure.FreePercent != want {
			t.Fatalf("disk-1 lastResults[%d] = %v, want %v (mais recente primeiro)", i, result.Measure.FreePercent, want)
		}
		if strings.HasPrefix(result.RunID, "disk2-") {
			t.Fatalf("lastResults do disk-1 vazou amostra do disk-2: %q", result.RunID)
		}
	}

	disk2 := snapshot.Sensors[1]
	if len(disk2.LastResults) != sensorDebugLastResultsMax {
		t.Fatalf("lastResults disk-2 = %d, want %d", len(disk2.LastResults), sensorDebugLastResultsMax)
	}
	for i, result := range disk2.LastResults {
		want := float64(114 - i)
		if result.Measure.FreePercent == nil || *result.Measure.FreePercent != want {
			t.Fatalf("disk-2 lastResults[%d] = %v, want %v", i, result.Measure.FreePercent, want)
		}
	}

	// Sensor sem histórico devolve nil (não slice com buraco).
	service2 := sensorDebugService(t, &SensorSyncResponse{Sensors: []SensorPolicy{sensorDebugPolicy("disk-9", "Sem historico", 60)}})
	if got := service2.SensorDebugSnapshot().Sensors[0].LastResults; got != nil {
		t.Fatalf("lastResults sem historico = %+v, want nil", got)
	}
}
