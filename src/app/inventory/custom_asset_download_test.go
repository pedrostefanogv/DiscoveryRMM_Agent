package inventory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discovery/app/appstore"
	"discovery/app/debug"
)

// TestDownloadCustomAssetValidaIntegridade cobre as guardas adicionadas ao
// download do instalador custom: checksum SHA-256, tamanho esperado e caminho
// feliz (arquivo gravado em pasta temporária).
func TestDownloadCustomAssetValidaIntegridade(t *testing.T) {
	payload := []byte("conteudo-do-instalador-custom")
	sum := sha256.Sum256(payload)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file" {
			t.Fatalf("path inesperado: %s", r.URL.Path)
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	newService := func() *Service {
		return NewService(Options{
			DebugConfig: func() debug.Config {
				return debug.Config{
					ApiInsecure: true, // httptest é http
					ApiServer:   strings.TrimPrefix(server.URL, "http://"),
					AuthToken:   "mdz_test_token",
					AgentID:     "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
				}
			},
			Ctx: func() context.Context { return context.Background() },
		})
	}

	t.Run("checksum e tamanho corretos", func(t *testing.T) {
		path, err := newService().downloadCustomAsset(context.Background(), appstore.Item{
			FileURL:       "/file",
			FileSizeBytes: int64(len(payload)),
			FileChecksum:  hex.EncodeToString(sum[:]),
		}, "foxitedito")
		if err != nil {
			t.Fatalf("download deveria ter sucesso: %v", err)
		}
		defer func() { _ = os.RemoveAll(filepath.Dir(path)) }()

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ler arquivo baixado: %v", err)
		}
		if !bytes.Equal(data, payload) {
			t.Fatalf("conteúdo baixado difere do servido")
		}
	})

	t.Run("checksum divergente falha", func(t *testing.T) {
		_, err := newService().downloadCustomAsset(context.Background(), appstore.Item{
			FileURL:      "/file",
			FileChecksum: strings.Repeat("0", 64),
		}, "foxitedito")
		if err == nil || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("esperava erro de checksum, recebeu: %v", err)
		}
	})

	t.Run("tamanho divergente falha", func(t *testing.T) {
		_, err := newService().downloadCustomAsset(context.Background(), appstore.Item{
			FileURL:       "/file",
			FileSizeBytes: int64(len(payload) + 10),
		}, "foxitedito")
		if err == nil || !strings.Contains(err.Error(), "tamanho") {
			t.Fatalf("esperava erro de tamanho, recebeu: %v", err)
		}
	})

	t.Run("checksum legado (ETag/MD5) é ignorado", func(t *testing.T) {
		// Pacotes antigos gravaram o ETag do S3 (32 hex), não SHA-256: não pode
		// bloquear a instalação de um pacote que já funcionava.
		path, err := newService().downloadCustomAsset(context.Background(), appstore.Item{
			FileURL:      "/file",
			FileChecksum: strings.Repeat("a", 32),
		}, "foxitedito")
		if err != nil {
			t.Fatalf("checksum legado não deveria bloquear: %v", err)
		}
		defer func() { _ = os.RemoveAll(filepath.Dir(path)) }()
	})

	t.Run("config incompleta falha", func(t *testing.T) {
		svc := NewService(Options{Ctx: func() context.Context { return context.Background() }})
		_, err := svc.downloadCustomAsset(context.Background(), appstore.Item{FileURL: "/file"}, "foxitedito")
		if err == nil || !strings.Contains(err.Error(), "indisponível") {
			t.Fatalf("esperava erro de configuração, recebeu: %v", err)
		}
	})
}

// TestDownloadCustomAssetCompletaExtensaoDoInstalador reproduz o caso real do
// app foxitedito: o servidor manda Content-Disposition apenas com o packageId
// (sem extensão) e o binário é um PE. O arquivo precisa ser gravado com .exe —
// `cmd /C` não inicia executável sem extensão — e o conteúdo/tamanho não podem
// ser alterados pela leitura usada na detecção da assinatura.
func TestDownloadCustomAssetCompletaExtensaoDoInstalador(t *testing.T) {
	payload := append([]byte{'M', 'Z', 0x90, 0x00}, bytes.Repeat([]byte{0xAB}, 4096)...)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sem-extensao":
			w.Header().Set("Content-Disposition", `attachment; filename="foxitedito"`)
		case "/com-extensao":
			w.Header().Set("Content-Disposition", `attachment; filename="FoxitPDFEditor.exe"`)
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	svc := NewService(Options{
		DebugConfig: func() debug.Config {
			return debug.Config{
				ApiInsecure: true,
				ApiServer:   strings.TrimPrefix(server.URL, "http://"),
				AuthToken:   "mdz_test_token",
				AgentID:     "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
			}
		},
		Ctx: func() context.Context { return context.Background() },
	})

	t.Run("nome sem extensão vira .exe", func(t *testing.T) {
		path, err := svc.downloadCustomAsset(context.Background(), appstore.Item{
			FileURL:       "/sem-extensao",
			FileSizeBytes: int64(len(payload)),
		}, "foxitedito")
		if err != nil {
			t.Fatalf("download deveria ter sucesso: %v", err)
		}
		defer func() { _ = os.RemoveAll(filepath.Dir(path)) }()

		if filepath.Base(path) != "foxitedito.exe" {
			t.Fatalf("esperava foxitedito.exe, veio %q", filepath.Base(path))
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ler arquivo baixado: %v", err)
		}
		if !bytes.Equal(data, payload) {
			t.Fatalf("conteúdo baixado difere do servido (%d bytes vs %d)", len(data), len(payload))
		}
	})

	t.Run("nome com extensão é preservado", func(t *testing.T) {
		path, err := svc.downloadCustomAsset(context.Background(), appstore.Item{
			FileURL:       "/com-extensao",
			FileSizeBytes: int64(len(payload)),
		}, "foxitedito")
		if err != nil {
			t.Fatalf("download deveria ter sucesso: %v", err)
		}
		defer func() { _ = os.RemoveAll(filepath.Dir(path)) }()

		if filepath.Base(path) != "FoxitPDFEditor.exe" {
			t.Fatalf("nome deveria ser preservado, veio %q", filepath.Base(path))
		}
	})
}

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		512:                    "512 B",
		2048:                   "2.00 KB",
		5 * 1024 * 1024:        "5.00 MB",
		2 * 1024 * 1024 * 1024: "2.00 GB",
	}
	for value, want := range cases {
		if got := humanBytes(value); got != want {
			t.Fatalf("humanBytes(%d) = %q, esperado %q", value, got, want)
		}
	}
}
