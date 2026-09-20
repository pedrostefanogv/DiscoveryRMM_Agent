package buildinfo

import "testing"

// Builds oficiais injetam Commit via ldflags — CommitForReport deve devolvê-lo
// sem alteração.
func TestCommitForReport_UsesInjectedCommit(t *testing.T) {
	orig := Commit
	defer func() { Commit = orig }()

	Commit = "2d28aa0c"
	if got := CommitForReport(); got != "2d28aa0c" {
		t.Fatalf("CommitForReport() = %q, want %q", got, "2d28aa0c")
	}
}

// CommitForReport nunca deve devolver placeholders — com Commit sem injeção,
// cai para o VCS stamping (Revision) ou devolve "" para o servidor preservar
// o último valor conhecido.
func TestCommitForReport_NeverReturnsPlaceholder(t *testing.T) {
	orig := Commit
	defer func() { Commit = orig }()

	for _, c := range []string{"", "unknown"} {
		Commit = c
		if got := CommitForReport(); got == "unknown" || got == "dev" || got == "0.0.0" {
			t.Fatalf("CommitForReport() = %q com Commit=%q — placeholder não deve ser reportado", got, c)
		}
	}
}
