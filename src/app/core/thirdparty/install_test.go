package thirdparty

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256Of(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type runnerCall struct {
	name string
	args []string
}

func TestInstallerDownloadsVerifiesAndRunsMsi(t *testing.T) {
	payload := []byte("conteudo-do-msi")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	var calls []runnerCall
	installer := NewInstaller()
	installer.Client = server.Client()
	installer.Runner = func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, runnerCall{name: name, args: args})
		return "ok", nil
	}

	output, err := installer.Install(context.Background(), InstallSpec{
		Source:        SourceWinget,
		ID:            "Vendor.App",
		InstallerURL:  server.URL + "/app.msi",
		SHA256:        sha256Of(payload),
		InstallerType: "msi",
		SilentArgs:    "/l*v log.txt",
	})
	if err != nil {
		t.Fatalf("instalacao curada falhou: %v", err)
	}
	if output != "ok" {
		t.Fatalf("output inesperado: %q", output)
	}
	if len(calls) != 1 || calls[0].name != "msiexec" {
		t.Fatalf("esperava msiexec; got %+v", calls)
	}
	joined := strings.Join(calls[0].args, " ")
	for _, expected := range []string{"/i", "/qn", "/norestart", "/l*v log.txt"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("argumento %q ausente em %q", expected, joined)
		}
	}
}

func TestInstallerRejectsHashMismatchBeforeRunning(t *testing.T) {
	payload := []byte("binario-adulterado")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	ran := false
	installer := NewInstaller()
	installer.Client = server.Client()
	installer.Runner = func(context.Context, string, ...string) (string, error) {
		ran = true
		return "", nil
	}

	_, err := installer.Install(context.Background(), InstallSpec{
		Source:        SourceWinget,
		ID:            "Vendor.App",
		InstallerURL:  server.URL + "/app.exe",
		SHA256:        sha256Of([]byte("esperado-diferente")),
		InstallerType: "exe",
	})
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("esperava ErrHashMismatch; got %v", err)
	}
	if ran {
		t.Fatal("instalador NAO pode ser executado com hash divergente")
	}
}

func TestInstallerRequiresHttpsAndIntegrity(t *testing.T) {
	installer := NewInstaller()
	installer.Runner = func(context.Context, string, ...string) (string, error) { return "", nil }

	if _, err := installer.Install(context.Background(), InstallSpec{
		Source: SourceWinget, ID: "Vendor.App",
	}); err == nil {
		t.Fatal("sem URL/hash deve falhar")
	}

	if _, err := installer.Install(context.Background(), InstallSpec{
		Source:       SourceWinget,
		ID:           "Vendor.App",
		InstallerURL: "http://inseguro.example/app.exe",
		SHA256:       sha256Of([]byte("x")),
	}); err == nil {
		t.Fatal("URL http deve ser recusada")
	}
}

func TestInstallerRunsExeWithSilentArgs(t *testing.T) {
	payload := []byte("exe")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	var call runnerCall
	installer := NewInstaller()
	installer.Client = server.Client()
	installer.Runner = func(_ context.Context, name string, args ...string) (string, error) {
		call = runnerCall{name: name, args: args}
		return "", nil
	}

	_, err := installer.Install(context.Background(), InstallSpec{
		Source:        SourceChoco,
		ID:            "vendor/app",
		InstallerURL:  server.URL + "/app.exe",
		SHA256:        sha256Of(payload),
		InstallerType: "exe",
		SilentArgs:    "/S",
	})
	if err != nil {
		t.Fatalf("instalacao exe falhou: %v", err)
	}
	if filepath.Ext(call.name) != ".exe" {
		t.Fatalf("esperava executar o .exe baixado; got %q", call.name)
	}
	if len(call.args) != 1 || call.args[0] != "/S" {
		t.Fatalf("switches silenciosos inesperados: %+v", call.args)
	}
}

func TestVerifyFileSHA256(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/file.bin"
	payload := []byte("dados")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := VerifyFileSHA256(path, sha256Of(payload)); err != nil {
		t.Fatalf("hash correto deveria passar: %v", err)
	}
	if err := VerifyFileSHA256(path, sha256Of([]byte("outro"))); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("hash errado deveria falhar: %v", err)
	}
}
