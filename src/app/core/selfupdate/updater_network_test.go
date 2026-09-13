package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShortHash cobre o guard anti-panic do truncamento de hash (A9).
func TestShortHash(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"123456789012", "123456789012"},  // exatamente 12
		{"1234567890123", "123456789012"}, // 13 → trunca
		{"  abcdef12345678  ", "abcdef12345678"[:12]},
	}
	for _, c := range cases {
		if got := shortHash(c.in); got != c.want {
			t.Fatalf("shortHash(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestVerifyInstallerSHA256 cobre as regras C1/C2: ausência de qualquer lado
// e divergência são fatais; comparação é case-insensitive.
func TestVerifyInstallerSHA256(t *testing.T) {
	updater := &Updater{}

	if err := updater.verifyInstallerSHA256(strings.Repeat("a", 64), strings.Repeat("A", 64)); err != nil {
		t.Fatalf("hashes iguais (case-insensitive) devem passar: %v", err)
	}

	if err := updater.verifyInstallerSHA256("", strings.Repeat("a", 64)); err == nil {
		t.Fatalf("esperado ausente deve ser erro (C1/C2)")
	}
	if err := updater.verifyInstallerSHA256(strings.Repeat("a", 64), ""); err == nil {
		t.Fatalf("obtido ausente deve ser erro (C1/C2)")
	}
	if err := updater.verifyInstallerSHA256("", ""); err == nil {
		t.Fatalf("ambos ausentes devem ser erro (C1/C2)")
	}

	err := updater.verifyInstallerSHA256(strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err == nil || !strings.Contains(err.Error(), "divergente") {
		t.Fatalf("divergência deve ser erro com mensagem 'divergente', got: %v", err)
	}
}

// TestDownloadFromCacheOrPublic_RefusesDownloadWithoutServerHash valida o C2:
// sem hash de referência do servidor, NENHUM download é iniciado — o P2P não
// é consultado e o fluxo aborta com erro.
func TestDownloadFromCacheOrPublic_RefusesDownloadWithoutServerHash(t *testing.T) {
	p2pQueries := 0
	downloads := 0
	updater := &Updater{
		ApiScheme: "http",
		// Servidor indisponível (porta fechada) — fetch do hash falha.
		ApiServer: "127.0.0.1:1",
		FindPeersByReleaseID: func(ctx context.Context, artifactID string) ([]string, error) {
			p2pQueries++
			return []string{"peer-1"}, nil
		},
		DownloadFromPeer: func(ctx context.Context, artifactID, peerID string) (string, error) {
			downloads++
			return "", nil
		},
		TempDir: t.TempDir(),
	}

	_, _, fromP2P, err := updater.downloadFromCacheOrPublic(context.Background(), "")
	if err == nil {
		t.Fatalf("esperado erro: sem hash do servidor nenhum download pode ocorrer (C1/C2)")
	}
	if !strings.Contains(err.Error(), "sha256 do servidor indisponivel") {
		t.Fatalf("erro inesperado: %v", err)
	}
	if fromP2P {
		t.Fatalf("fromP2P = true, want false (P2P recusado sem hash)")
	}
	if p2pQueries != 0 {
		t.Fatalf("consultas P2P = %d, want 0 (P2P deve ser recusado sem hash de referência)", p2pQueries)
	}
	if downloads != 0 {
		t.Fatalf("downloads P2P = %d, want 0", downloads)
	}
}

// TestDownloadFromCacheOrPublic_HTTPMismatchAbortsAndRemoves valida o C1 no
// caminho HTTP: hash do arquivo divergente do hash publicado pelo servidor
// aborta, remove o arquivo temporário e não retorna path utilizável.
func TestDownloadFromCacheOrPublic_HTTPMismatchAbortsAndRemoves(t *testing.T) {
	payload := []byte("update payload")
	checksum := sha256.Sum256(payload)
	serverHashHex := strings.ToLower(hex.EncodeToString(checksum[:]))

	tamperPayload := []byte("TAMPERED payload — build/peering envenenado")

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/download/agent/sha256":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(serverHashHex)) // hash que o servidor declara
		case "/api/v1/download/agent":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(tamperPayload) // conteúdo com hash diferente
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiServer.Close()

	apiURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}

	tempDir := t.TempDir()
	updater := &Updater{
		ApiScheme: apiURL.Scheme,
		ApiServer: apiURL.Host,
		TempDir:   tempDir,
	}

	_, _, fromP2P, err := updater.downloadFromCacheOrPublic(context.Background(), "")
	if err == nil {
		t.Fatalf("esperado erro: hash divergente deve abortar (C1)")
	}
	if !strings.Contains(err.Error(), "divergente") {
		t.Fatalf("erro inesperado: %v", err)
	}
	if fromP2P {
		t.Fatalf("fromP2P = true, want false")
	}
	// Nenhum arquivo pode ter sobrado no TempDir.
	entries, readErr := os.ReadDir(tempDir)
	if readErr != nil {
		t.Fatalf("ReadDir: %v", readErr)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".exe" {
			t.Fatalf("arquivo de download sobreviveu ao abort: %s", e.Name())
		}
	}
}
