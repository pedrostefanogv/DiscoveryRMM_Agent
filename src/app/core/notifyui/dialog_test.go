package notifyui

import "testing"

// NormalizeDialogButtons preserva os RÓTULOS do MessageBoxButtonText (contrato
// do resultado do DialogBox) e coloca a ação principal por último.
func TestNormalizeDialogButtons(t *testing.T) {
	cases := map[string][]string{
		"":                {"OK"},
		"ok":              {"OK"},
		"okcancel":        {"Cancel", "OK"},
		"yesno":           {"No", "Yes"},
		"yesnocancel":     {"Cancel", "No", "Yes"},
		"abortretryignore": {"Ignore", "Retry", "Abort"},
		"retrycancel":     {"Cancel", "Retry"},
		"canceltrycontinue": {"Cancel", "Try Again", "Continue"},
		"desconhecido":    {"OK"},
	}
	for kind, want := range cases {
		got := NormalizeDialogButtons(kind)
		if len(got) != len(want) {
			t.Fatalf("%q: botões = %v, want %v", kind, got, want)
		}
		for i, b := range got {
			if b.Label != want[i] {
				t.Fatalf("%q: botão %d = %q, want %q", kind, i, b.Label, want[i])
			}
		}
		// Último botão é a ação principal (mesma regra do cartão).
		last := got[len(got)-1]
		if !last.IsPrimary() {
			t.Fatalf("%q: último botão %q deveria ser primary", kind, last.Label)
		}
	}
}

// DialogCloseLabel devolve "Cancel" quando existe; senão o rótulo padrão.
func TestDialogCloseLabel(t *testing.T) {
	if got := DialogCloseLabel(NormalizeDialogButtons("okcancel")); got != "Cancel" {
		t.Fatalf("okcancel close = %q, want Cancel", got)
	}
	if got := DialogCloseLabel(NormalizeDialogButtons("yesno")); got != "Yes" {
		t.Fatalf("yesno close = %q, want Yes (sem cancelar, fecha = ação padrão)", got)
	}
	if got := DialogCloseLabel(NormalizeDialogButtons("")); got != "OK" {
		t.Fatalf("ok close = %q, want OK", got)
	}
}
