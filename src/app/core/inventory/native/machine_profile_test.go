//go:build windows

package native

import (
	"testing"

	"discovery/app/core/models"
)

// TestClassifyMachineProfile trava a REGRA de detecção de notebook. Os casos
// de "desconhecido" são tão importantes quanto os positivos: VM/agente antigo
// sem WMI não pode virar "desktop" por omissão no painel.
func TestClassifyMachineProfile(t *testing.T) {
	cases := []struct {
		name           string
		pcSystemType   int
		hasPCSystem    bool
		hasBattery     *bool
		isServer       bool
		wantLaptop     *bool
		wantMachineTyp string
	}{
		{
			name:           "desktop (PCSystemType 1, sem bateria)",
			pcSystemType:   1,
			hasPCSystem:    true,
			hasBattery:     boolPtr(false),
			wantLaptop:     boolPtr(false),
			wantMachineTyp: models.MachineTypeDesktop,
		},
		{
			name:           "notebook (PCSystemType 2)",
			pcSystemType:   2,
			hasPCSystem:    true,
			hasBattery:     boolPtr(true),
			wantLaptop:     boolPtr(true),
			wantMachineTyp: models.MachineTypeNotebook,
		},
		{
			name:           "OEM que reporta 1 em notebook, mas com bateria",
			pcSystemType:   1,
			hasPCSystem:    true,
			hasBattery:     boolPtr(true),
			wantLaptop:     boolPtr(true),
			wantMachineTyp: models.MachineTypeNotebook,
		},
		{
			name:           "tablet/convertible (PCSystemType 9)",
			pcSystemType:   9,
			hasPCSystem:    true,
			hasBattery:     boolPtr(true),
			wantLaptop:     boolPtr(true),
			wantMachineTyp: models.MachineTypeTablet,
		},
		{
			name:           "Windows Server nunca é notebook",
			pcSystemType:   1,
			hasPCSystem:    true,
			hasBattery:     boolPtr(true),
			isServer:       true,
			wantLaptop:     boolPtr(false),
			wantMachineTyp: models.MachineTypeServer,
		},
		{
			name:           "classe de servidor no hardware vence a bateria (UPS)",
			pcSystemType:   4,
			hasPCSystem:    true,
			hasBattery:     boolPtr(true),
			wantLaptop:     boolPtr(false),
			wantMachineTyp: models.MachineTypeServer,
		},
		{
			name:           "sem WMI e sem bateria legível ⇒ desconhecido",
			hasPCSystem:    false,
			hasBattery:     nil,
			wantLaptop:     nil,
			wantMachineTyp: models.MachineTypeUnknown,
		},
		{
			name:           "PCSystemType 2 sem bateria legível ainda é notebook",
			pcSystemType:   2,
			hasPCSystem:    true,
			hasBattery:     nil,
			wantLaptop:     boolPtr(true),
			wantMachineTyp: models.MachineTypeNotebook,
		},
		{
			name:           "desktop só com ausência de bateria CONFIRMADA",
			pcSystemType:   1,
			hasPCSystem:    true,
			hasBattery:     nil,
			wantLaptop:     nil,
			wantMachineTyp: models.MachineTypeUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyMachineProfile(tc.pcSystemType, tc.hasPCSystem, tc.hasBattery, tc.isServer)

			if tc.wantMachineTyp != got.MachineType {
				t.Errorf("MachineType = %q, esperado %q", got.MachineType, tc.wantMachineTyp)
			}
			switch {
			case tc.wantLaptop == nil && got.IsLaptop != nil:
				t.Errorf("IsLaptop = %v, esperado nil (desconhecido)", *got.IsLaptop)
			case tc.wantLaptop != nil && got.IsLaptop == nil:
				t.Errorf("IsLaptop = nil, esperado %v", *tc.wantLaptop)
			case tc.wantLaptop != nil && got.IsLaptop != nil && *tc.wantLaptop != *got.IsLaptop:
				t.Errorf("IsLaptop = %v, esperado %v", *got.IsLaptop, *tc.wantLaptop)
			}
			if tc.hasPCSystem && got.PCSystemType == nil {
				t.Errorf("PCSystemType lido deveria ser propagado")
			}
		})
	}
}

// TestClassifyMachineProfile_ServerIgnoraBateria é o caso do servidor com UPS
// exposta como Win32_Battery: continua servidor, nunca notebook.
func TestClassifyMachineProfile_ServerIgnoraBateria(t *testing.T) {
	got := classifyMachineProfile(0, false, boolPtr(true), true)
	if got.MachineType != models.MachineTypeServer {
		t.Fatalf("MachineType = %q, esperado server", got.MachineType)
	}
	if got.IsLaptop == nil || *got.IsLaptop {
		t.Fatalf("IsLaptop = %v, esperado false", got.IsLaptop)
	}
}

// TestWmiValueInt_DistingueAusenteDeZero garante que um campo AUSENTE não vira
// pcSystemType 0 e, principalmente, que 0 não é confundido com "lido".
func TestWmiValueInt_DistingueAusenteDeZero(t *testing.T) {
	row := map[string]any{"PCSystemType": int64(2), "Outro": "x"}
	if got := wmiValueInt(row, "PCSystemType"); got != 2 {
		t.Errorf("PCSystemType = %d, esperado 2", got)
	}
	if got := wmiValueInt(row, "Inexistente"); got != 0 {
		t.Errorf("campo ausente = %d, esperado 0", got)
	}
	if got := wmiValueInt(map[string]any{"Vazio": nil}, "Vazio"); got != 0 {
		t.Errorf("campo nil = %d, esperado 0", got)
	}
	if got := wmiValueInt(map[string]any{"Texto": " 10 "}, "Texto"); got != 10 {
		t.Errorf("campo texto = %d, esperado 10", got)
	}
}

func boolPtr(v bool) *bool { return &v }
