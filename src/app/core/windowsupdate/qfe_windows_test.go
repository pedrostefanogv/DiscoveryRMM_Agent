package windowsupdate

import (
	"testing"
	"time"
)

// Regressao do bug real (maquina 019faa6f): as linhas do Win32_QuickFixEngineering
// devem produzir titulos reais, chaves canonicas e datas parseadas. A extracao
// via COM e testada indiretamente aqui: a mesma estrutura de linha que o WMI
// devolve e alimentada na montagem dos Updates.
func TestBuildsInstalledUpdatesFromRows(t *testing.T) {
	rows := []map[string]any{
		{
			"HotFixID":    "KB5127690",
			"Caption":     "http://support.microsoft.com/?kbid=5127690",
			"Description": "Update",
			"InstalledOn": "2026-10-16 21:09:57",
		},
		{
			"HotFixID":    "5127846",
			"Caption":     "",
			"Description": "",
			"InstalledOn": "16/10/2026",
		},
		{
			"HotFixID": "KB5127690", // duplicada (QFE as vezes repete)
		},
		{
			"HotFixID": "lixo",
		},
	}
	updates := buildsInstalledUpdatesFromRows(rows)
	if len(updates) != 2 {
		t.Fatalf("len=%d want 2 (%+v)", len(updates), updates)
	}
	if updates[0].UpdateKey != "kb:KB5127690" {
		t.Fatalf("UpdateKey=%q", updates[0].UpdateKey)
	}
	if updates[0].Title != "Atualizacao KB5127690" {
		t.Fatalf("titulo da KB com caption URL=%q", updates[0].Title)
	}
	if updates[0].Source != SourceQFE || !updates[0].IsInstalled {
		t.Fatalf("metadados invalidos: %+v", updates[0])
	}
	if updates[0].InstalledAt == nil {
		t.Fatal("InstalledOn nao parseada")
	}
	if updates[1].InstalledAt == nil {
		t.Fatal("InstalledOn pt-BR nao parseada")
	}
}

func TestParseWMIInstalledOn(t *testing.T) {
	cases := map[string]bool{
		"20261016":                  true,
		"2026-10-16":                true,
		"2026-10-16 21:09:57":       true,
		"16/10/2026":                true,
		"10/16/2026":                true,
		"20261016210957.000000-180": true,
		"2026-10-16T21:09:57Z":      true,
		"":                          false,
		"ontem":                     false,
	}
	for raw, ok := range cases {
		got := parseWMIInstalledOn(raw)
		if ok && got == nil {
			t.Fatalf("parseWMIInstalledOn(%q) deveria parsear", raw)
		}
		if !ok && got != nil {
			t.Fatalf("parseWMIInstalledOn(%q) nao deveria parsear (%v)", raw, got)
		}
	}
	parsed := parseWMIInstalledOn("2026-10-16 21:09:57")
	if parsed == nil || !parsed.Equal(time.Date(2026, 10, 16, 21, 9, 57, 0, time.UTC)) {
		t.Fatalf("data incorreta: %v", parsed)
	}
}
