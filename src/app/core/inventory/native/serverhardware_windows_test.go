//go:build windows

package native

import (
	"context"
	"testing"
	"time"
)

// TestIsDegradedRAIDState cobre a classificação de estado da controladora.
// O risco aqui é o FALSO POSITIVO: estados saudáveis não podem ser
// reportados como degradados, senão o alerta vira ruído e o operador para
// de confiar nele.
func TestIsDegradedRAIDState(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		// Saudáveis — nunca degradados.
		{"Optimal", false},
		{"optimal", false},
		{"ONLINE", false},
		{"OK", false},
		{"Good", false},
		{"Normal", false},
		{"", false},
		{"   ", false},
		// Degradados/falhos — sempre detectados.
		{"Degraded", true},
		{"degraded", true},
		{"Failed", true},
		{"Offline", true},
		{"Critical", true},
		{"Rebuild", true},
		{"Rbld", true},
		// Estado real do storcli durante reconstrução.
		{"Optimal, Rebuild", true},
	}
	for _, tc := range cases {
		if got := isDegradedRAIDState(tc.state); got != tc.want {
			t.Errorf("isDegradedRAIDState(%q) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

// TestParseRAIDJSONStorcli cobre o parse da saída JSON do storcli/perccli.
// O formato real é {"Controllers":[{"Response Data":{...}}]}.
func TestParseRAIDJSONStorcli(t *testing.T) {
	payload := `{"Controllers":[{"Command Status":{"Status":"Success"},` +
		`"Response Data":{"Product Name":"PERC H730","Virtual Drives":{` +
		`"0":{"VD0":"0/1","State":"Optimal"},` +
		`"1":{"VD1":"0/1","State":"Degraded"}},` +
		`"PDs for VD0":{"0":{"State":"Online"},` +
		`"1":{"State":"Failed"}}}}]}`

	report := &RAIDToolReport{Name: "perccli"}
	parseRAIDJSON(report, payload)

	if report.Controllers != 1 {
		t.Errorf("Controllers = %d, want 1", report.Controllers)
	}
	if report.VirtualDisks != 2 {
		t.Errorf("VirtualDisks = %d, want 2", report.VirtualDisks)
	}
	if report.PhysicalDisks != 2 {
		t.Errorf("PhysicalDisks = %d, want 2", report.PhysicalDisks)
	}
	// Um VD degradado e um PD falho = 2 entradas degradadas.
	if len(report.Degraded) != 2 {
		t.Errorf("Degraded = %v (len %d), want 2", report.Degraded, len(report.Degraded))
	}
}

// TestParseRAIDJSONTextoNaoPanica garante que a saída em TEXTO (MegaCli,
// ssacli, arcconf) não quebra o parse — só resulta em contagens zeradas.
func TestParseRAIDJSONTextoNaoPanica(t *testing.T) {
	report := &RAIDToolReport{Name: "MegaCli"}
	parseRAIDJSON(report, "Adapter #0 ; Product Name: PERC H730 ; State: Optimal")
	if report.Controllers != 0 || report.VirtualDisks != 0 || report.PhysicalDisks != 0 {
		t.Errorf("saida texto nao deveria preencher contagens: %+v", report)
	}
}

// TestParseRAIDJSONInvalidoNaoPanica cobre JSON truncado/corrompido.
func TestParseRAIDJSONInvalidoNaoPanica(t *testing.T) {
	for _, bad := range []string{"", "{", "[1,2,3]", "null", "{\"Controllers\":\"x\"}"} {
		report := &RAIDToolReport{Name: "storcli"}
		parseRAIDJSON(report, bad)
		if report.Controllers != 0 {
			t.Errorf("entrada %q deveria deixar Controllers=0", bad)
		}
	}
}

// TestProbeToolFileNaoPanica cobre o probador de caminho com entradas vazias.
func TestProbeToolFileNaoPanica(t *testing.T) {
	for _, dir := range []string{"", `C:\Windows\System32`, `Z:\nao-existe-xyz`} {
		_ = probeToolFile(dir, "storcli")
		_ = probeToolFile(dir, "")
	}
}

// TestCollectServerHardwareNaoPanica garante o contrato de robustez: o
// coletor roda no ciclo de inventário e não pode derrubar a coleta.
func TestCollectServerHardwareNaoPanica(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hw := CollectServerHardware(ctx)
	// Se nada foi encontrado, o motivo tem de estar preenchido — o servidor
	// precisa distinguir "não é servidor" de "é servidor sem ferramenta".
	if !serverHardwareAvailable(hw) && hw.Reason == "" {
		t.Error("resultado vazio deve registrar um motivo")
	}
}
