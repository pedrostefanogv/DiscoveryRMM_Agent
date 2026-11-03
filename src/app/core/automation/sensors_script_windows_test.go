//go:build windows

package automation

import (
	"context"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Testes que executam PROCESSO de verdade (Windows): o contrato do sensor de
// script depende do exit code e do protocolo MDZ no stdout.
// ─────────────────────────────────────────────────────────────────────────────

// TestExecuteSensorProcessExitCode20 cobre a fixture "exit 20": exit code
// diferente de zero é EVIDÊNCIA (vai em measure.exitCode e o servidor aplica o
// mapa de exit codes do sensor), NÃO erro de coleta.
func TestExecuteSensorProcessExitCode20(t *testing.T) {
	result := executeSensorProcess(context.Background(), "cmd", []string{"/c", "exit 20"}, 15*time.Second, nil)
	if result.TimedOut {
		t.Fatal("cmd /c exit 20 nao deveria dar timeout")
	}
	if result.Err != "" {
		t.Fatalf("exit 20 nao e erro de execucao, veio Err=%q", result.Err)
	}
	if result.ExitCode != 20 {
		t.Fatalf("exitCode = %d, want 20", result.ExitCode)
	}
}

// TestExecuteSensorProcessCapturesStreamsSeparately garante stdout/stderr
// separados (o parser só lê stdout).
func TestExecuteSensorProcessCapturesStreamsSeparately(t *testing.T) {
	result := executeSensorProcess(context.Background(), "cmd", []string{"/c", "echo saida-padrao & echo erro-padrao 1>&2"}, 15*time.Second, nil)
	if !containsText(result.Stdout, "saida-padrao") {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if !containsText(result.Stderr, "erro-padrao") {
		t.Fatalf("stderr = %q", result.Stderr)
	}
	if containsText(result.Stdout, "erro-padrao") {
		t.Fatalf("stdout nao deveria conter o stderr: %q", result.Stdout)
	}
}

// TestRunSensorScriptMDZEndToEnd roda um script PowerShell real que emite o
// protocolo MDZ e confere a amostra pronta para envio (canais + status + exit).
func TestRunSensorScriptMDZEndToEnd(t *testing.T) {
	content := joinLines(
		"Write-Output 'MDZ_STATUS: WARNING'",
		"Write-Output 'MDZ_MESSAGE: fila alta'",
		"Write-Output 'MDZ_METRIC: queue=7'",
		"Write-Output 'linha visivel'",
		"exit 20",
	)
	cfg := &SensorConfig{
		Policy: SensorPolicy{
			SensorID:   "11111111-1111-1111-1111-111111111111",
			Name:       "fila",
			SensorType: SensorTypeScript,
			Script:     &SensorScriptRef{ScriptType: ScriptPowerShell, Content: content},
		},
		Timeout:          60 * time.Second,
		Interval:         time.Minute,
		DeclaredChannels: declaredChannels("queue"),
	}

	collection := runSensorScript(context.Background(), cfg)
	if collection.Err != "" {
		t.Fatalf("erro de coleta: %s", collection.Err)
	}
	if collection.Measure.ExitCode == nil || *collection.Measure.ExitCode != 20 {
		t.Fatalf("exitCode = %v, want 20", collection.Measure.ExitCode)
	}
	if collection.DeclaredStatus != "WARNING" {
		t.Fatalf("declaredStatus = %q, want WARNING", collection.DeclaredStatus)
	}
	if collection.DeclaredMessage != "fila alta" {
		t.Fatalf("declaredMessage = %q", collection.DeclaredMessage)
	}
	if got := collection.Channels["queue"]; got != 7 {
		t.Fatalf("queue = %v, want 7", got)
	}
	if collection.Measure.Stdout == nil || !containsText(*collection.Measure.Stdout, "linha visivel") {
		t.Fatalf("stdout sem a saida visivel: %v", collection.Measure.Stdout)
	}
	if collection.Measure.Stdout != nil && containsText(*collection.Measure.Stdout, "MDZ_") {
		t.Fatalf("protocolo MDZ nao deveria ir no stdout enviado: %q", *collection.Measure.Stdout)
	}
}

// TestCollectSensorUptime usa a API nativa (GetTickCount64) de verdade.
func TestCollectSensorUptime(t *testing.T) {
	cfg := &SensorConfig{Policy: SensorPolicy{SensorType: SensorTypeUptime}}
	collections := collectSensor(context.Background(), cfg)
	if len(collections) != 1 {
		t.Fatalf("collections = %d, want 1", len(collections))
	}
	if collections[0].Err != "" {
		t.Fatalf("erro: %s", collections[0].Err)
	}
	if collections[0].Measure.UptimeSeconds == nil || *collections[0].Measure.UptimeSeconds <= 0 {
		t.Fatalf("uptimeSeconds = %v", collections[0].Measure.UptimeSeconds)
	}
}

// TestCollectSensorUnsupportedType devolve erro EXPLÍCITO (servidor marca
// Unknown + RunError) em vez de medida inventada. Todos os tipos do contrato têm
// coletor; um tipo desconhecido (servidor mais novo) não pode derrubar o motor.
func TestCollectSensorUnsupportedType(t *testing.T) {
	cfg := &SensorConfig{Policy: SensorPolicy{SensorType: SensorType("TipoQueNaoExiste")}}
	collections := collectSensor(context.Background(), cfg)
	if len(collections) != 1 || collections[0].Err == "" {
		t.Fatalf("collect = %+v, want erro de tipo nao suportado", collections)
	}
	if !containsText(collections[0].Err, "nao suportado") {
		t.Fatalf("erro = %q, want 'nao suportado'", collections[0].Err)
	}
}
