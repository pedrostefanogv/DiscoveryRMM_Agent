package updates

import (
	"strings"
	"testing"
)

// TestParseInstalledListOutputRealHostChromeId prova que o parser de produção do
// "winget list" extrai, para o Chrome instalado via ARP, o Id de ARP
// ("ARP\Machine\X86\Google Chrome") — e NÃO o Id de catálogo
// ("Google.Chrome.EXE"). É a razão pela qual a Loja não pode casar apenas por
// PackageID.
//
// As linhas abaixo são VERBATIM deste host (mesmas larguras de coluna),
// extraídas do log do agente.
func TestParseInstalledListOutputRealHostChromeId(t *testing.T) {
	header := "Nome                                                           ID                                                                                        Versão                        Disponível    Origem"
	dashes := "-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------"
	sevenZip := "7-Zip 26.05 (x64 edition)                                      7zip.7zip                                                                                 26.05.00.0                                  winget"
	chrome := "Google Chrome                                                  ARP\\Machine\\X86\\Google Chrome                                                     157.0.8103.39"
	beta := "Google Chrome Beta                                             Google.Chrome.Beta.EXE                                                                   157.0.8103.39"

	raw := header + "\r\n" + dashes + "\r\n" + sevenZip + "\r\n" + chrome + "\r\n" + beta

	items := parseInstalledListOutput(raw)
	if len(items) == 0 {
		t.Fatalf("parser nao extraiu nenhum pacote da saida real")
	}

	byName := map[string]string{}
	for _, it := range items {
		byName[it.Name] = it.ID
		t.Logf("%q -> ID=%q Version=%q", it.Name, it.ID, it.Version)
	}

	got, ok := byName["Google Chrome"]
	if !ok {
		t.Fatalf("parser nao encontrou 'Google Chrome'; itens=%v", byName)
	}
	if !strings.Contains(got, "ARP") {
		t.Fatalf("esperado Id de ARP para o Chrome instalado; veio %q", got)
	}
	if got == "Google.Chrome.EXE" {
		t.Fatalf("o Chrome estavel nao deveria ser resolvido para o Id de catalogo pelo winget list")
	}
	if _, ok := byName["Google Chrome Beta"]; !ok {
		t.Fatalf("o Chrome Beta instalado tambem deveria aparecer")
	}
}
