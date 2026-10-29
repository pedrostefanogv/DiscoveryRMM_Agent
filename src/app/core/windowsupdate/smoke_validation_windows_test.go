//go:build windows

package windowsupdate

// Validacao em MAQUINA REAL dos caminhos que o CI Linux nao exercita:
// leitura da politica (WU-16/WU-01), sonda de estado e scan WUA (WU-02/WU-14/
// WU-15). Roda somente com DISCOVERY_WUA_SMOKE=1 para nao pesar no CI padrao.
//
// Uso: $env:DISCOVERY_WUA_SMOKE='1'; go test ./app/core/windowsupdate/ -run TestSmoke -v

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func smokeEnabled(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("DISCOVERY_WUA_SMOKE")) != "1" {
		t.Skip("defina DISCOVERY_WUA_SMOKE=1 para rodar a validacao em maquina real")
	}
}

func TestSmokeReadPolicyRealRegistry(t *testing.T) {
	smokeEnabled(t)

	// Caminhos corretos (WU-01): mesmo quando as chaves nao existem, a leitura
	// NAO pode falhar — ela apenas devolve valores nil.
	settings, err := ReadPolicy()
	if err != nil {
		t.Fatalf("ReadPolicy falhou: %v", err)
	}
	t.Logf("politica real: noAutoUpdate=%v auOptions=%v deferral=%v",
		settings.NoAutoUpdate, settings.AUOptions, settings.DeferQualityUpdatesPeriodInDays)
}

func TestSmokeScanRealWUA(t *testing.T) {
	smokeEnabled(t)

	client := New(DefaultOptions())
	state := client.ProbeState(context.Background())
	t.Logf("sonda real: service=%s online=%t policyManaged=%t rebootPending=%t",
		state.ServiceState, state.WuOnline, state.PolicyManaged, state.RebootPending)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	result, err := client.Scan(ctx, ScanOptions{IncludeInstalled: true})
	if result == nil {
		if err != nil {
			t.Skipf("scan WUA indisponivel nesta maquina: %v", err)
		}
		t.Fatal("scan devolveu resultado nulo sem erro")
	}
	if err != nil {
		t.Logf("scan parcial (%v) — validando o que veio", err)
	}

	t.Logf("scan real: pendentes=%d instaladas=%d ignorados=%d reboot=%t",
		result.PendingCount, result.InstalledCount, result.SkippedCount, result.PendingReboot.Pending)

	severities := map[string]int{}
	classifications := map[string]int{}
	updateTypes := map[string]int{}
	sources := map[string]int{}

	for _, update := range result.Updates {
		if strings.TrimSpace(update.UpdateKey) == "" {
			t.Fatalf("update sem chave canonica: %+v", update)
		}
		if update.Severity != "" {
			switch update.SeveritySource {
			case SeveritySourceVendor, SeveritySourceDerived, SeveritySourceUnknown:
			default:
				t.Fatalf("severidade sem proveniencia valida: %+v", update)
			}
			if update.SeveritySource == SeveritySourceVendor {
				canonical := canonicalSeverities[strings.ToLower(update.Severity)]
				if canonical == "" {
					t.Fatalf("severidade vendor fora do dominio: %q", update.Severity)
				}
			}
			if update.SeveritySource == SeveritySourceDerived && update.Severity != "Important" {
				t.Fatalf("severidade derivada deveria ter teto Important: %+v", update)
			}
		}
		if update.IsPending && strings.TrimSpace(update.Classification) == "" {
			t.Fatalf("update pendente sem classificacao: %+v", update)
		}
		switch update.UpdateType {
		case "feature", "driver", "software":
		default:
			t.Fatalf("updateType invalido: %q", update.UpdateType)
		}
		severities[update.Severity]++
		classifications[update.Classification]++
		updateTypes[update.UpdateType]++
		sources[update.Source]++
	}

	t.Logf("severidade: %v", severities)
	t.Logf("classificacao: %v", classifications)
	t.Logf("tipo: %v", updateTypes)
	t.Logf("origem: %v", sources)

	// A busca exclui drivers por padrao (source=wua).
	for _, update := range result.Updates {
		if update.Source == SourceWUA && isDriverUpdate(update) {
			t.Fatalf("driver vazou para o inventario: %+v", update)
		}
	}
}
