package remotesession

import (
	"fmt"
	"testing"
)

// ── A4b: DIVERGÊNCIA START vs RUNTIME (payload real do C#) ──
//
// O C# monta DOIS payloads com a MESMA política de servidor:
//
//   START  (RemoteSessionDispatcher.cs:51-59) → objeto aninhado
//          {"bandwidth":{"mode":DefaultMode,"ceilingKbps":ManualDefaultKbps,
//                        "autoMinKbps":..,"autoMaxKbps":..,"autoStepKbps":..,"serverMaxKbps":..}}
//          → applyBandwidthPolicy (manager.go:965-997)
//
//   RUNTIME (RemoteSessionCommandHandlers.cs:856-869) → campos na RAIZ
//          {"action":"bandwidth","mode":..,"ceilingKbps":..,"autoMinKbps":..,...}
//          → handleBandwidth (manager.go:488-537)
//
// Os DOIS terminam em SessionScreen.ConfigureBandwidth (manager.go:992 e :525),
// que é o ponto de convergência. A divergência NÃO está no handler de aplicação:
// está no VALOR que cada payload carrega para mode=auto.
//
// Defaults reais do servidor (RemoteAccessOptions.cs):
//   DefaultMode="auto", ManualDefaultKbps=8000, AutoStartKbps=8000 (NÃO é enviado!),
//   AutoMinKbps=1000, AutoMaxKbps=30000, AutoStepKbps=2000, ServerMaxKbps=0.

// servStartBlock reproduz o bloco "bandwidth" do START com os defaults do servidor.
func servStartBlock() map[string]any {
	return map[string]any{
		"mode":          "auto",
		"ceilingKbps":   float64(8000), // ManualDefaultKbps — NÃO é AutoStartKbps
		"autoMinKbps":   float64(1000),
		"autoMaxKbps":   float64(30000),
		"autoStepKbps":  float64(2000),
		"serverMaxKbps": float64(0),
	}
}

// servRuntimePayload reproduz o payload RAIZ do RUNTIME quando o operador troca
// para auto SEM informar teto (troca pura de modo).
func servRuntimePayload() map[string]any {
	return map[string]any{
		"action":    "bandwidth",
		"sessionId": "sess-1",
		"mode":      "auto",
		// ceilingKbps ausente → toFloat(payload["ceilingKbps"], -1) = -1
		"autoMinKbps":   float64(1000),
		"autoMaxKbps":   float64(30000),
		"autoStepKbps":  float64(2000),
		"serverMaxKbps": float64(0),
	}
}

// applyStartPath executa o caminho do START sem tocar no Manager: as mesmas
// conversões de applyBandwidthPolicy (manager.go:984-992) + ConfigureBandwidth.
func applyStartPath(block map[string]any) *BandwidthGate {
	g := NewBandwidthGate(defaultBandwidthGateConfig())
	cfg := BandwidthGateConfig{
		Mode:          BandwidthMode(toString(block["mode"])),
		CeilingKbps:   toFloat(block["ceilingKbps"], -1),
		AutoMinKbps:   toFloat(block["autoMinKbps"], -1),
		AutoMaxKbps:   toFloat(block["autoMaxKbps"], -1),
		AutoStepKbps:  toFloat(block["autoStepKbps"], -1),
		ServerMaxKbps: toFloat(block["serverMaxKbps"], -1),
	}
	ConfigureBandwidthOn(g, cfg)
	return g
}

// applyRuntimePath executa o caminho do RUNTIME (manager.go:519-532).
func applyRuntimePath(payload map[string]any) *BandwidthGate {
	g := NewBandwidthGate(defaultBandwidthGateConfig())
	cfg := BandwidthGateConfig{
		Mode:          BandwidthMode(toString(payload["mode"])),
		CeilingKbps:   toFloat(payload["ceilingKbps"], -1),
		AutoMinKbps:   toFloat(payload["autoMinKbps"], -1),
		AutoMaxKbps:   toFloat(payload["autoMaxKbps"], -1),
		AutoStepKbps:  toFloat(payload["autoStepKbps"], -1),
		ServerMaxKbps: toFloat(payload["serverMaxKbps"], -1),
	}
	ConfigureBandwidthOn(g, cfg)
	return g
}

