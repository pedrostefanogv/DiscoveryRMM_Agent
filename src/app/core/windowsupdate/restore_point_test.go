package windowsupdate

import "testing"

func TestNeedsRestorePoint(t *testing.T) {
	if NeedsRestorePoint(nil) {
		t.Fatal("lote vazio nao precisa de checkpoint")
	}
	if NeedsRestorePoint([][]string{{"Definition Updates"}, {"Definitions"}}) {
		t.Fatal("apenas definitions nao precisam de checkpoint")
	}
	if !NeedsRestorePoint([][]string{{"Definition Updates"}, {"Security Updates"}}) {
		t.Fatal("um update de seguranca exige checkpoint")
	}
	if !NeedsRestorePoint([][]string{nil}) {
		t.Fatal("update sem categoria conta como arriscado (fail-safe)")
	}
}
