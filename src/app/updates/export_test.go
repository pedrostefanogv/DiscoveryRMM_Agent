package updates

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Regressão: a pasta do executável (Program Files) vinha primeiro e exigia
// elevação — sem admin, a exportação falhava antes de chegar às pastas do
// usuário. A primeira escolha agora é sempre uma pasta gravável pelo usuário.
func TestExportDirCandidatesPrefersUserWritableDir(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("ordem definida para Windows")
	}
	localAppData := filepath.Join(t.TempDir(), "AppData", "Local")
	t.Setenv("LOCALAPPDATA", localAppData)

	dirs := exportDirCandidates()
	if len(dirs) == 0 {
		t.Fatal("nenhuma pasta candidata")
	}
	want := filepath.Join(localAppData, "Discovery", "Exports")
	if dirs[0] != want {
		t.Fatalf("primeiro candidato = %q, want %q", dirs[0], want)
	}

	if exe, err := os.Executable(); err == nil && exe != "" {
		exeDir := filepath.Join(filepath.Dir(exe), "DiscoveryExports")
		if dirs[0] == exeDir {
			t.Fatal("pasta do executável não pode ser a primeira opção")
		}
	}
}
