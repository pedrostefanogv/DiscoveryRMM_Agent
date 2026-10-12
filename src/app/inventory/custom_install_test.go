package inventory

import (
	"archive/zip"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyCustomCommandPlaceholders(t *testing.T) {
	command := `{{installerDir}}setup.exe /S /f "{{installer}}"`
	got := applyCustomCommandPlaceholders(command, `C:Tempasetup.exe`, `C:Tempa`)

	if strings.Contains(got, "{{installer}}") || strings.Contains(got, "{{installerDir}}") {
		t.Fatalf("placeholders nao resolvidos: %s", got)
	}
	if !strings.Contains(got, `C:Tempasetup.exe`) {
		t.Fatalf("caminho do instalador ausente: %s", got)
	}

	if same := applyCustomCommandPlaceholders(command, "", ""); same != command {
		t.Fatalf("sem arquivo baixado o comando deveria ficar intacto, veio %q", same)
	}
}

func TestExtractZipArchiveAndResolveRoot(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "pacote.zip")
	createTestZip(t, zipPath, map[string]string{
		"app/setup.exe":       "binario",
		"app/conf/config.ini": "cfg",
	})

	dest := filepath.Join(t.TempDir(), "extracted")
	if err := extractZipArchive(zipPath, dest); err != nil {
		t.Fatalf("extractZipArchive falhou: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "app", "setup.exe")); err != nil {
		t.Fatalf("setup.exe nao extraido: %v", err)
	}

	root := resolveExtractionRoot(dest)
	if root != filepath.Join(dest, "app") {
		t.Fatalf("raiz esperada %q, veio %q", filepath.Join(dest, "app"), root)
	}
}

func TestExtractZipArchiveRejectsZipSlip(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "malicioso.zip")
	createTestZip(t, zipPath, map[string]string{"../escaped.txt": "x"})

	err := extractZipArchive(zipPath, filepath.Join(t.TempDir(), "out"))
	if err == nil {
		t.Fatal("esperava erro para entrada com path traversal")
	}
	if !strings.Contains(err.Error(), "fora do diretório") {
		t.Fatalf("erro inesperado: %v", err)
	}
}

func TestCustomAssetFileName(t *testing.T) {
	raw := "https://tngplacas.com.br/api/v1/agent-auth/me/app-store/custom/x/file"

	withDisposition := &http.Response{Header: http.Header{}}
	withDisposition.Header.Set("Content-Disposition", "attachment; filename=\"setup.exe\"")
	if name := customAssetFileName(withDisposition, raw); name != "setup.exe" {
		t.Fatalf("esperava setup.exe, veio %q", name)
	}

	withoutHeader := &http.Response{Header: http.Header{}}
	if name := customAssetFileName(withoutHeader, raw); name != "file" {
		t.Fatalf("esperava fallback 'file', veio %q", name)
	}
}

func createTestZip(t *testing.T, zipPath string, entries map[string]string) {
	t.Helper()

	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("criar zip: %v", err)
	}
	defer file.Close()

	writer := zip.NewWriter(file)
	for name, body := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("criar entrada %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatalf("escrever entrada %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("fechar zip: %v", err)
	}
}
