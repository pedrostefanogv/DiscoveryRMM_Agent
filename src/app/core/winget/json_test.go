package winget

import (
	"errors"
	"testing"
)

func TestParseListJSON_Array(t *testing.T) {
	raw := `[{"Name":"Google Chrome","Id":"Google.Chrome.EXE","Version":"153.0","AvailableVersion":"154.0","Source":"winget"}]`
	entries, ok := ParseListJSON(raw)
	if !ok || len(entries) != 1 {
		t.Fatalf("esperava 1 entrada JSON, ok=%v len=%d", ok, len(entries))
	}
	if entries[0].ID != "Google.Chrome.EXE" || entries[0].AvailableVersionValue() != "154.0" {
		t.Fatalf("entrada inesperada: %+v", entries[0])
	}
}

func TestParseListJSON_AvailableFallback(t *testing.T) {
	raw := `[{"Id":"X","Available":"2.0"}]`
	entries, ok := ParseListJSON(raw)
	if !ok || len(entries) != 1 || entries[0].AvailableVersionValue() != "2.0" {
		t.Fatalf("fallback Available falhou: ok=%v entries=%+v", ok, entries)
	}
}

func TestParseListJSON_EmptyArrayIsJSON(t *testing.T) {
	if _, ok := ParseListJSON("[]"); !ok {
		t.Fatalf("array vazio é JSON válido")
	}
}

func TestParseListJSON_TableIsNotJSON(t *testing.T) {
	table := "Name  Id  Version\n----\nFoo  Foo.Bar  1.0\n"
	if _, ok := ParseListJSON(table); ok {
		t.Fatalf("tabela não pode ser tratada como JSON")
	}
}

func TestParseListJSON_WrappedObject(t *testing.T) {
	raw := `{"Packages":[{"Id":"Brave.Brave","Version":"1.0"}]}`
	entries, ok := ParseListJSON(raw)
	if !ok || len(entries) != 1 || entries[0].ID != "Brave.Brave" {
		t.Fatalf("wrapper Packages nao parseado: ok=%v entries=%+v", ok, entries)
	}
}

func TestParseListJSON_UnknownArgumentOutputIsNotJSON(t *testing.T) {
	raw := "Argument name was not recognized for the current command: '--output'\nusage: winget list ..."
	if _, ok := ParseListJSON(raw); ok {
		t.Fatalf("texto de erro nao pode ser JSON")
	}
	if !isUnknownJSONArgumentError(errors.New("exit status"), raw) {
		t.Fatalf("deveria detectar argumento JSON nao suportado")
	}
	if isUnknownJSONArgumentError(nil, raw) {
		t.Fatalf("sem erro nao ha deteccao")
	}
}
