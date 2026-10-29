package windowsupdate

import "testing"

const (
	guidSecurity   = "0FA1201D-4330-4FA8-8AE9-B877473B6441"
	guidDefinition = "E0789628-CE08-4437-BE74-2495B842F43B"
	guidDrivers    = "EBFC1FC5-71A4-4F7B-9ACA-3B9A503104A0"
	guidUpgrades   = "3689BDC8-B205-4AF4-8D4A-A63924C5E9D5"
	guidProduct    = "11111111-2222-3333-4444-555555555555"
)

func TestClassifyCategoryUsesGuidOverLocalizedName(t *testing.T) {
	// Nomes localizados (pt-BR) que o fallback textual NAO reconhece.
	names := []string{"Windows 11", "Atualizações de Segurança"}

	got := ClassifyCategory([]string{guidProduct, guidSecurity}, names)
	if got != "Security Updates" {
		t.Fatalf("GUID de seguranca deveria vencer o nome localizado; got %q", got)
	}
}

func TestClassifyCategoryFallsBackToNames(t *testing.T) {
	got := ClassifyCategory(nil, []string{"Windows 11", "Security Updates"})
	if got != "Security Updates" {
		t.Fatalf("sem GUID deve manter o fallback por nome; got %q", got)
	}

	got = ClassifyCategory([]string{guidProduct}, []string{"Windows 11"})
	if got != "Windows 11" {
		t.Fatalf("categoria de produto deve ser o ultimo recurso; got %q", got)
	}
}

func TestClassifyCategoryPriority(t *testing.T) {
	got := ClassifyCategory([]string{guidDefinition, guidSecurity}, []string{"Windows 11"})
	if got != "Security Updates" {
		t.Fatalf("security tem prioridade sobre definition; got %q", got)
	}
}

func TestDeriveUpdateType(t *testing.T) {
	cases := []struct {
		ids    []string
		names  []string
		expect string
	}{
		{[]string{guidUpgrades}, nil, "feature"},
		{[]string{guidDrivers}, nil, "driver"},
		{[]string{guidSecurity}, []string{"Security Updates"}, "software"},
		{nil, []string{"Upgrades"}, "feature"},
		{nil, nil, "software"},
	}
	for _, tc := range cases {
		if got := DeriveUpdateType(tc.ids, tc.names); got != tc.expect {
			t.Errorf("DeriveUpdateType(%v,%v) = %q; want %q", tc.ids, tc.names, got, tc.expect)
		}
	}
}

func TestDriverAndUpgradeByGuid(t *testing.T) {
	if !IsDriverCategory([]string{guidDrivers}, []string{"Controladores"}) {
		t.Fatal("GUID de driver deveria ser reconhecido mesmo com nome localizado")
	}
	if !IsUpgradeCategory([]string{guidUpgrades}, nil) {
		t.Fatal("GUID de upgrade deveria ser reconhecido")
	}
	if IsDriverCategory([]string{guidSecurity}, []string{"Security Updates"}) {
		t.Fatal("update de seguranca nao e driver")
	}
}
