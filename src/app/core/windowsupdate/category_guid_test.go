package windowsupdate

import "testing"

const (
	guidSecurity   = "0FA1201D-4330-4FA8-8AE9-B877473B6441"
	guidDefinition = "E0789628-CE08-4437-BE74-2495B842F43B"
	guidDrivers    = "EBFC1FC5-71A4-4F7B-9ACA-3B9A503104A0"
	guidDriverSets = "07213788-A764-4820-90B7-11358A57828E"
	guidFeaturePks = "B54E7D24-7ADD-428F-8B75-90A396FA584F"
	guidUpdates    = "CD5FFD1E-E932-4E3A-BF74-18BF0B1BBD83"
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
	if !IsDriverCategory([]string{guidDriverSets}, []string{"Conjuntos de Drivers"}) {
		t.Fatal("GUID de Driver Sets deveria ser reconhecido como driver")
	}
}

func TestClassifyUpdatesGuidAndFeatureTypes(t *testing.T) {
	if got := ClassifyCategory([]string{guidProduct, guidUpdates}, []string{"Windows 11", "Atualizações"}); got != "Updates" {
		t.Fatalf("GUID Updates deveria resolver para Updates em vez do produto; got %q", got)
	}
	if got := DeriveUpdateType([]string{guidFeaturePks}, []string{"Pacotes de Recursos"}); got != "feature" {
		t.Fatalf("Feature Packs deveria derivar feature; got %q", got)
	}
	for _, title := range []string{
		"Feature update to Windows 11, version 24H2",
		"Atualização de recursos para o Windows 11, versão 24H2",
		"Windows 11, version 25H2",
		"Windows Experience Feature Experience Pack",
	} {
		if got := DeriveUpdateTypeWithTitle([]string{guidUpdates}, []string{"Atualizações"}, title); got != "feature" {
			t.Errorf("titulo %q deveria derivar feature; got %q", title, got)
		}
	}
	canonical := CanonicalCategoryNames([]string{guidSecurity, guidUpdates}, []string{"Atualizações de Segurança", "Windows 11"})
	if len(canonical) < 3 || canonical[0] != "Security Updates" || canonical[1] != "Updates" {
		t.Fatalf("CanonicalCategoryNames inesperado: %v", canonical)
	}
}
