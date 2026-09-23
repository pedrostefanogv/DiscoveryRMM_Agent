//go:build windows

package inventory

import (
	"math"
	"testing"
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
