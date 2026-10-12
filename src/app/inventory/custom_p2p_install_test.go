package inventory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discovery/app/appstore"
	"discovery/app/debug"
)

// TestRunCustomInstallCommandPrefereP2PEViraSeed cobre a distribuição P2P dos
// instaladores de apps custom:
//
//  1. primeira execução: não há artifact na rede → baixa da API, publica no
//     cache P2P (vira seed) e instala a partir do arquivo publicável;
//  2. segunda execução: o artifact já está no cache/rede → NÃO bate na API.
func TestRunCustomInstallCommandPrefereP2PEViraSeed(t *testing.T) {
	payload := append([]byte{'M', 'Z', 0x90, 0x00}, []byte("instalador-custom")...)
	apiHits := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiHits++
		// Caso real do foxitedito: nome sem extensão no Content-Disposition.
		w.Header().Set("Content-Disposition", `attachment; filename="foxitedito"`)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	p2pDir := t.TempDir()
	publishedSource := ""
	publishedPackage := ""
	publishedVersion := ""
	fetchCalls := 0

	svc := NewService(Options{
		DebugConfig: func() debug.Config {
			return debug.Config{
				ApiInsecure: true, // httptest é http
				ApiServer:   strings.TrimPrefix(server.URL, "http://"),
				AuthToken:   "mdz_test_token",
				AgentID:     "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
			}
		},
		Ctx:  func() context.Context { return context.Background() },
		Logf: func(string) {},
		PublishCustomInstallerForP2P: func(_ context.Context, packageID, version, installerPath string) (string, error) {
			publishedPackage = packageID
			publishedVersion = version
			publishedSource = installerPath

			// Reproduz o PublishFileWithIDAndVersion: copia para o P2P_Temp com
			// o nome canônico e devolve o caminho publicado.
			target := filepath.Join(p2pDir, "custom-foxitedito.exe")
			data, err := os.ReadFile(installerPath)
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(target, data, 0o644); err != nil {
				return "", err
			}
			return target, nil
		},
		CustomInstallerFromP2P: func(_ context.Context, packageID, version string) (string, bool) {
			fetchCalls++
			if fetchCalls == 1 {
				return "", false // primeira vez: nada publicado na rede
			}
			return filepath.Join(p2pDir, "custom-foxitedito.exe"), true
		},
	})

	item := appstore.Item{
		InstallationType: string(appstore.InstallationCustom),
		PackageID:        "foxitedito",
		Version:          "2",
		FileURL:          "/file",
		FileSizeBytes:    int64(len(payload)),
		// Comando inofensivo que só devolve o caminho resolvido de {{installer}}.
		InstallCommand: `echo {{installer}}`,
	}

	// ── 1ª execução: baixa da API, publica e instala a partir do P2P ─────────
	out, err := svc.runCustomInstallCommand(context.Background(), item, "foxitedito")
	if err != nil {
		t.Fatalf("instalação deveria ter sucesso: %v (out=%q)", err, out)
	}
	if apiHits != 1 {
		t.Fatalf("esperava 1 download da API, houve %d", apiHits)
	}
	if publishedPackage != "foxitedito" || publishedVersion != "2" {
		t.Fatalf("publicação com packageId/versão errados: %q/%q", publishedPackage, publishedVersion)
	}
	if filepath.Base(publishedSource) != "foxitedito.exe" {
		t.Fatalf("deveria publicar o arquivo já com extensão, veio %q", filepath.Base(publishedSource))
	}
	publishedPath := filepath.Join(p2pDir, "custom-foxitedito.exe")
	if !strings.Contains(out, publishedPath) {
		t.Fatalf("a instalação deveria rodar a partir do artifact P2P %q, saída=%q", publishedPath, out)
	}
	if !strings.Contains(out, publishedPath) || strings.Contains(out, "discovery-custom-") {
		t.Fatalf("a instalação não deveria usar a pasta temporária, saída=%q", out)
	}

	// ── 2ª execução: artifact já no cache P2P → nenhuma chamada à API ────────
	out, err = svc.runCustomInstallCommand(context.Background(), item, "foxitedito")
	if err != nil {
		t.Fatalf("segunda instalação deveria ter sucesso: %v (out=%q)", err, out)
	}
	if apiHits != 1 {
		t.Fatalf("não deveria baixar da API de novo; downloads=%d", apiHits)
	}
	if !strings.Contains(out, publishedPath) {
		t.Fatalf("segunda instalação deveria usar o artifact do P2P, saída=%q", out)
	}
}
