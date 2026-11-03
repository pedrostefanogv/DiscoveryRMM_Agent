//go:build windows

package automation

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Testes dos coletores NATIVOS de rede: DnsLatency e HttpThroughput.
//
// DNS usa o resolver do sistema (localhost/hosts é determinístico); o caminho de
// erro é testado com nome inválido e com um lookup fake (sem rede). Throughput
// usa httptest: nenhum teste depende de internet.
// ─────────────────────────────────────────────────────────────────────────────

func newSensorCollectConfig(sensorType SensorType, target, optionsJSON string, timeout time.Duration) SensorConfig {
	policy := SensorPolicy{
		SensorID:    "11111111-1111-1111-1111-111111111111",
		Name:        "sensor de teste",
		SensorType:  sensorType,
		Target:      target,
		OptionsJSON: optionsJSON,
	}
	return SensorConfig{Policy: policy, Options: parseSensorOptions(optionsJSON), Timeout: timeout}
}

// ── DNS ──

// TestCollectSensorDnsLatencyLocalhost cobre o caminho FELIZ de verdade: o nome
// "localhost" sai do arquivo hosts, sem consulta DNS externa.
func TestCollectSensorDnsLatencyLocalhost(t *testing.T) {
	cfg := newSensorCollectConfig(SensorTypeDNSLatency, "localhost", `{"samples":2}`, 5*time.Second)
	collections := collectSensor(context.Background(), &cfg)
	if len(collections) != 1 {
		t.Fatalf("collections = %d, want 1 (DNS nao tem alvo por volume)", len(collections))
	}
	if collections[0].Err != "" {
		t.Fatalf("erro de coleta inesperado: %s", collections[0].Err)
	}
	if collections[0].TargetKey != "" {
		t.Fatalf("targetKey = %q, want vazio (1 amostra por sensor)", collections[0].TargetKey)
	}
	if collections[0].Measure.ResolveMs == nil {
		t.Fatal("resolveMs ausente na evidencia")
	}
	if *collections[0].Measure.ResolveMs < 0 || *collections[0].Measure.ResolveMs > 5000 {
		t.Fatalf("resolveMs = %v, fora da faixa plausivel", *collections[0].Measure.ResolveMs)
	}
}

// TestCollectDnsLatencyDefaultSamples garante o default de 3 resolucoes e que a
// media e a das amostras (lookup fake com sleep: deterministico, sem rede).
func TestCollectDnsLatencyDefaultSamples(t *testing.T) {
	cases := []struct {
		name     string
		options  string
		wantHits int
	}{
		{name: "sem options usa 3", options: "", wantHits: 3},
		{name: "samples do options", options: `{"samples":4}`, wantHits: 4},
		{name: "samples zero cai no default", options: `{"samples":0}`, wantHits: 3},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			calls := 0
			stub := func(ctx context.Context, host string) ([]string, error) {
				time.Sleep(2 * time.Millisecond)
				calls++
				if host != "dns.example.test" {
					t.Errorf("host = %q, want a porta removida do alvo", host)
				}
				return []string{"127.0.0.1"}, nil
			}
			cfg := newSensorCollectConfig(SensorTypeDNSLatency, "dns.example.test:53", testCase.options, 5*time.Second)
			collection := collectDNSLatencySampleWith(context.Background(), &cfg, stub)
			if collection.Err != "" {
				t.Fatalf("erro de coleta inesperado: %s", collection.Err)
			}
			if calls != testCase.wantHits {
				t.Fatalf("resolucoes = %d, want %d", calls, testCase.wantHits)
			}
			if collection.Measure.ResolveMs == nil {
				t.Fatal("resolveMs ausente")
			}
			if *collection.Measure.ResolveMs < 1 {
				t.Fatalf("resolveMs = %v, want media >= 1ms (fake dorme 2ms)", *collection.Measure.ResolveMs)
			}
		})
	}
}

// TestCollectSensorDnsLatencyInvalidDomain garante que erro de resolucao vira
// Error de coleta (servidor marca Unknown) e NAO medida inventada nem panic.
func TestCollectSensorDnsLatencyInvalidDomain(t *testing.T) {
	cfg := newSensorCollectConfig(SensorTypeDNSLatency, "bad..host", "", 5*time.Second)
	collections := collectSensor(context.Background(), &cfg)
	if len(collections) != 1 {
		t.Fatalf("collections = %d, want 1", len(collections))
	}
	if collections[0].Err == "" {
		t.Fatal("dominio invalido deveria produzir erro de coleta")
	}
	if collections[0].Measure.ResolveMs != nil {
		t.Fatalf("resolveMs = %v, want nil em erro (nao inventar medida)", *collections[0].Measure.ResolveMs)
	}
}

// TestCollectSensorDnsLatencyLookupFailure cobre o erro sem depender de DNS.
func TestCollectSensorDnsLatencyLookupFailure(t *testing.T) {
	stub := func(ctx context.Context, host string) ([]string, error) {
		return nil, fmt.Errorf("no such host")
	}
	cfg := newSensorCollectConfig(SensorTypeDNSLatency, "sensor.invalid", "", 5*time.Second)
	collection := collectDNSLatencySampleWith(context.Background(), &cfg, stub)
	if collection.Err == "" {
		t.Fatal("falha de resolucao deveria virar erro de coleta")
	}
	if !strings.Contains(collection.Err, "sensor.invalid") {
		t.Fatalf("erro = %q, want o hostname na mensagem", collection.Err)
	}
}

