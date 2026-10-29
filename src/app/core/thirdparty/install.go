package thirdparty

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// WU-21/D24 (segurança): instalação a partir do CATÁLOGO curado.
//
// O catálogo fixa URL do fornecedor + SHA-256 + switches silenciosos. O agente:
//  1. baixa o instalador da URL oficial (nunca redistribuímos binário);
//  2. verifica o SHA-256 ANTES de executar (hash divergente = aborta);
//  3. opcionalmente verifica a assinatura Authenticode (Windows);
//  4. executa com os switches validados em VM.
//
// Quando o payload NÃO traz metadados de catálogo, o agente mantém o caminho
// anterior (upgrade pelo próprio gerenciador) — a curadoria é opt-in por app.

// ErrHashMismatch indica divergência entre o SHA-256 esperado e o baixado.
var ErrHashMismatch = errors.New("hash SHA-256 do instalador nao confere")

// InstallSpec descreve como instalar UM app de catálogo.
type InstallSpec struct {
	Source        string
	ID            string
	InstallerURL  string
	SHA256        string
	SilentArgs    string
	InstallerType string
}

// HasCatalogIntegrity informa se o spec tem URL+hash (caminho curado).
func (s InstallSpec) HasCatalogIntegrity() bool {
	return strings.TrimSpace(s.InstallerURL) != "" && strings.TrimSpace(s.SHA256) != ""
}

// CommandRunner executa um comando (injetável nos testes).
type CommandRunner func(ctx context.Context, name string, args ...string) (string, error)

// Installer baixa/verifica/executa instaladores de catálogo.
type Installer struct {
	Client   *http.Client
	Runner   CommandRunner
	TempDir  string
	MaxBytes int64
	Timeout  time.Duration
	// SignatureVerifier valida Authenticode; nil = verifyAuthenticode da
	// plataforma. Injetável para testes (o CI Windows não tem binário assinado
	// de teste) e para plataformas sem Authenticode.
	SignatureVerifier func(path string) error
}

// NewInstaller devolve o instalador com defaults seguros.
func NewInstaller() *Installer {
	return &Installer{
		Client:   &http.Client{Timeout: 10 * time.Minute},
		Runner:   runCommand,
		MaxBytes: 2 << 30, // 2 GiB: instalador maior que isso é bug/abuso
		Timeout:  20 * time.Minute,
	}
}

// Install baixa o instalador, valida o hash e executa com os switches.
func (i *Installer) Install(ctx context.Context, spec InstallSpec) (string, error) {
	if !spec.HasCatalogIntegrity() {
		return "", errors.New("spec sem URL/SHA-256 de catalogo")
	}
	if !ValidWingetID(spec.ID) && !ValidAppID(spec.ID) {
		return "", ErrInvalidAppID
	}
	if !strings.HasPrefix(strings.ToLower(spec.InstallerURL), "https://") {
		return "", errors.New("installerUrl precisa ser https")
	}

	installer := i
	if installer.Client == nil {
		installer = NewInstaller()
	}

	workDir := installer.TempDir
	if workDir == "" {
		dir, err := os.MkdirTemp("", "discovery-patch-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		workDir = dir
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}

	target := filepath.Join(workDir, sanitizeFileName(spec.ID)+installerExtension(spec))
	if err := installer.download(ctx, spec.InstallerURL, target); err != nil {
		return "", err
	}
	if err := VerifyFileSHA256(target, spec.SHA256); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	// WU-21/D24: hash garante integridade; Authenticode garante autoria. Falha
	// é fatal (não executamos binário sem assinatura válida no Windows).
	verifier := installer.SignatureVerifier
	if verifier == nil {
		verifier = verifyAuthenticode
	}
	if err := verifier(target); err != nil {
		_ = os.Remove(target)
		return "", err
	}

	output, err := installer.execute(ctx, spec, target)
	if err != nil {
		return output, fmt.Errorf("executar instalador: %w", err)
	}
	return output, nil
}

func (i *Installer) download(ctx context.Context, url, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := i.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download retornou HTTP %d", response.StatusCode)
	}

	maxBytes := i.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 2 << 30
	}
	file, err := os.Create(target)
	if err != nil {
		return err
	}
	defer file.Close()

	written, err := io.Copy(file, io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if written > maxBytes {
		return errors.New("instalador excede o tamanho maximo permitido")
	}
	return nil
}

func (i *Installer) execute(ctx context.Context, spec InstallSpec, installerPath string) (string, error) {
	runCtx := ctx
	if i.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, i.Timeout)
		defer cancel()
	}

	runner := i.Runner
	if runner == nil {
		runner = runCommand
	}

	installerType := strings.ToLower(strings.TrimSpace(spec.InstallerType))
	if installerType == "msi" || (installerType == "" && strings.HasSuffix(strings.ToLower(installerPath), ".msi")) {
		args := []string{"/i", installerPath, "/qn", "/norestart"}
		if extra := strings.TrimSpace(spec.SilentArgs); extra != "" {
			args = append(args, strings.Fields(extra)...)
		}
		return runner(runCtx, "msiexec", args...)
	}

	args := []string{}
	if extra := strings.TrimSpace(spec.SilentArgs); extra != "" {
		args = append(args, strings.Fields(extra)...)
	}
	return runner(runCtx, installerPath, args...)
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("%s falhou: %w", name, err)
	}
	return text, nil
}

// VerifyFileSHA256 compara o hash do arquivo com o esperado (hex, case-insensitive).
func VerifyFileSHA256(path, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if len(expected) != 64 {
		return errors.New("hash esperado invalido (precisa de 64 hex)")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != expected {
		return fmt.Errorf("%w: esperado %s, obtido %s", ErrHashMismatch, expected, actual)
	}
	return nil
}

// VerifyFileSHA256Value devolve o hash de um arquivo (para testes/log).
func FileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func sanitizeFileName(id string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(id)
}

func installerExtension(spec InstallSpec) string {
	switch strings.ToLower(strings.TrimSpace(spec.InstallerType)) {
	case "msi", "msix":
		return ".msi"
	case "exe", "inno", "nsis", "wix", "burn", "":
		return ".exe"
	default:
		return ".bin"
	}
}
