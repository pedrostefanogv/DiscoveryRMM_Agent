//go:build windows

package inventory

import (
	"context"
	"math"
	"testing"
	"time"

	"discovery/app/core/inventory/native"
)

// TestConvertThermalRawToCelsius cobre a regressão principal do coletor de
// temperatura: o valor cru do sensor (Kelvin ou décimos de Kelvin) não pode
// ser truncado como se fosse percentual.
func TestConvertThermalRawToCelsius(t *testing.T) {
	tests := []struct {
		name string
		raw  float64
		want float64
		ok   bool
	}{
		{"decimos de kelvin (formato WMI/PDH)", 3180, 44.85, true},
		{"kelvin inteiro (alguns drivers)", 318, 44.85, true},
		{"limite inferior valido (0 C)", 273.15, 0, true},
		{"zero e invalido", 0, -1, false},
		{"negativo e invalido", -10, -1, false},
		{"fora da faixa plausivel", 99999, -1, false},
		{"valor ambiguo (nem K nem decimos)", 500, -1, false},
		{"abaixo da faixa (150 K)", 1500, -1, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := convertThermalRawToCelsius(tc.raw)
			if ok != tc.ok {
				t.Fatalf("convertThermalRawToCelsius(%v): ok=%v, want %v", tc.raw, ok, tc.ok)
			}
			if ok && math.Abs(got-tc.want) > 0.05 {
				t.Fatalf("convertThermalRawToCelsius(%v)=%v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestRoundTo1Decimal(t *testing.T) {
	if got := roundTo1Decimal(44.86); got != 44.9 {
		t.Fatalf("roundTo1Decimal(44.86)=%v, want 44.9", got)
	}
	if got := roundTo1Decimal(math.NaN()); got != -1 {
		t.Fatalf("roundTo1Decimal(NaN)=%v, want -1", got)
	}
}

func TestConvertProbeRawToCelsius(t *testing.T) {
	tests := []struct {
		name string
		raw  float64
		want float64
		ok   bool
	}{
		{"decimos de celsius", 450, 45.0, true},
		{"celsius inteiro", 45, 45.0, true},
		{"zero e invalido", 0, -1, false},
		{"negativo e invalido", -5, -1, false},
		{"fora da faixa", 5000, -1, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := convertProbeRawToCelsius(tc.raw)
			if ok != tc.ok {
				t.Fatalf("convertProbeRawToCelsius(%v): ok=%v, want %v", tc.raw, ok, tc.ok)
			}
			if ok && math.Abs(got-tc.want) > 0.05 {
				t.Fatalf("convertProbeRawToCelsius(%v)=%v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSelectBestThermalReading(t *testing.T) {
	t.Run("unidades mistas (kelvin inteiro vs decimos de kelvin)", func(t *testing.T) {
		readings := []native.ThermalReading{
			{Source: "wmi_acpi", Name: "TZ10", RawValue: 2900, Unit: native.ThermalUnitAutoKelvin}, // 16.9°C
			{Source: "wmi_perf", Name: "TZ01", RawValue: 330, Unit: native.ThermalUnitAutoKelvin},  // 56.9°C
		}
		got, ok := selectBestThermalReading(readings)
		if !ok {
			t.Fatalf("selectBestThermalReading returned ok=false, want true")
		}
		if math.Abs(got-56.9) > 0.1 {
			t.Fatalf("selectBestThermalReading = %v, want ~56.9", got)
		}
	})

	t.Run("sensor invalido nao mascara sensor valido", func(t *testing.T) {
		readings := []native.ThermalReading{
			{Source: "wmi_acpi", Name: "BOGUS", RawValue: 99999, Unit: native.ThermalUnitAutoKelvin},
			{Source: "wmi_acpi", Name: "CPU", RawValue: 3215, Unit: native.ThermalUnitAutoKelvin}, // 48.4°C
		}
		got, ok := selectBestThermalReading(readings)
		if !ok {
			t.Fatalf("selectBestThermalReading returned ok=false, want true")
		}
		if math.Abs(got-48.4) > 0.1 {
			t.Fatalf("selectBestThermalReading = %v, want ~48.4", got)
		}
	})

	t.Run("vazio ou todos invalidos", func(t *testing.T) {
		if got, ok := selectBestThermalReading(nil); ok || got != -1 {
			t.Fatalf("selectBestThermalReading(nil) = (%v, %v), want (-1, false)", got, ok)
		}
		bad := []native.ThermalReading{
			{Source: "wmi_acpi", RawValue: 0, Unit: native.ThermalUnitAutoKelvin},
			{Source: "wmi_acpi", RawValue: 99999, Unit: native.ThermalUnitAutoKelvin},
		}
		if got, ok := selectBestThermalReading(bad); ok || got != -1 {
			t.Fatalf("selectBestThermalReading(bad) = (%v, %v), want (-1, false)", got, ok)
		}
	})
}

func TestSelectBestHardwareMonitorCPUTemp(t *testing.T) {
	t.Run("intel prefere CPU Package e ignora GPU/NVMe/VRM", func(t *testing.T) {
		readings := []native.ThermalReading{
			{Source: "librehwm", Identifier: "/gpu-nvidia/0/temperature/0", Name: "GPU Core", RawValue: 82.0, Unit: native.ThermalUnitCelsius},
			{Source: "librehwm", Identifier: "/nvme/0/temperature/0", Name: "Temperature", RawValue: 70.0, Unit: native.ThermalUnitCelsius},
			{Source: "librehwm", Identifier: "/lpc/nct6798d/temperature/2", Name: "VRM MOS", RawValue: 75.0, Unit: native.ThermalUnitCelsius},
			{Source: "librehwm", Identifier: "/intelcpu/0/temperature/0", Name: "CPU Core #1", RawValue: 54.0, Unit: native.ThermalUnitCelsius},
			{Source: "librehwm", Identifier: "/intelcpu/0/temperature/3", Name: "Core Average", RawValue: 56.0, Unit: native.ThermalUnitCelsius},
			{Source: "librehwm", Identifier: "/intelcpu/0/temperature/4", Name: "CPU Package", RawValue: 61.5, Unit: native.ThermalUnitCelsius},
		}
		got, ok := selectBestHardwareMonitorCPUTemp(readings)
		if !ok {
			t.Fatalf("selectBestHardwareMonitorCPUTemp returned ok=false, want true")
		}
		if got != 61.5 {
			t.Fatalf("selectBestHardwareMonitorCPUTemp = %v, want 61.5", got)
		}
	})

	t.Run("amd ryzen prefere Tctl/Tdie", func(t *testing.T) {
		readings := []native.ThermalReading{
			{Source: "librehwm", Identifier: "/amdcpu/0/temperature/3", Name: "CCD1 (Tdie)", RawValue: 59.2, Unit: native.ThermalUnitCelsius},
			{Source: "librehwm", Identifier: "/amdcpu/0/temperature/2", Name: "Core (Tctl/Tdie)", RawValue: 64.3, Unit: native.ThermalUnitCelsius},
			{Source: "librehwm", Identifier: "/lpc/it8688e/temperature/0", Name: "Motherboard", RawValue: 40.0, Unit: native.ThermalUnitCelsius},
		}
		got, ok := selectBestHardwareMonitorCPUTemp(readings)
		if !ok {
			t.Fatalf("selectBestHardwareMonitorCPUTemp returned ok=false, want true")
		}
		if got != 64.3 {
			t.Fatalf("selectBestHardwareMonitorCPUTemp = %v, want 64.3", got)
		}
	})

	t.Run("intel ignora Distance to TjMax", func(t *testing.T) {
		readings := []native.ThermalReading{
			{Source: "librehwm", Identifier: "/intelcpu/0/temperature/5", Name: "Core Max Distance to TjMax", RawValue: 70.0, Unit: native.ThermalUnitCelsius},
			{Source: "librehwm", Identifier: "/intelcpu/0/temperature/6", Name: "CPU Core #1 Distance to TjMax", RawValue: 72.0, Unit: native.ThermalUnitCelsius},
			{Source: "librehwm", Identifier: "/intelcpu/0/temperature/4", Name: "CPU Package", RawValue: 30.0, Unit: native.ThermalUnitCelsius},
		}
		got, ok := selectBestHardwareMonitorCPUTemp(readings)
		if !ok {
			t.Fatalf("selectBestHardwareMonitorCPUTemp returned ok=false, want true")
		}
		if got != 30.0 {
			t.Fatalf("selectBestHardwareMonitorCPUTemp = %v, want 30.0", got)
		}
	})
}

func TestCollectCPUTemperatureWMI_CancelledContextDoesNotTriggerBackoff(t *testing.T) {
	cpuTempWMIMu.Lock()
	prevLastAt := cpuTempWMILastAt
	prevLastVal := cpuTempWMILastVal
	prevRetryAt := cpuTempWMIRetryAt
	cpuTempWMILastAt = time.Time{}
	cpuTempWMILastVal = -1
	cpuTempWMIRetryAt = time.Time{}
	cpuTempWMIMu.Unlock()

	defer func() {
		cpuTempWMIMu.Lock()
		cpuTempWMILastAt = prevLastAt
		cpuTempWMILastVal = prevLastVal
		cpuTempWMIRetryAt = prevRetryAt
		cpuTempWMIMu.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, ok := collectCPUTemperatureWMI(ctx)
	if ok || got != -1 {
		t.Fatalf("collectCPUTemperatureWMI(cancelled) = (%v, %v), want (-1, false)", got, ok)
	}

	cpuTempWMIMu.Lock()
	retryAt := cpuTempWMIRetryAt
	cpuTempWMIMu.Unlock()
	if !retryAt.IsZero() {
		t.Fatalf("cpuTempWMIRetryAt = %v, want zero (no backoff on cancelled context)", retryAt)
	}
}

func TestCollectCPUTemperatureNative_Live(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pdhTemp, pdhOk := collectCPUTemperatureNative()
	t.Logf("PDH wildcard temp: ok=%v celsius=%v", pdhOk, pdhTemp)
	if pdhOk && (pdhTemp < 0 || pdhTemp > 125) {
		t.Fatalf("PDH temp out of range: %v", pdhTemp)
	}

	wmiTemp, wmiOk := collectCPUTemperatureWMI(ctx)
	t.Logf("COM/WMI temp: ok=%v celsius=%v", wmiOk, wmiTemp)
	if wmiOk && (wmiTemp < 0 || wmiTemp > 125) {
		t.Fatalf("COM/WMI temp out of range: %v", wmiTemp)
	}
}
