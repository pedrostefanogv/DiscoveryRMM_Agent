//go:build windows

package automation

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Teste de integração do MOTOR de sensores contra um servidor HTTP falso:
// valida o caminho completo sync -> coleta -> lote -> POST /sensors/samples,
// incluindo headers agent-auth, camelCase do envelope e o fingerprint conhecido
// (sem o qual o agente baixaria a policy inteira a cada ciclo).
// ─────────────────────────────────────────────────────────────────────────────

func TestSensorEngineIntegrationWithServer(t *testing.T) {
	const agentID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	const sensorID = "11111111-1111-1111-1111-111111111111"

	var (
		mu          sync.Mutex
		syncCalls   int
		syncBody    map[string]any
		sampleCalls int
		lastBatch   SensorResultBatch
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Agent-ID"); got != agentID {
			t.Errorf("X-Agent-ID = %q, want %q", got, agentID)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mdz_test" {
			t.Errorf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/api/v1/agent-auth/me/sensors/sync":
			body, _ := io.ReadAll(r.Body)
			var decoded map[string]any
			_ = json.Unmarshal(body, &decoded)
			mu.Lock()
			syncCalls++
			syncBody = decoded
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"upToDate":               false,
				"sensorFingerprint":      "fp-1",
				"generatedAt":            "2026-11-03T12:00:00Z",
				"sensorCount":            1,
				"defaultIntervalSeconds": 300,
				"sensors": []map[string]any{{
					"sensorId":        sensorID,
					"name":            "Uptime",
					"sensorType":      "Uptime",
					"intervalSeconds": 30,
					"timeoutSeconds":  30,
					"channels":        []map[string]any{{"key": "uptime_seconds", "isPrimary": true}},
				}},
			})
		case "/api/v1/agent-auth/me/sensors/samples":
			body, _ := io.ReadAll(r.Body)
			var batch SensorResultBatch
			if err := json.Unmarshal(body, &batch); err != nil {
				t.Errorf("payload de amostras invalido: %v (%s)", err, body)
			}
			mu.Lock()
			sampleCalls++
			lastBatch = batch
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(batch.Samples), "rejected": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewService(func() RuntimeConfig {
		return RuntimeConfig{BaseURL: server.URL, Token: "mdz_test", AgentID: agentID}
	}, func(string) {})

	service.refreshSensors(t.Context(), true)
	sensors, _, disabled := service.sensorStateForTests()
	if disabled {
		t.Fatal("loop nao deveria estar desabilitado")
	}
	if len(sensors) != 1 || sensors[0].Policy.SensorID != sensorID {
		t.Fatalf("sensores aplicados = %+v", sensors)
	}
	mu.Lock()
	if syncCalls != 1 {
		t.Fatalf("syncCalls = %d, want 1", syncCalls)
	}
	if include, _ := syncBody["includeScriptContent"].(bool); !include {
		t.Fatalf("sync deveria pedir o conteudo do script: %+v", syncBody)
	}
	mu.Unlock()

	// Coleta direta + entrega do lote (sem depender do ticker de 15s).
	service.runSensor(t.Context(), sensors[0])
	service.flushSensorSamples(t.Context())

	mu.Lock()
	if sampleCalls != 1 {
		t.Fatalf("sampleCalls = %d, want 1", sampleCalls)
	}
	if lastBatch.AgentID != agentID {
		t.Fatalf("agentId do lote = %q", lastBatch.AgentID)
	}
	if lastBatch.CorrelationID == "" {
		t.Fatal("lote sem correlationId")
	}
	if len(lastBatch.Samples) != 1 {
		t.Fatalf("amostras = %d, want 1", len(lastBatch.Samples))
	}
	sample := lastBatch.Samples[0]
	mu.Unlock()

	if sample.SensorID != sensorID {
		t.Fatalf("sensorId = %q", sample.SensorID)
	}
	if sample.RunID == "" {
		t.Fatal("runId (uuid) nao preenchido")
	}
	if sample.StartedAt == "" || sample.FinishedAt == "" || sample.DurationMs == nil {
		t.Fatalf("timestamps/duration ausentes: %+v", sample)
	}
	if sample.Error != "" {
		t.Fatalf("erro de coleta: %s", sample.Error)
	}
	if sample.Measure.UptimeSeconds == nil || *sample.Measure.UptimeSeconds <= 0 {
		t.Fatalf("uptimeSeconds = %v", sample.Measure.UptimeSeconds)
	}

	// Segundo sync manda o fingerprint conhecido: o servidor pode responder
	// up-to-date sem devolver a lista inteira.
	service.refreshSensors(t.Context(), true)
	mu.Lock()
	defer mu.Unlock()
	if known, _ := syncBody["knownSensorFingerprint"].(string); known != "fp-1" {
		t.Fatalf("knownSensorFingerprint = %q, want fp-1", known)
	}
}
