package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestRefreshSensors_ConteudoSoNoForceERecuperaSemCorpo cobre o contrato de
// ECONOMIA do sensor-sync:
//
//   - sync periódico (force=false) NÃO pede o conteúdo dos scripts — só o
//     fingerprint, para o servidor poder responder "up to date" sem payload;
//   - se a policy chegar com referência de script SEM corpo (cache local
//     corrompido), o agente refaz o sync com IncludeScriptContent=true e o
//     servidor entrega o conteúdo (o 2º sync ignora o atalho "up to date").
func TestRefreshSensors_ConteudoSoNoForceERecuperaSemCorpo(t *testing.T) {
	t.Parallel()

	const (
		agentID  = "55555555-5555-5555-5555-555555555555"
		sensorID = "66666666-6666-6666-6666-666666666666"
	)

	var mu sync.Mutex
	var includeFlags []bool
	syncCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sensors/sync") {
			http.NotFound(w, r)
			return
		}

		var request SensorSyncRequest
		_ = json.NewDecoder(r.Body).Decode(&request)

		mu.Lock()
		includeFlags = append(includeFlags, request.IncludeScriptContent)
		syncCalls++
		mu.Unlock()

		script := map[string]any{
			"scriptId":          "77777777-7777-7777-7777-777777777777",
			"name":              "sensor.ps1",
			"version":           "1.0.0",
			"scriptType":        "PowerShell",
			"contentHashSha256": "deadbeef",
		}
		// Sem IncludeScriptContent o servidor entrega só a referência (simula o
		// que aconteceria se o conteúdo não viesse); com true, entrega o corpo.
		if request.IncludeScriptContent {
			script["content"] = "Write-Output 'MDZ_STATUS: OK'"
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"upToDate":               false,
			"sensorFingerprint":      "fp-1",
			"generatedAt":            "2026-11-03T00:00:00Z",
			"sensorCount":            1,
			"defaultIntervalSeconds": 300,
			"sensors": []map[string]any{{
				"sensorId":        sensorID,
				"name":            "Scripted",
				"sensorType":      "Script",
				"intervalSeconds": 60,
				"timeoutSeconds":  30,
				"channels":        []map[string]any{{"key": "valor", "isPrimary": true}},
				"script":          script,
			}},
		})
	}))
	defer srv.Close()

	service := NewService(func() RuntimeConfig {
		return RuntimeConfig{BaseURL: srv.URL, Token: "mdz_test", AgentID: agentID}
	}, func(string) {})

	service.refreshSensors(context.Background(), false)

	mu.Lock()
	flags := append([]bool(nil), includeFlags...)
	calls := syncCalls
	mu.Unlock()

	if calls != 2 {
		t.Fatalf("syncs = %d, want 2 (periódico + recuperação de conteúdo): %v", calls, flags)
	}
	if flags[0] {
		t.Fatalf("sync periódico pediu conteúdo de script (deveria ser só fingerprint): %v", flags)
	}
	if !flags[1] {
		t.Fatalf("recuperação deveria pedir IncludeScriptContent=true: %v", flags)
	}

	sensors, _, disabled := service.sensorStateForTests()
	if disabled {
		t.Fatal("loop não deveria estar desabilitado")
	}
	if len(sensors) != 1 || sensors[0].Policy.Script == nil {
		t.Fatalf("sensor de script não aplicado: %+v", sensors)
	}
	if strings.TrimSpace(sensors[0].Policy.Script.Content) == "" {
		t.Fatal("conteúdo do script deveria ter sido recuperado no 2º sync")
	}
}

// TestRefreshSensors_ForceSemprePedeConteudo garante que o "forçar sync" do
// console continua trazendo o corpo do script (é o caminho de recuperação manual).
func TestRefreshSensors_ForceSemprePedeConteudo(t *testing.T) {
	t.Parallel()

	var include atomicBool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sensors/sync") {
			http.NotFound(w, r)
			return
		}
		var request SensorSyncRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		include.Set(request.IncludeScriptContent)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"upToDate":               false,
			"sensorFingerprint":      "fp",
			"generatedAt":            "2026-11-03T00:00:00Z",
			"sensorCount":            0,
			"defaultIntervalSeconds": 300,
			"sensors":                []any{},
		})
	}))
	defer srv.Close()

	service := NewService(func() RuntimeConfig {
		return RuntimeConfig{BaseURL: srv.URL, Token: "mdz_test", AgentID: "88888888-8888-8888-8888-888888888888"}
	}, func(string) {})

	service.refreshSensors(context.Background(), true)

	if !include.Get() {
		t.Fatal("sync forçado deveria pedir IncludeScriptContent=true")
	}
}

// atomicBool é um bool seguro para uso no handler HTTP.
type atomicBool struct {
	mu    sync.Mutex
	value bool
}

func (b *atomicBool) Set(value bool) {
	b.mu.Lock()
	b.value = value
	b.mu.Unlock()
}

func (b *atomicBool) Get() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.value
}
