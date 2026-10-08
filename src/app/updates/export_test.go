package updates

import (
	"os"
	"path/filepath"
	"testing"
)

// Regressão 1: a pasta do executável (Program Files) vinha primeiro e exigia
// elevação — sem admin, a exportação falhava antes de chegar às pastas do
// usuário.
// Regressão 2 (2026-10-07): o destino era LocalAppData\Discovery\Exports, que o
// usuário não enxerga no dia a dia. A ordem agora é Desktop → Documentos, com
// LocalAppData apenas como fallback.
func TestExportDirCandidatesPrefersDesktopThenDocuments(t *testing.T) {
	home := t.TempDir()
	desktop := filepath.Join(home, "Desktop")
	documents := filepath.Join(home, "Documents")
	for _, dir := range []string{desktop, documents} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("preparar %s: %v", dir, err)
		}
	}
	// os.UserHomeDir lê USERPROFILE (Windows) / HOME (demais sistemas).
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	// OneDrive vazio: o teste valida a ordem padrão do perfil local.
	t.Setenv("OneDrive", "")
	localAppData := filepath.Join(home, "AppData", "Local")
	t.Setenv("LOCALAPPDATA", localAppData)

	dirs := exportDirCandidates()
	if len(dirs) == 0 {
		t.Fatal("nenhuma pasta candidata")
	}
	wantDesktop := filepath.Join(desktop, "DiscoveryExports")
	if dirs[0] != wantDesktop {
		t.Fatalf("primeiro candidato = %q, want %q", dirs[0], wantDesktop)
	}
	wantDocuments := filepath.Join(documents, "DiscoveryExports")
	if len(dirs) < 2 || dirs[1] != wantDocuments {
		t.Fatalf("segundo candidato = %q, want %q", dirs[1], wantDocuments)
	}

	legacy := filepath.Join(localAppData, "Discovery", "Exports")
	for i, dir := range dirs {
		if dir == legacy {
			if i < 2 {
				t.Fatalf("LocalAppData não pode vir antes das pastas pessoais (índice %d)", i)
			}
			break
		}
	}

	if exe, err := os.Executable(); err == nil && exe != "" {
		exeDir := filepath.Join(filepath.Dir(exe), "DiscoveryExports")
		if dirs[0] == exeDir {
			t.Fatal("pasta do executável não pode ser a primeira opção")
		}
	}
}

// Execução como SYSTEM (serviço): o perfil do sistema não tem Desktop/Documentos
// do usuário — o relatório deve ir para Documentos Públicos, não para
// C:\Windows\system32\config\systemprofile (invisível para o usuário).
func TestExportDirCandidates_SystemProfileUsesPublicDocuments(t *testing.T) {
	home := filepath.Join(t.TempDir(), "Windows", "system32", "config", "systemprofile")
	public := filepath.Join(t.TempDir(), "Public")
	for _, dir := range []string{home, filepath.Join(public, "Documents")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("preparar %s: %v", dir, err)
		}
	}
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("OneDrive", "")
	t.Setenv("PUBLIC", public)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))

	dirs := exportDirCandidates()
	if len(dirs) == 0 {
		t.Fatal("nenhuma pasta candidata")
	}
	want := filepath.Join(public, "Documents", "DiscoveryExports")
	if dirs[0] != want {
		t.Fatalf("primeiro candidato = %q, want %q", dirs[0], want)
	}
}

// Pastas pessoais inexistentes (ex.: Desktop antigo após o Known Folder Move do
// OneDrive) não podem ser preferidas às que existem de fato.
func TestExportDirCandidatesPrefersExistingPersonalDirs(t *testing.T) {
	home := t.TempDir()
	oneDrive := filepath.Join(t.TempDir(), "OneDrive")
	oneDriveDesktop := filepath.Join(oneDrive, "Desktop")
	if err := os.MkdirAll(oneDriveDesktop, 0o755); err != nil {
		t.Fatalf("preparar %s: %v", oneDriveDesktop, err)
	}
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("OneDrive", oneDrive)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))

	dirs := exportDirCandidates()
	if len(dirs) == 0 {
		t.Fatal("nenhuma pasta candidata")
	}
	want := filepath.Join(oneDriveDesktop, "DiscoveryExports")
	if dirs[0] != want {
		t.Fatalf("primeiro candidato = %q, want %q (Desktop do OneDrive é o que existe)", dirs[0], want)
	}
}