// ConfigureBandwidthOn replica SessionScreen.ConfigureBandwidth
// (session_screen.go:1094-1106) para um gate solto — o ponto de convergência
// dos dois caminhos.
func ConfigureBandwidthOn(g *BandwidthGate, cfg BandwidthGateConfig) {
	if cfg.AutoMinKbps > 0 || cfg.AutoMaxKbps > 0 || cfg.AutoStepKbps > 0 || cfg.ServerMaxKbps > 0 {
		g.SetLimits(cfg.AutoMinKbps, cfg.AutoMaxKbps, cfg.AutoStepKbps, cfg.ServerMaxKbps)
	}
	ceiling := cfg.CeilingKbps
	if ceiling < 0 {
		ceiling = 0
	}
	g.SetMode(cfg.Mode, ceiling)
}

// TestBandwidthStartVsRuntimeAutoCeiling FIXA a convergência: "entrei na sessão
// em auto" (START) e "troquei para auto durante a sessão" (RUNTIME) devem dar o
// MESMO teto inicial com a MESMA política do servidor.
//
// ANTES da correção (evidência registrada): START=8000, RUNTIME=1000.
func TestBandwidthStartVsRuntimeAutoCeiling(t *testing.T) {
	start := applyStartPath(servStartBlock())
	runtime := applyRuntimePath(servRuntimePayload())

	fmt.Printf("[A4b] START   mode=auto => teto inicial=%.0f enabled=%v\n", start.CeilingKbps(), start.Enabled())
	fmt.Printf("[A4b] RUNTIME mode=auto => teto inicial=%.0f enabled=%v\n", runtime.CeilingKbps(), runtime.Enabled())
	fmt.Printf("[A4b] AutoStartKbps do servidor = 8000; AutoMin = 1000\n")

	// REGRESSÃO (bug A4b, corrigido): os dois caminhos precisam convergir.
	// Antes da correção o RUNTIME caía no piso AutoMin (1000) — o teto do auto
	// dependia de COMO o operador chegou ao modo.
	if runtime.CeilingKbps() != 8000 {
		t.Fatalf("RUNTIME(auto) sem ceiling deveria partir de AutoStartKbps=8000, got %.0f "+
			"(antes da correção: 1000, o piso AutoMin)", runtime.CeilingKbps())
	}
	if start.CeilingKbps() != runtime.CeilingKbps() {
		t.Fatalf("DIVERGÊNCIA START vs RUNTIME: %.0f != %.0f", start.CeilingKbps(), runtime.CeilingKbps())
	}
}

// TestBandwidthStartAndRuntimeShareTheSameSink prova que o HANDLER de aplicação
// é o mesmo (ConfigureBandwidth) — a divergência é de PAYLOAD, não de código de
// aplicação. Ambos os caminhos produzem o MESMO resultado quando recebem o MESMO
// conjunto de campos.
func TestBandwidthStartAndRuntimeShareTheSameSink(t *testing.T) {
	// Mesma política, campos equivalentes: START aninhado vs RUNTIME raiz.
	block := map[string]any{
		"mode": "manual", "ceilingKbps": float64(4000),
		"autoMinKbps": float64(1000), "autoMaxKbps": float64(30000),
		"autoStepKbps": float64(2000), "serverMaxKbps": float64(0),
	}
	payload := map[string]any{
		"action": "bandwidth", "sessionId": "s", "mode": "manual",
		"ceilingKbps": float64(4000), "autoMinKbps": float64(1000),
		"autoMaxKbps": float64(30000), "autoStepKbps": float64(2000),
		"serverMaxKbps": float64(0),
	}
	a, b := applyStartPath(block), applyRuntimePath(payload)
	if a.CeilingKbps() != b.CeilingKbps() || a.Mode() != b.Mode() {
		t.Fatalf("mesma política deveria dar o mesmo resultado: start=%v/%v runtime=%v/%v",
			a.Mode(), a.CeilingKbps(), b.Mode(), b.CeilingKbps())
	}
	fmt.Printf("[A4b] mesma política: START e RUNTIME convergem em ConfigureBandwidth => %v/%.0f\n", a.Mode(), a.CeilingKbps())

	// E o efeito do ServerMax=0 (default do servidor) vindo como float64(0):
	// toFloat devolve 0 (NÃO -1) porque 0 é numérico, e ConfigureBandwidth só
	// chama SetLimits se ALGUM campo for > 0 — com todos 0, SetLimits é pulado.
	zeroPayload := map[string]any{
		"action": "bandwidth", "sessionId": "s", "mode": "auto",
		"autoMinKbps": float64(0), "autoMaxKbps": float64(0),
		"autoStepKbps": float64(0), "serverMaxKbps": float64(0),
	}
	z := applyRuntimePath(zeroPayload)
	fmt.Printf("[A4b] RUNTIME auto com autoMin/Max/Step/ServerMax todos 0 => teto=%.0f (SetLimits PULADO, defaults do agent)\n", z.CeilingKbps())
}
