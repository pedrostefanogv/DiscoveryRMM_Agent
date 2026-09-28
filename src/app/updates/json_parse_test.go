package updates

import "testing"

const installedJSON = `[
  {"Name":"Google Chrome","Id":"Google.Chrome.EXE","Version":"153.0","AvailableVersion":"154.0","Source":"winget"},
  {"Name":"7-Zip","Id":"7zip.7zip","Version":"26.03","Source":"winget"}
]`

func TestParseInstalledListOutput_JSON(t *testing.T) {
	items := parseInstalledListOutput(installedJSON)
	if len(items) != 2 {
		t.Fatalf("esperava 2 itens, got %d (%+v)", len(items), items)
	}
	if items[0].ID != "Google.Chrome.EXE" || items[0].Version != "153.0" || items[0].Source != "winget" {
		t.Fatalf("item inesperado: %+v", items[0])
	}
}

func TestParseInstalledOutput_JSON(t *testing.T) {
	ids := parseInstalledOutput(installedJSON)
	if len(ids) != 2 || ids[0] != "Google.Chrome.EXE" || ids[1] != "7zip.7zip" {
		t.Fatalf("ids inesperados: %+v", ids)
	}
}

func TestParseUpgradeOutput_JSON(t *testing.T) {
	raw := `[{"Name":"Google Chrome","Id":"Google.Chrome.EXE","Version":"153.0","AvailableVersion":"154.0","Source":"winget"}]`
	items := parseUpgradeOutput(raw)
	if len(items) != 1 {
		t.Fatalf("esperava 1 item, got %d", len(items))
	}
	got := items[0]
	if got.ID != "Google.Chrome.EXE" || got.CurrentVersion != "153.0" || got.AvailableVersion != "154.0" {
		t.Fatalf("item inesperado: %+v", got)
	}
}

func TestParseInstalledListOutput_TableStillWorks(t *testing.T) {
	table := "Name          Id                  Version\n" +
		"----------------------------------------------\n" +
		"Google Chrome Google.Chrome.EXE    153.0\n"
	items := parseInstalledListOutput(table)
	if len(items) != 1 || items[0].ID != "Google.Chrome.EXE" {
		t.Fatalf("fallback tabela quebrou: %+v", items)
	}
}
