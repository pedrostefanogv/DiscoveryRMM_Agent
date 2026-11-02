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

// A desinstalação também publica o instalador no cache P2P (vira seed): quem só
// remove o app poupa o download dos outros agents do site. A execução, porém,
// continua na cópia LOCAL — o artifact publicado tem nome canônico
// (custom-<pkg>-v<ver>.exe) e comandos de remoção podem citar o nome original em
// {{installerDir}}.
func TestPrepareCustomInstallerAssetUninstallPublishesButKeepsLocalCopy(t *testing.T) {
	payload := append([]byte{'M', 'Z', 0x90, 0x00}, []byte("instalador-custom")...)
	apiHits := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiHits++
		w.Header().Set("Content-Disposition", `attachment; filename="foxitedito.exe"`)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	p2pDir := t.TempDir()
	publishedPackage := ""
	publishedVersion := ""
	publishedSource := ""
	fetchCalls := 0

	svc := NewService(Options{
		DebugConfig: func() debug.Config {
			return debug.Config{
				ApiInsecure: true,
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

			target := filepath.Join(p2pDir, "custom-foxitedito-v2.exe")
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
			return filepath.Join(p2pDir, "custom-foxitedito-v2.exe"), true
		},
	})

	item := appstore.Item{
		InstallationType: string(appstore.InstallationCustom),
		PackageID:        "foxitedito",
		Version:          "2",
		FileURL:          "/file",
		FileSizeBytes:    int64(len(payload)),
	}

	// ── 1ª execução (desinstalação): baixa da API, publica no P2P e executa a
	// cópia local ────────────────────────────────────────────────────────────
	installerPath, workDir, err := svc.prepareCustomInstallerAsset(context.Background(), item, "foxitedito", customAssetUninstall)
	if err != nil {
		t.Fatalf("preparo do instalador falhou: %v", err)
	}
	if apiHits != 1 {
		t.Fatalf("esperava 1 download da API, houve %d", apiHits)
	}
	if publishedPackage != "foxitedito" || publishedVersion != "2" {
		t.Fatalf("publicação com packageId/versão errados: %q/%q", publishedPackage, publishedVersion)
	}
	if filepath.Base(publishedSource) != "foxitedito.exe" {
		t.Fatalf("deveria publicar o arquivo baixado, veio %q", filepath.Base(publishedSource))
	}
	if !strings.Contains(installerPath, "discovery-custom-") {
		t.Fatalf("desinstalação deveria executar a cópia local, veio %q", installerPath)
	}
	if workDir != filepath.Dir(installerPath) {
		t.Fatalf("workDir = %q, esperado %q", workDir, filepath.Dir(installerPath))
	}
	if _, statErr := os.Stat(filepath.Join(p2pDir, "custom-foxitedito-v2.exe")); statErr != nil {
		t.Fatalf("artifact deveria ter sido publicado no P2P: %v", statErr)
	}

	// ── 2ª execução: artifact já no cache P2P → nenhuma chamada à API e a
	// desinstalação passa a usar o artifact publicado ────────────────────────
	installerPath, _, err = svc.prepareCustomInstallerAsset(context.Background(), item, "foxitedito", customAssetUninstall)
	if err != nil {
		t.Fatalf("segundo preparo falhou: %v", err)
	}
	if apiHits != 1 {
		t.Fatalf("não deveria baixar da API de novo; downloads=%d", apiHits)
	}
	if !strings.Contains(installerPath, "custom-foxitedito-v2.exe") {
		t.Fatalf("segunda desinstalação deveria usar o artifact do P2P, veio %q", installerPath)
	}
}
