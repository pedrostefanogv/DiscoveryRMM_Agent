package thirdparty

import (
	"context"
	"errors"
	"testing"
)

type fakeProvider struct {
	source  string
	updates []AppUpdate
	err     error
	calls   int
}

func (f *fakeProvider) Source() string { return f.source }
func (f *fakeProvider) Inventory(context.Context) ([]AppUpdate, error) {
	f.calls++
	return f.updates, f.err
}
func (f *fakeProvider) Upgrade(context.Context, string) (string, error) { return "", nil }

func TestParseChocoOutdated(t *testing.T) {
	raw := "Chocolatey v2.2.2\n" +
		"googlechrome|120.0|121.0|false\n" +
		"7zip|23.1|24.0|true\n" +
		"firefox|121.0|122.0\n" +
		"invalido id|1|2|false\n" +
		"quebrado|1\n" +
		"\n"

	updates := ParseChocoOutdated(raw)

	if len(updates) != 2 {
		t.Fatalf("esperava 2 updates (pinned e invalido fora); got %+v", updates)
	}
	if updates[0].UpdateKey() != "choco:googlechrome" {
		t.Fatalf("chave inesperada: %q", updates[0].UpdateKey())
	}
	if updates[0].InstalledVersion != "120.0" || updates[0].AvailableVersion != "121.0" {
		t.Fatalf("versoes inesperadas: %+v", updates[0])
	}
	if updates[1].ID != "firefox" {
		t.Fatalf("segundo update inesperado: %+v", updates[1])
	}
}

func TestParseWingetUpgradable(t *testing.T) {
	raw := `[
	  {"Name":"Google Chrome","Id":"Google.Chrome","Version":"120.0","AvailableVersion":"121.0"},
	  {"Name":"Igual","Id":"Vendor.Same","Version":"1.0","Available":"1.0"},
	  {"Name":"Sem id valido","Id":"Vendor App!","Version":"1.0","AvailableVersion":"2.0"},
	  {"Name":"Sem versao disponivel","Id":"Vendor.SemVersao","Version":"1.0"}
	]`

	updates := ParseWingetUpgradable(raw)

	if len(updates) != 1 {
		t.Fatalf("esperava 1 update valido; got %+v", updates)
	}
	if updates[0].UpdateKey() != "winget:Google.Chrome" {
		t.Fatalf("chave inesperada: %q", updates[0].UpdateKey())
	}
	if updates[0].Name != "Google Chrome" {
		t.Fatalf("nome inesperado: %+v", updates[0])
	}
}

func TestParseWingetUpgradableRejectsTable(t *testing.T) {
	table := "Name  Id  Version  Available\nChrome  Google.Chrome  120  121\n"
	if updates := ParseWingetUpgradable(table); len(updates) != 0 {
		t.Fatalf("tabela nao e contrato confiavel; got %+v", updates)
	}
}

func TestCollectAggregatesAndSkipsFailures(t *testing.T) {
	ok := &fakeProvider{source: SourceChoco, updates: []AppUpdate{{Source: SourceChoco, ID: "a"}}}
	bad := &fakeProvider{source: SourceWinget, err: errors.New("winget quebrado")}

	updates, failures := Collect(context.Background(), []Provider{nil, ok, bad})

	if len(updates) != 1 {
		t.Fatalf("esperava 1 update do provider que funcionou; got %+v", updates)
	}
	if len(failures) != 1 || failures[SourceWinget] == nil {
		t.Fatalf("falha do winget deveria ser registrada; got %+v", failures)
	}
}

func TestFindProviderAndSourceClassification(t *testing.T) {
	choco := &fakeProvider{source: SourceChoco}
	if FindProvider([]Provider{choco}, "CHOCO") == nil {
		t.Fatal("FindProvider deveria achar por case-insensitive")
	}
	if FindProvider([]Provider{choco}, SourceWinget) != nil {
		t.Fatal("FindProvider nao deveria inventar provider")
	}
	if !IsThirdPartySource("choco") || !IsThirdPartySource("WINGET") {
		t.Fatal("choco/winget sao origens de terceiros")
	}
	if IsThirdPartySource("wua") || IsThirdPartySource("") {
		t.Fatal("wua/vazio nao sao terceiros")
	}
}

func TestValidAppID(t *testing.T) {
	if !ValidAppID("googlechrome") || !ValidAppID("Microsoft.Edge") {
		t.Fatal("IDs legitimos deveriam ser aceitos")
	}
	if ValidAppID("") || ValidAppID("com espaco") || ValidAppID("a;rm -rf") {
		t.Fatal("IDs invalidos deveriam ser recusados")
	}
}
