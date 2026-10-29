package thirdparty

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Fixture REAL capturada em 2026-10-29 (winget 1.30.140-preview, pt-BR).
func loadTestdata(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return strings.TrimPrefix(string(raw), "\ufeff")
}

func TestParseWingetUpgradeTableRealFixture(t *testing.T) {
	updates := ParseWingetUpgradeTable(loadTestdata(t, "winget_upgrade_ptbr.txt"))

	if len(updates) != 3 {
		t.Fatalf("esperava 3 updates reais; got %d: %+v", len(updates), updates)
	}
	byID := map[string]AppUpdate{}
	for _, update := range updates {
		byID[update.ID] = update
	}
	node, ok := byID["OpenJS.NodeJS.LTS"]
	if !ok {
		t.Fatalf("Node.js nao encontrado: %+v", updates)
	}
	if node.Name != "Node.js" || node.InstalledVersion != "24.22.1" || node.AvailableVersion != "26.13.0" {
		t.Fatalf("campos inesperados: %+v", node)
	}
	if wsl, ok := byID["Microsoft.WSL"]; !ok || wsl.AvailableVersion != "2.7.14" {
		t.Fatalf("WSL inesperado: %+v", wsl)
	}
	if rgb, ok := byID["GIGABYTE.RGBFusion"]; !ok || rgb.InstalledVersion != "3.24.1202.1" || rgb.AvailableVersion != "3.26.0923.1" {
		t.Fatalf("RGB Fusion inesperado: %+v", rgb)
	}
}

func TestParseWingetUpgradeTableEnglishAndTruncated(t *testing.T) {
	header := fmt.Sprintf("%-32s%-22s%-12s%-12s%s", "Name", "Id", "Version", "Available", "Source")
	separator := strings.Repeat("-", len(header))
	rows := []string{
		fmt.Sprintf("%-32s%-22s%-12s%-12s%s", "7-Zip", "7zip.7zip", "23.01", "24.09", "winget"),
		fmt.Sprintf("%-32s%-22s%-12s%-12s%s", "Truncated", "Vendor.Truncated…", "1.0", "2.0", "winget"),
		fmt.Sprintf("%-32s%-22s%-12s%-12s%s", "Same", "Vendor.Same", "1.0", "1.0", "winget"),
	}
	raw := header + "\n" + separator + "\n" + strings.Join(rows, "\n") + "\n3 upgrades available.\n"

	updates := ParseWingetUpgradeTable(raw)

	if len(updates) != 1 {
		t.Fatalf("esperava so o 7-Zip (truncado e sem update ficam fora); got %+v", updates)
	}
	if updates[0].ID != "7zip.7zip" || updates[0].AvailableVersion != "24.09" {
		t.Fatalf("7-Zip inesperado: %+v", updates[0])
	}
}

func TestParseWingetUpgradeTableRejectsGarbage(t *testing.T) {
	if updates := ParseWingetUpgradeTable("O nome do argumento nao foi reconhecido"); len(updates) != 0 {
		t.Fatalf("texto de ajuda nao e tabela: %+v", updates)
	}
	if updates := ParseWingetUpgradeTable(""); len(updates) != 0 {
		t.Fatalf("vazio nao e tabela: %+v", updates)
	}
}
