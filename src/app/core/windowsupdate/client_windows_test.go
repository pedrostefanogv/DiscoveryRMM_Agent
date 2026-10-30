//go:build windows

package windowsupdate

// Leitura das propriedades inteiras do WUA via late binding. MaxDownloadSize e
// IUpdate.MaxDownloadSize (bytes) e chega como VT_I4/VT_UI4/VT_I8: precisa ser
// lido sem truncar (tamanho acima de 2 GiB nao pode virar negativo) e sem
// derrubar o update quando a propriedade falha.

import (
	"strings"
	"testing"

	ole "github.com/go-ole/go-ole"
)

func TestPendingCriteriaIncludeOptionalUpdatesAndUpgrades(t *testing.T) {
	criteria := pendingCriteria(false, false)
	if len(criteria) == 0 {
		t.Fatal("pendingCriteria vazio")
	}
	for _, criterion := range criteria {
		lower := strings.ToLower(criterion)
		// BrowseOnly=0 escondia atualizacoes opcionais (feature updates).
		if strings.Contains(lower, "browseonly") {
			t.Fatalf("criterio ainda filtra BrowseOnly (opcionais): %q", criterion)
		}
		if !strings.Contains(lower, "isinstalled=0") {
			t.Fatalf("criterio sem IsInstalled=0: %q", criterion)
		}
	}
	primary := strings.ToLower(criteria[0])
	if !strings.Contains(primary, "ishidden=0") {
		t.Fatalf("criterio primario deveria excluir ocultos: %q", criteria[0])
	}
	// Type='Software' e mantido para nao ler drivers em todo scan (custo).
	if !strings.Contains(primary, "type='software'") {
		t.Fatalf("criterio primario deveria manter Type='Software': %q", criteria[0])
	}

	withHidden := pendingCriteria(true, false)
	if len(withHidden) == 0 || withHidden[0] != "IsInstalled=0 and Type='Software'" {
		t.Fatalf("includeHidden=%v want primeiro criterio sem IsHidden e com Type=Software", withHidden)
	}

	withDrivers := pendingCriteria(false, true)
	if len(withDrivers) == 0 || withDrivers[0] != "IsInstalled=0 and IsHidden=0" {
		t.Fatalf("includeDrivers=%v want primeiro criterio sem Type", withDrivers)
	}
}

// TestBetterScanResultPrefersMoreIncluded: entre variantes de criterio de
// busca, escolhe mais incluidos e, em empate, menos unreadable. O WUA lista o
// mesmo pendente de formas diferentes por criterio; a variante que descarta o
// item por falha de leitura nao pode vencer (pendentes invisiveis no servico).
func TestBetterScanResultPrefersMoreIncluded(t *testing.T) {
	cases := []struct {
		name   string
		cand   ScanCounters
		best   ScanCounters
		expect bool
	}{
		{name: "mais incluidos vence", cand: ScanCounters{Included: 2}, best: ScanCounters{Included: 1}, expect: true},
		{name: "empate com menos unreadable vence", cand: ScanCounters{Included: 1, Skipped: map[string]int{SkipReasonUnreadable: 0}}, best: ScanCounters{Included: 1, Skipped: map[string]int{SkipReasonUnreadable: 1}}, expect: true},
		{name: "empate completo mantem o primeiro", cand: ScanCounters{Included: 1, Read: 1}, best: ScanCounters{Included: 1, Read: 1}, expect: false},
		{name: "mesmo incluidos, mais unreadable perde", cand: ScanCounters{Included: 1, Skipped: map[string]int{SkipReasonUnreadable: 2}}, best: ScanCounters{Included: 1, Skipped: map[string]int{SkipReasonUnreadable: 1}}, expect: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := betterScanResult(tc.cand, tc.best); got != tc.expect {
				t.Fatalf("betterScanResult=%v want %v", got, tc.expect)
			}
		})
	}
}

func TestVariantInt64ReadsMaxDownloadSizeTypes(t *testing.T) {
	cases := []struct {
		name string
		v    *ole.VARIANT
		want int64
	}{
		{name: "VT_I4", v: &ole.VARIANT{VT: ole.VT_I4, Val: 1_234_567}, want: 1_234_567},
		{name: "VT_UI4 acima de 2GiB", v: &ole.VARIANT{VT: ole.VT_UI4, Val: 4_000_000_000}, want: 4_000_000_000},
		{name: "VT_I8", v: &ole.VARIANT{VT: ole.VT_I8, Val: 5_000_000_000}, want: 5_000_000_000},
		{name: "VT_UI2", v: &ole.VARIANT{VT: ole.VT_UI2, Val: 4096}, want: 4096},
		{name: "nulo", v: nil, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := variantInt64(tc.v); got != tc.want {
				t.Fatalf("variantInt64=%d want %d", got, tc.want)
			}
		})
	}
}
