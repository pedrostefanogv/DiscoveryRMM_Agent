package inventory

import "testing"

func TestEnrichPathWith_AppendsDedupesAndTrims(t *testing.T) {
	current := `C:\Windows;C:\ProgramData\chocolatey\bin;C:\Windows\`
	extras := []string{`C:\tools\dart-sdk\bin`, `  `, `C:\ProgramData\chocolatey\bin`, `C:\tools\dart-sdk\bin\`}

	got := enrichPathWith(current, extras)
	want := `C:\Windows;C:\ProgramData\chocolatey\bin;C:\tools\dart-sdk\bin`

	if got != want {
		t.Fatalf("enrichPathWith = %q, want %q", got, want)
	}
}

func TestEnrichPathWith_EmptyCurrent(t *testing.T) {
	got := enrichPathWith("", []string{`C:\tools\dart-sdk\bin`})
	want := `C:\tools\dart-sdk\bin`
	if got != want {
		t.Fatalf("enrichPathWith = %q, want %q", got, want)
	}
}

// TestPackageManagerEnv_KeepsPath garante que o ambiente do gerenciador mantém
// a variável PATH (mesmo quando não há nenhuma ferramenta em C:	ools, como em
// CI/Linux, onde o glob não casa).
func TestPackageManagerEnv_KeepsPath(t *testing.T) {
	env := packageManagerEnv()
	for _, entry := range env {
		if len(entry) >= 5 && (entry[:5] == "PATH=" || entry[:5] == "Path=") {
			return
		}
	}
	t.Fatalf("PATH ausente no ambiente do gerenciador: %v", env)
}
