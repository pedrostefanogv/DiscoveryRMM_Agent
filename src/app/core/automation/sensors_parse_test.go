package automation

import (
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Testes do parser de sensores com fixtures REAIS de saída de script.
//
// O parser é a peça que decide o que vira canal numérico: se ele erra, o
// servidor avalia o sensor com evidência errada (status falso). Por isso as
// fixtures abaixo são saídas reais: JSON do Ookla, mensagem do speedtest,
// contadores de interface e saída de ferramenta com host:valor.
// ─────────────────────────────────────────────────────────────────────────────

// ooklaJSONFixture é a saída real do Ookla CLI --format=json (campos usados:
// download.bandwidth em BYTES/s, ping.latency em ms).
const ooklaJSONFixture = `{"type":"result","timestamp":"2024-05-11T12:00:00Z","ping":{"jitter":0.412,"latency":8.735,"low":7.919,"high":9.551},"download":{"bandwidth":31087500,"bytes":394249556,"elapsed":10150},"upload":{"bandwidth":10485760,"bytes":131072000,"elapsed":10001},"packetLoss":0,"isp":"Provedor Exemplo","interface":{"internalIp":"192.168.0.10","name":"Ethernet","isVpn":false},"server":{"id":1,"host":"speedtest.exemplo.net","port":8080,"name":"Servidor","location":"Sao Paulo","country":"BR"}}`

func declaredChannels(keys ...string) map[string]bool {
	declared := make(map[string]bool, len(keys))
	for _, key := range keys {
		declared[key] = true
	}
	return declared
}

// TestParseSensorStdoutOoklaJSON cobre o caso mais comum de sensor de banda:
// JSON do Ookla com scale 8e-6 (bytes/s -> Mbps).
func TestParseSensorStdoutOoklaJSON(t *testing.T) {
	stdout := "Iniciando teste..." + "\n" + ooklaJSONFixture + "\n" + "Teste concluido."
	extractors := []sensorExtractor{
		{Type: "json", Channel: "download_mbps", JSONPath: "download.bandwidth", Scale: 8e-6, Aggregate: "last"},
		{Type: "json", Channel: "latency_ms", JSONPath: "ping.latency", Scale: 1, Aggregate: "last"},
	}

	parsed := parseSensorStdout(stdout, declaredChannels("download_mbps", "latency_ms"), extractors)

	if got := parsed.Channels["download_mbps"]; got < 248.69 || got > 248.71 {
		t.Fatalf("download_mbps = %v, want ~248.7", got)
	}
	if got := parsed.Channels["latency_ms"]; got < 8.73 || got > 8.74 {
		t.Fatalf("latency_ms = %v, want ~8.735", got)
	}
	// O JSON faz parte do output exibido: só as linhas de PROTOCOLO MDZ saem.
	if !containsText(parsed.Output, "\"download\"") {
		t.Fatalf("output nao preservou o JSON: %q", parsed.Output)
	}
}

// TestParseSensorStdoutSpeedtestText cobre a saída textual do speedtest CLI.
func TestParseSensorStdoutSpeedtestText(t *testing.T) {
	stdout := "Download Speed: 248.7 mbps\nUpload Speed: 84.2 mbps"
	extractors := []sensorExtractor{
		{Type: "regex", Channel: "download_mbps", Regex: "Download Speed:\\s*([0-9.]+)", Scale: 1, Aggregate: "last"},
	}
	parsed := parseSensorStdout(stdout, declaredChannels("download_mbps"), extractors)
	if got := parsed.Channels["download_mbps"]; got != 248.7 {
		t.Fatalf("download_mbps = %v, want 248.7", got)
	}
}

// TestParseSensorStdoutInterfaceCounters cobre contadores RX/TX no mesmo texto.
func TestParseSensorStdoutInterfaceCounters(t *testing.T) {
	stdout := "RX:123B/s TX:456B/s"
	extractors := []sensorExtractor{
		{Type: "regex", Channel: "rx_bps", Regex: "RX:([0-9]+)B/s", Scale: 1, Aggregate: "last"},
		{Type: "regex", Channel: "tx_bps", Regex: "TX:([0-9]+)B/s", Scale: 1, Aggregate: "last"},
	}
	parsed := parseSensorStdout(stdout, declaredChannels("rx_bps", "tx_bps"), extractors)
	if parsed.Channels["rx_bps"] != 123 {
		t.Fatalf("rx_bps = %v, want 123", parsed.Channels["rx_bps"])
	}
	if parsed.Channels["tx_bps"] != 456 {
		t.Fatalf("tx_bps = %v, want 456", parsed.Channels["tx_bps"])
	}
}

// TestParseSensorStdoutSimpleKeyValue cobre saidas do tipo host:123.
func TestParseSensorStdoutSimpleKeyValue(t *testing.T) {
	parsed := parseSensorStdout("host:123", declaredChannels("value"), []sensorExtractor{
		{Type: "regex", Channel: "value", Regex: "host:([0-9]+)", Scale: 1, Aggregate: "last"},
	})
	if got := parsed.Channels["value"]; got != 123 {
		t.Fatalf("value = %v, want 123", got)
	}
}

// TestParseSensorStdoutMDZProtocol cobre o protocolo MDZ completo: linhas
// consumidas, último valor vence e canal não declarado descartado.
func TestParseSensorStdoutMDZProtocol(t *testing.T) {
	stdout := joinLines(
		"MDZ_STATUS: OK",
		"MDZ_MESSAGE: primeira mensagem",
		"MDZ_METRIC: free_percent=12.5",
		"MDZ_STATUS: WARNING",
		"MDZ_MESSAGE: disco quase cheio",
		"MDZ_METRIC: free_percent=11",
		"MDZ_METRIC_JSON: {\"queue_depth\": 3, \"nao_declarado\": 99}",
		"linha de saida normal",
	)

	parsed := parseSensorStdout(stdout, declaredChannels("free_percent", "queue_depth"), nil)

	if parsed.Status != "WARNING" {
		t.Fatalf("status = %q, want WARNING (ultimo valor vence)", parsed.Status)
	}
	if parsed.Message != "disco quase cheio" {
		t.Fatalf("message = %q", parsed.Message)
	}
	if got := parsed.Channels["free_percent"]; got != 11 {
		t.Fatalf("free_percent = %v, want 11 (ultimo valor vence)", got)
	}
	if got := parsed.Channels["queue_depth"]; got != 3 {
		t.Fatalf("queue_depth = %v, want 3", got)
	}
	if _, found := parsed.Channels["nao_declarado"]; found {
		t.Fatal("canal nao declarado deveria ser descartado")
	}
	if containsText(parsed.Output, "MDZ_") {
		t.Fatalf("linhas de protocolo deveriam ser consumidas: %q", parsed.Output)
	}
	if !containsText(parsed.Output, "linha de saida normal") {
		t.Fatalf("output perdeu a saida normal: %q", parsed.Output)
	}
}

// TestParseSensorStdoutMDZExtractor cobre o extrator type=mdz com scale.
func TestParseSensorStdoutMDZExtractor(t *testing.T) {
	stdout := joinLines(
		"MDZ_METRIC: bytes_total=2048",
	)
	parsed := parseSensorStdout(stdout, declaredChannels("bytes_total"), []sensorExtractor{
		{Type: "mdz", Channel: "bytes_total", Scale: 1, Aggregate: "last"},
	})
	if got := parsed.Channels["bytes_total"]; got != 2048 {
		t.Fatalf("bytes_total = %v, want 2048", got)
	}
}

// TestParseSensorStdoutAggregates cobre last/first/min/max/avg/sum.
func TestParseSensorStdoutAggregates(t *testing.T) {
	stdout := joinLines("v=5", "v=1", "v=9")
	cases := []struct {
		aggregate string
		want      float64
	}{
		{"last", 9},
		{"first", 5},
		{"min", 1},
		{"max", 9},
		{"avg", 5},
		{"sum", 15},
		{"", 9}, // default = last
	}
	for _, testCase := range cases {
		t.Run("aggregate="+testCase.aggregate, func(t *testing.T) {
			parsed := parseSensorStdout(stdout, declaredChannels("v"), []sensorExtractor{{
				Type: "regex", Channel: "v", Regex: "v=([0-9]+)", Scale: 1, Aggregate: testCase.aggregate,
			}})
			if got := parsed.Channels["v"]; got != testCase.want {
				t.Fatalf("aggregate %q = %v, want %v", testCase.aggregate, got, testCase.want)
			}
		})
	}
}

// TestParseSensorExtractorsInvalidRuleIgnored garante que regra quebrada no
// console não derruba os outros canais do sensor.
func TestParseSensorExtractorsInvalidRuleIgnored(t *testing.T) {
	raw := `[{"type":"regex","channel":"broken","regex":"([a-z"},{"type":"regex","channel":"ok","regex":"v=([0-9]+)"}]`
	extractors := parseSensorExtractors(raw)
	if len(extractors) != 1 {
		t.Fatalf("extractors = %d, want 1 (regra invalida ignorada)", len(extractors))
	}
	parsed := parseSensorStdout("v=7", declaredChannels("ok"), extractors)
	if parsed.Channels["ok"] != 7 {
		t.Fatalf("ok = %v, want 7", parsed.Channels["ok"])
	}
}

// TestParseLooseFloat cobre formatos que só um parser tolerante aceita.
func TestParseLooseFloat(t *testing.T) {
	cases := map[string]float64{
		"248.7 mbps": 248.7,
		"\u003c1ms":  1,
		"12,5":       12.5,
		" 42 ":       42,
	}
	for raw, want := range cases {
		got, ok := parseLooseFloat(raw)
		if !ok || got != want {
			t.Fatalf("parseLooseFloat(%q) = %v,%t want %v", raw, got, ok, want)
		}
	}
}

// TestParseSensorOptions lê OptionsJson por nome, com fallback.
func TestParseSensorOptions(t *testing.T) {
	options := parseSensorOptions(`{"count": 6, "expectedStatusMin": "250", "passIfStartPending": false, "method": "HEAD"}`)
	if got := options.Int(4, "count"); got != 6 {
		t.Fatalf("count = %d, want 6", got)
	}
	if got := options.Int(200, "expectedStatusMin", "expectedStatusMinimum"); got != 250 {
		t.Fatalf("expectedStatusMin = %d, want 250", got)
	}
	if options.Bool(true, "passIfStartPending") {
		t.Fatal("passIfStartPending deveria ser false")
	}
	if got := options.String("METHOD"); got != "HEAD" {
		t.Fatalf("method = %q, want HEAD (case-insensitive)", got)
	}
	// JSON inválido não pode derrubar a coleta: cai nos defaults.
	if got := parseSensorOptions("{invalido").Int(4, "count"); got != 4 {
		t.Fatalf("options invalidas: count = %d, want 4", got)
	}
}

// helpers do arquivo de teste.

func joinLines(lines ...string) string {
	joined := ""
	for index, line := range lines {
		if index > 0 {
			joined += "\n"
		}
		joined += line
	}
	return joined
}

func containsText(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}