// TestCollectSensorDnsLatencyWithoutTarget nao pode derrubar a coleta.
func TestCollectSensorDnsLatencyWithoutTarget(t *testing.T) {
	cfg := newSensorCollectConfig(SensorTypeDNSLatency, "   ", "", 5*time.Second)
	collections := collectSensor(context.Background(), &cfg)
	if len(collections) != 1 || collections[0].Err == "" {
		t.Fatalf("collect = %+v, want erro de alvo ausente", collections)
	}
}

// ── HTTP throughput ──

func newThroughputTestServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestCollectSensorHttpThroughputDownloads valida a evidencia completa: bytes
// baixados, downloadMbps > 0 e totalMs presentes, com targetKey vazio.
func TestCollectSensorHttpThroughputDownloads(t *testing.T) {
	const payloadSize = 256 * 1024
	server := newThroughputTestServer(t, bytes.Repeat([]byte("d"), payloadSize))

	options := fmt.Sprintf(`{"url":%q,"bytes":%d,"durationMs":5000}`, server.URL, payloadSize)
	cfg := newSensorCollectConfig(SensorTypeHTTPThroughput, "", options, 10*time.Second)
	collections := collectSensor(context.Background(), &cfg)
	if len(collections) != 1 {
		t.Fatalf("collections = %d, want 1", len(collections))
	}
	collection := collections[0]
	if collection.Err != "" {
		t.Fatalf("erro de coleta inesperado: %s", collection.Err)
	}
	if collection.TargetKey != "" {
		t.Fatalf("targetKey = %q, want vazio", collection.TargetKey)
	}
	if collection.Measure.Bytes == nil || int64(*collection.Measure.Bytes) != payloadSize {
		t.Fatalf("bytes = %v, want %d", collection.Measure.Bytes, payloadSize)
	}
	if collection.Measure.DownloadMbps == nil || *collection.Measure.DownloadMbps <= 0 {
		t.Fatalf("downloadMbps = %v, want > 0", collection.Measure.DownloadMbps)
	}
	if collection.Measure.TotalMs == nil {
		t.Fatal("totalMs ausente na evidencia")
	}
	if collection.Measure.StatusCode == nil || *collection.Measure.StatusCode != http.StatusOK {
		t.Fatalf("statusCode = %v, want 200", collection.Measure.StatusCode)
	}
}

// TestCollectSensorHttpThroughputRespectsByteCap prova que o corpo e limitado
// pelo options bytes (nao baixa o servidor inteiro).
func TestCollectSensorHttpThroughputRespectsByteCap(t *testing.T) {
	const payloadSize = 512 * 1024
	const requestedBytes = 64 * 1024
	server := newThroughputTestServer(t, bytes.Repeat([]byte("x"), payloadSize))

	options := fmt.Sprintf(`{"url":%q,"bytes":%d,"durationMs":5000}`, server.URL, requestedBytes)
	cfg := newSensorCollectConfig(SensorTypeHTTPThroughput, "", options, 10*time.Second)
	collections := collectSensor(context.Background(), &cfg)
	if len(collections) != 1 || collections[0].Err != "" {
		t.Fatalf("collect = %+v", collections)
	}
	if collections[0].Measure.Bytes == nil || int64(*collections[0].Measure.Bytes) != requestedBytes {
		t.Fatalf("bytes = %v, want o limite de %d", collections[0].Measure.Bytes, requestedBytes)
	}
}

// TestCollectSensorHttpThroughputServerError: HTTP >= 400 e falha (o corpo e uma
// pagina de erro, nao uma medicao).
func TestCollectSensorHttpThroughputServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	options := fmt.Sprintf(`{"url":%q,"bytes":1024}`, server.URL)
	cfg := newSensorCollectConfig(SensorTypeHTTPThroughput, "", options, 5*time.Second)
	collections := collectSensor(context.Background(), &cfg)
	if len(collections) != 1 || collections[0].Err == "" {
		t.Fatalf("collect = %+v, want erro de coleta", collections)
	}
	if !strings.Contains(collections[0].Err, "500") {
		t.Fatalf("erro = %q, want o status HTTP na mensagem", collections[0].Err)
	}
}

// TestCollectSensorHttpThroughputSlowServer: servidor que segura a resposta alem
// do timeout do sensor vira Error + TimedOut (servidor marca Unknown).
func TestCollectSensorHttpThroughputSlowServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(server.Close)

	options := fmt.Sprintf(`{"url":%q,"bytes":1024,"durationMs":5000}`, server.URL)
	cfg := newSensorCollectConfig(SensorTypeHTTPThroughput, "", options, 300*time.Millisecond)
	started := time.Now()
	collections := collectSensor(context.Background(), &cfg)
	elapsed := time.Since(started)
	if len(collections) != 1 {
		t.Fatalf("collections = %d, want 1", len(collections))
	}
	if collections[0].Err == "" {
		t.Fatal("servidor lento deveria produzir erro de coleta")
	}
	if !collections[0].Measure.TimedOut {
		t.Fatalf("timedOut = false (err=%q), want true", collections[0].Err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("coleta demorou %v, want respeitar o timeout", elapsed)
	}
}
