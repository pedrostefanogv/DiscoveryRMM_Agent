package power

import "testing"

// TestMapRaw cobre a tradução dos valores crus de SYSTEM_POWER_STATUS para o
// estado normalizado. Os casos 255 (desconhecido) e 128 (sem bateria) são os
// que mais importam: tratá-los como "na bateria" bloquearia deploy sem motivo,
// e tratá-los como "com bateria" mostraria um notebook falso no painel.
func TestMapRaw(t *testing.T) {
	cases := []struct {
		name        string
		raw         RawStatus
		wantKnown   bool
		wantBattery bool
		wantOnAC    *bool
		wantCharg   *bool
		wantPercent *int
	}{
		{
			name:        "notebook na tomada carregando",
			raw:         RawStatus{ACLineStatus: 1, BatteryFlag: 8, BatteryLifePercent: 42},
			wantKnown:   true,
			wantBattery: true,
			wantOnAC:    boolp(true),
			wantCharg:   boolp(true),
			wantPercent: intp(42),
		},
		{
			name:        "notebook na bateria descarregando",
			raw:         RawStatus{ACLineStatus: 0, BatteryFlag: 1, BatteryLifePercent: 37},
			wantKnown:   true,
			wantBattery: true,
			wantOnAC:    boolp(false),
			wantCharg:   boolp(false),
			wantPercent: intp(37),
		},
		{
			name:        "desktop sem bateria (128) — na tomada",
			raw:         RawStatus{ACLineStatus: 1, BatteryFlag: 128, BatteryLifePercent: 255},
			wantKnown:   true,
			wantBattery: false,
			wantOnAC:    boolp(true),
			wantCharg:   nil,
			wantPercent: nil,
		},
		{
			name:        "flag desconhecida (255) não afirma bateria nem carregamento",
			raw:         RawStatus{ACLineStatus: 255, BatteryFlag: 255, BatteryLifePercent: 255},
			wantKnown:   false,
			wantBattery: false,
			wantOnAC:    nil,
			wantCharg:   nil,
			wantPercent: nil,
		},
		{
			name:        "0% é valor válido, não desconhecido",
			raw:         RawStatus{ACLineStatus: 0, BatteryFlag: 4, BatteryLifePercent: 0},
			wantKnown:   true,
			wantBattery: true,
			wantOnAC:    boolp(false),
			wantCharg:   boolp(false),
			wantPercent: intp(0),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MapRaw(tc.raw)

			if got.BatteryKnown != tc.wantKnown {
				t.Errorf("BatteryKnown = %v, esperado %v", got.BatteryKnown, tc.wantKnown)
			}
			if got.HasBattery != tc.wantBattery {
				t.Errorf("HasBattery = %v, esperado %v", got.HasBattery, tc.wantBattery)
			}
			assertBoolPtr(t, "OnAC", got.OnAC, tc.wantOnAC)
			assertBoolPtr(t, "Charging", got.Charging, tc.wantCharg)
			if tc.wantPercent == nil {
				if got.Percent != nil {
					t.Errorf("Percent = %v, esperado nil", *got.Percent)
				}
			} else if got.Percent == nil || *got.Percent != *tc.wantPercent {
				t.Errorf("Percent = %v, esperado %v", got.Percent, *tc.wantPercent)
			}
		})
	}
}

// TestMapRaw_TemposDesconhecidos garante que 0xFFFFFFFF não vira "0 minutos".
func TestMapRaw_TemposDesconhecidos(t *testing.T) {
	got := MapRaw(RawStatus{
		ACLineStatus: 1, BatteryFlag: 8, BatteryLifePercent: 50,
		BatteryLifeTime: lifeTimeUnknown, BatteryFullLifeTime: lifeTimeUnknown,
	})
	if got.MinutesLeft != nil {
		t.Errorf("MinutesLeft = %v, esperado nil", *got.MinutesLeft)
	}
	if got.MinutesToFull != nil {
		t.Errorf("MinutesToFull = %v, esperado nil", *got.MinutesToFull)
	}
}

func boolp(v bool) *bool { return &v }
func intp(v int) *int    { return &v }

func assertBoolPtr(t *testing.T, name string, got, want *bool) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %v, esperado nil", name, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, esperado %v", name, *want)
	case want != nil && got != nil && *want != *got:
		t.Errorf("%s = %v, esperado %v", name, *got, *want)
	}
}
