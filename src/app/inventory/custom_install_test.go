package inventory

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discovery/app/appstore"
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

// Pacote custom sem instalador publicado e comando que depende do placeholder:
// o erro precisa dizer a causa real, em vez do "{{installer}} não é reconhecido
// como um comando..." devolvido pelo shell.
func TestRunCustomInstallCommandSemInstaladorExplicaACausa(t *testing.T) {
	svc := &Service{}

	_, err := svc.runCustomInstallCommand(context.Background(),
		appstore.Item{InstallCommand: `{{installer}} /S`, FileURL: ""}, "sem-arquivo")
	if err == nil {
		t.Fatal("esperava erro para comando com placeholder e sem instalador publicado")
	}
	if !strings.Contains(err.Error(), "nenhum instalador foi publicado") {
		t.Fatalf("mensagem inesperada: %v", err)
	}

	// Comando sem placeholder não é bloqueado pela ausência de arquivo (ex.:
	// choco/winget): segue para a execução, que pode falhar por outro motivo
	// dependendo do SO — o que não pode acontecer é o bloqueio do placeholder.
	_, err = svc.runCustomInstallCommand(context.Background(),
		appstore.Item{InstallCommand: "echo sem-placeholder", FileURL: ""}, "sem-arquivo")
	if err != nil && strings.Contains(err.Error(), "nenhum instalador foi publicado") {
		t.Fatalf("comando sem placeholder não deveria ser bloqueado: %v", err)
	}
}

func TestCommandReferencesInstaller(t *testing.T) {
	positive := []string{
		`{{installer}} /S`,
		`msiexec /i "{{installer}}" /qn`,
		`{{installerDir}}\setup.exe`,
		`%INSTALLER% /quiet`,
		`copy "{{installerDir}}\conf" "%ProgramData%\app"`,
	}
	for _, command := range positive {
		if !commandReferencesInstaller(command) {
			t.Fatalf("%q deveria ser reconhecido como dependente do instalador", command)
		}
	}

	negative := []string{
		`choco install foxit -y`,
		`{{Installer}} /S`, // caixa diferente não é substituída pelo agent
		`echo pronto`,
	}
	for _, command := range negative {
		if commandReferencesInstaller(command) {
			t.Fatalf("%q não deveria ser reconhecido como dependente do instalador", command)
		}
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

func TestHasInstallerExtension(t *testing.T) {
	valid := []string{"setup.exe", "PACKAGE.ZIP", "app.msi", "patch.msu", "script.ps1"}
	for _, name := range valid {
		if !hasInstallerExtension(name) {
			t.Fatalf("%q deveria ser reconhecido como instalador", name)
		}
	}

	invalid := []string{"foxitedito", "installer.bin", "file", "pacote.exe.zip.bak"}
	for _, name := range invalid {
		if hasInstallerExtension(name) {
			t.Fatalf("%q não deveria ser reconhecido como instalador", name)
		}
	}
}

func TestAppendInstallerExtension(t *testing.T) {
	cases := []struct {
		name        string
		head        []byte
		contentType string
		want        string
	}{
		// Caso real do app foxitedito: PE sem extensão no Content-Disposition.
		{"foxitedito", []byte{'M', 'Z', 0x90, 0x00}, "application/octet-stream", "foxitedito.exe"},
		{"foxitedito", []byte{'M', 'Z'}, "", "foxitedito.exe"},
		{"pacote", []byte{'P', 'K', 0x03, 0x04}, "", "pacote.zip"},
		{"setup", []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, "", "setup.msi"},
		{"compactado", []byte{'7', 'z', 0xBC, 0xAF, 0x27, 0x1C}, "", "compactado.7z"},
		{"driver", []byte{'M', 'S', 'C', 'F'}, "", "driver.cab"},
		// Extensão presente mas não utilizável: é corrigida.
		{"installer.bin", []byte{'M', 'Z'}, "", "installer.exe"},
		// Sem assinatura conhecida: cai no Content-Type.
		{"arquivo", []byte{0x01, 0x02}, "application/zip", "arquivo.zip"},
		// Sem assinatura nem Content-Type útil: assume executável.
		{"arquivo", []byte{0x01, 0x02}, "text/plain", "arquivo.exe"},
		{"", []byte{'M', 'Z'}, "", "installer.exe"},
	}

	for _, tc := range cases {
		if got := appendInstallerExtension(tc.name, tc.head, tc.contentType); got != tc.want {
			t.Fatalf("appendInstallerExtension(%q, %v, %q) = %q, esperado %q",
				tc.name, tc.head, tc.contentType, got, tc.want)
		}
	}
}

func TestEnsureInstallerExtension(t *testing.T) {
	// Nome já válido: corpo passa intacto e nada é renomeado.
	original := []byte("conteudo-original")
	body, renamed := ensureInstallerExtension("setup.exe", bytes.NewReader(original), "")
	if renamed != "" {
		t.Fatalf("nome válido não deveria ser renomeado, veio %q", renamed)
	}
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ler corpo: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("corpo alterado: %q", got)
	}

	// Nome sem extensão: renomeia e o corpo precisa continuar COMPLETO — os
	// bytes usados na detecção da assinatura voltam para o começo do stream.
	pe := append([]byte{'M', 'Z', 0x90, 0x00}, []byte("resto-do-instalador")...)
	body, renamed = ensureInstallerExtension("foxitedito", bytes.NewReader(pe), "")
	if renamed != "foxitedito.exe" {
		t.Fatalf("esperava foxitedito.exe, veio %q", renamed)
	}
	got, err = io.ReadAll(body)
	if err != nil {
		t.Fatalf("ler corpo: %v", err)
	}
	if !bytes.Equal(got, pe) {
		t.Fatalf("corpo reconstruído difere do original: %q", got)
	}
}

func TestTruncateNotificationOutput(t *testing.T) {
	short := "falha curta"
	if got := truncateNotificationOutput(short); got != short {
		t.Fatalf("texto curto foi alterado: %q", got)
	}

	long := strings.Repeat("x", notificationOutputLimit+50)
	got := truncateNotificationOutput(long)
	if !strings.HasSuffix(got, "... (truncado)") {
		t.Fatalf("texto longo deveria ser truncado: %q", got[:40])
	}
	if len(got) != notificationOutputLimit+len("... (truncado)") {
		t.Fatalf("tamanho inesperado após truncar: %d", len(got))
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
