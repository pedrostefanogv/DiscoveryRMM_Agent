package appstore

import "testing"

func TestSortEffectiveItemsWingetFirst(t *testing.T) {
	items := []Item{
		{InstallationType: "Chocolatey", PackageID: "7zip.7zip"},
		{InstallationType: "Custom", PackageID: "erp-interno"},
		{InstallationType: "Winget", PackageID: "ZenBrowser.Zen"},
		{InstallationType: "Winget", PackageID: "Git.Git"},
		{InstallationType: "TipoDesconhecido", PackageID: "x.y"},
		{InstallationType: "winget", PackageID: "aBc.D"},
	}

	sortEffectiveItems(items)

	want := []struct {
		installationType string
		packageID        string
	}{
		// sortEffectiveItems não normaliza o valor armazenado (a normalização
		// "winget"→"Winget" acontece no FetchPage); mesmo rank, ordem por ID.
		{"winget", "aBc.D"},
		{"Winget", "Git.Git"},
		{"Winget", "ZenBrowser.Zen"},
		{"Chocolatey", "7zip.7zip"},
		{"Custom", "erp-interno"},
		{"TipoDesconhecido", "x.y"},
	}

	for i, w := range want {
		if items[i].InstallationType != w.installationType || items[i].PackageID != w.packageID {
			t.Fatalf("posicao %d: esperado %s/%s, obtido %s/%s",
				i, w.installationType, w.packageID, items[i].InstallationType, items[i].PackageID)
		}
	}
}

func TestInstallationTypeSortRank(t *testing.T) {
	cases := []struct {
		value string
		want  int
	}{
		{"Winget", 0},
		{"winget", 0},
		{"Chocolatey", 1},
		{"choco", 1},
		{"Custom", 2},
		{"custom", 2},
		{"", 3},
		{"QualquerCoisa", 3},
	}
	for _, c := range cases {
		if got := installationTypeSortRank(c.value); got != c.want {
			t.Errorf("installationTypeSortRank(%q) = %d, esperado %d", c.value, got, c.want)
		}
	}
}
