//go:build windows

package native

import "testing"

func TestMapMonitorRows_MapsAndFallsBackToInstance(t *testing.T) {
	rows := []map[string]any{
		{"name": "AOC 24G2", "manufacturer": "AOC", "serial": "SN123", "status": "Active", "instance": "DISPLAY\\AOC2401\\5&abc"},
		{"name": "", "manufacturer": "", "serial": "", "instance": "DISPLAY\\BOE0C77\\4&2c0d"},
		{"name": "Dup", "serial": "X"},
		{"name": "Dup", "serial": "X"},
	}
	result := mapMonitorRows(rows)
	if len(result) != 3 {
		t.Fatalf("expected 3 monitors (dedupe + fallback), got %d", len(result))
	}
	if result[0].Name != "AOC 24G2" || result[0].Manufacturer != "AOC" || result[0].Serial != "SN123" {
		t.Errorf("monitor 1 mal mapeado: %+v", result[0])
	}
	if result[1].Name != "Monitor (BOE0C77)" {
		t.Errorf("fallback por InstanceName = %q, want Monitor (BOE0C77)", result[1].Name)
	}
}

func TestMapMonitorRows_NilAndEmpty(t *testing.T) {
	if got := mapMonitorRows(nil); got != nil {
		t.Errorf("mapMonitorRows(nil) = %v, want nil", got)
	}
	if got := mapMonitorRows([]map[string]any{{"name": "", "manufacturer": "", "serial": "", "instance": ""}}); got != nil {
		t.Errorf("linha totalmente vazia deve ser descartada, got %v", got)
	}
}
