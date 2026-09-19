package updates

import (
	"strings"
	"testing"
)

// padTo alinha uma célula à esquerda dentro da largura da coluna (em runas),
// simulando o layout monoespaçado das tabelas do winget.
func padTo(s string, width int) string {
	if n := width - len([]rune(s)); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// TestParseUpgradeOutput_PortugueseHeaderKeepsVersionsIntact é a regressão do
// bug da tela de updates em Windows pt-BR: o cabeçalho do `winget upgrade`
// contém caracteres multi-byte ("Versão", "Disponível") e a coluna de origem
// chama "Origem". O parser antigo misturava offsets em bytes com fatiamento em
// runes — o primeiro caractere da versão disponível vazava para a Versão Atual
// ("26.02.00.0 2" + "6.03 winget") e a origem vazava para a Versão Disponível.
func TestParseUpgradeOutput_PortugueseHeaderKeepsVersionsIntact(t *testing.T) {
	header := padTo("Nome", 36) + padTo("ID", 30) + padTo("Versão", 16) + padTo("Disponível", 16) + "Origem"
	divider := strings.Repeat("-", 100)
	rows := []struct{ name, id, current, avail, source string }{
		{"7-Zip 26.02 (x64 edition)", "7zip.7zip", "26.02.00.0", "26.03", "winget"},
		{"Adobe Acrobat (64-bit)", "Adobe.Acrobat.Reader.64-bit", "26.002.21869", "26.002.21931", "winget"},
		{"Google Chrome", "Google.Chrome", "151.0.7922.170", "153.0.8010.53", "winget"},
		{"Mozilla Thunderbird (x64 en-US)", "Mozilla.Thunderbird", "155.0", "156.0", "winget"},
	}

	var sb strings.Builder
	sb.WriteString(header + "\r\n" + divider + "\r\n")
	for _, r := range rows {
		sb.WriteString(padTo(r.name, 36) + padTo(r.id, 30) + padTo(r.current, 16) + padTo(r.avail, 16) + r.source + "\r\n")
	}
	sb.WriteString("5 atualização(ões) disponível(is).\r\n")

	items := parseUpgradeOutput(sb.String())
	if len(items) != len(rows) {
		t.Fatalf("esperava %d itens, veio %d: %+v", len(rows), len(items), items)
	}
	for i, item := range items {
		want := rows[i]
		if item.Name != want.name {
			t.Errorf("linha %d: Name = %q, queria %q", i, item.Name, want.name)
		}
		if item.ID != want.id {
			t.Errorf("linha %d: ID = %q, queria %q", i, item.ID, want.id)
		}
		if item.CurrentVersion != want.current {
			t.Errorf("linha %d: CurrentVersion = %q, queria %q", i, item.CurrentVersion, want.current)
		}
		if item.AvailableVersion != want.avail {
			t.Errorf("linha %d: AvailableVersion = %q, queria %q", i, item.AvailableVersion, want.avail)
		}
		if item.Source != want.source {
			t.Errorf("linha %d: Source = %q, queria %q", i, item.Source, want.source)
		}
	}
}

// TestParseUpgradeOutput_MissingSourceColumnExtractsTrailingSource cobre a
// variante sem coluna de origem reconhecida no cabeçalho: a origem impressa no
// fim da linha ("26.03   winget") não pode ficar grudada na versão disponível.
func TestParseUpgradeOutput_MissingSourceColumnExtractsTrailingSource(t *testing.T) {
	header := padTo("Nome", 30) + padTo("ID", 60) + padTo("Versão", 74) + "Disponível"
	divider := strings.Repeat("-", 95)
	row := padTo("7-Zip 26.02 (x64 edition)", 30) + padTo("7zip.7zip", 60) + padTo("26.02.00.0", 74) + "26.03      winget"
	raw := header + "\r\n" + divider + "\r\n" + row + "\r\n"

	items := parseUpgradeOutput(raw)
	if len(items) != 1 {
		t.Fatalf("esperava 1 item, veio %d: %+v", len(items), items)
	}
	item := items[0]
	if item.AvailableVersion != "26.03" {
		t.Errorf("AvailableVersion = %q, queria %q", item.AvailableVersion, "26.03")
	}
	if item.Source != "winget" {
		t.Errorf("Source = %q, queria %q", item.Source, "winget")
	}
	if item.CurrentVersion != "26.02.00.0" {
		t.Errorf("CurrentVersion = %q, queria %q", item.CurrentVersion, "26.02.00.0")
	}
}

// TestParseUpgradeOutput_PortugueseHeaderBlankSource garante que linha sem
// origem preenchida (winget omite a Origem em alguns pacotes) não perde dados.
func TestParseUpgradeOutput_PortugueseHeaderBlankSource(t *testing.T) {
	header := padTo("Nome", 36) + padTo("ID", 30) + padTo("Versão", 16) + padTo("Disponível", 16) + "Origem"
	divider := strings.Repeat("-", 100)
	raw := header + "\r\n" + divider + "\r\n" +
		padTo("App Estranho", 36) + padTo("Vendor.App", 30) + padTo("1.2.3", 16) + "4.5.6" + "\r\n"

	items := parseUpgradeOutput(raw)
	if len(items) != 1 {
		t.Fatalf("esperava 1 item, veio %d: %+v", len(items), items)
	}
	if items[0].AvailableVersion != "4.5.6" {
		t.Errorf("AvailableVersion = %q, queria %q", items[0].AvailableVersion, "4.5.6")
	}
	if items[0].Source != "" {
		t.Errorf("Source = %q, queria vazio (dedupe assume winget depois)", items[0].Source)
	}
}
