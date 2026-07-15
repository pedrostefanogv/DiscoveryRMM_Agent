package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"discovery/app/netutil"
	"discovery/internal/buildinfo"
	"discovery/internal/errutil"
)

func (u *Updater) ResumePendingInstallReport(ctx context.Context) {
	state, err := u.loadPendingInstallState()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		u.logf("falha ao carregar estado pendente de install: %v", err)
		return
	}
	currentVersion := strings.TrimSpace(buildinfo.Version)
	if currentVersion == "" {
		currentVersion = "0.0.0"
	}
	if compareVersions(currentVersion, state.TargetVersion) < 0 {
		u.logf("estado pendente de install mantido: versao atual=%s target=%s", currentVersion, state.TargetVersion)
		return
	}
	u.logf("estado pendente de install resolvido: versao atual=%s >= target=%s (limpo)", currentVersion, state.TargetVersion)
	u.clearPendingInstallState()
}

// fetchPublicSHA256 obtem o SHA256 do build atual sem baixar o binario.
// Usa o endpoint /api/v1/download/agent/sha256 que retorna text/plain.
func (u *Updater) fetchPublicSHA256(ctx context.Context) (string, error) {
	endpoint := u.apiScheme() + "://" + u.apiServer() + "/api/v1/download/agent/sha256"
	ctxReq, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctxReq, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024))
		return "", fmt.Errorf("sha256 endpoint status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	shaBytes, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(shaBytes))
	if len(sha) != 64 {
		return "", fmt.Errorf("sha256 invalido: tamanho=%d esperado=64", len(sha))
	}
	return strings.ToLower(sha), nil
}

// downloadFromCacheOrPublic tenta baixar o artifact via P2P (se peers disponiveis)
// e faz fallback para o endpoint publico /api/v1/download/agent.
// expectedSHA256: se informado, monta artifactID = "selfupdate:" + sha256 e valida pos-download.
// Retorna o path do arquivo temporario e o SHA256 calculado localmente.
func (u *Updater) downloadFromCacheOrPublic(ctx context.Context, expectedSHA256 string) (string, string, error) {
	artifactID := "selfupdate:current"
	if expectedSHA256 != "" {
		artifactID = "selfupdate:" + strings.ToLower(expectedSHA256)
	} else {
		// Sem SHA256 conhecido, tenta obter do servidor antes do P2P.
		if sha, err := u.fetchPublicSHA256(ctx); err == nil && sha != "" {
			expectedSHA256 = sha
			artifactID = "selfupdate:" + sha
			u.logf("[selfupdate] SHA256 obtido do servidor: %s (nao baixou binario)", sha[:12])
		} else {
			u.logf("[selfupdate] nao foi possivel obter SHA256 do servidor: %v — P2P usara ID generico", err)
		}
	}

	// ── P2P-first ──
	if u.FindPeersByReleaseID != nil && u.DownloadFromPeer != nil {
		peers, findErr := u.FindPeersByReleaseID(ctx, artifactID)
		if findErr != nil {
			u.logf("[selfupdate] consulta P2P falhou (nao-critico): %v", findErr)
		}
		if len(peers) > 0 {
			u.logf("[selfupdate] P2P: %d peer(s) encontrados para artifactID=%s", len(peers), artifactID)
			for _, peerID := range peers {
				path, dlErr := u.DownloadFromPeer(ctx, artifactID, peerID)
				if dlErr != nil {
					u.logf("[selfupdate] P2P download do peer %s falhou: %v", peerID, dlErr)
					continue
				}
				actual, shaErr := fileSHA256(path)
				if shaErr != nil {
					u.logf("[selfupdate] P2P sha256 do peer %s falhou: %v", peerID, shaErr)
					_ = os.Remove(path)
					continue
				}
				if expectedSHA256 != "" && !strings.EqualFold(actual, expectedSHA256) {
					u.logf("[selfupdate] P2P sha256 mismatch do peer %s: esperado=%s obtido=%s (stale)", peerID, expectedSHA256[:12], actual[:12])
					_ = os.Remove(path)
					continue
				}
				u.logf("[selfupdate] download P2P concluido: peer=%s artifactID=%s sha256=%s", peerID, artifactID, actual[:12])
				return path, actual, nil
			}
			u.logf("[selfupdate] P2P exaurido (%d peers tentados), fallback para HTTP", len(peers))
		} else {
			u.logf("[selfupdate] P2P: nenhum peer com artifactID=%s, usando HTTP", artifactID)
		}
	}

	// ── HTTP download do endpoint publico ──
	downloadURL := u.apiScheme() + "://" + u.apiServer() + "/api/v1/download/agent"
	return u.downloadFromURL(ctx, downloadURL)
}

// downloadFromURL faz o download do instalador a partir de uma URL.
// Retorna o caminho do arquivo temporario e o SHA256 do conteudo.
func (u *Updater) downloadFromURL(ctx context.Context, downloadURL string) (string, string, error) {
	if err := os.MkdirAll(u.TempDir, 0o755); err != nil {
		return "", "", err
	}

	path := filepath.Join(u.TempDir, fmt.Sprintf("discovery-update-%s.exe", uuid.NewString()))
	f, err := os.Create(path)
	if err != nil {
		return "", "", err
	}

	ctxDownload, cancel := context.WithDeadline(ctx, time.Now().Add(downloadDeadline))
	defer cancel()

	req, err := http.NewRequestWithContext(ctxDownload, http.MethodGet, downloadURL, nil)
	if err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos erro de request")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download temp")
		return "", "", err
	}

	// Endpoint publico (AllowAnonymous) — envia token apenas para consistencia/logging.
	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token != "" && agentID != "" {
		_ = netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID)
	}

	u.logf("[selfupdate] baixando de %s", downloadURL)

	client := &http.Client{Timeout: downloadDeadline}
	resp, err := client.Do(req)
	if err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos falha HTTP")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha HTTP")
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo status != 200")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download status != 200")
		return "", "", fmt.Errorf("download status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	buf := make([]byte, 128*1024)
	if _, err := io.CopyBuffer(f, resp.Body, buf); err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos falha de copy")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de copy")
		return "", "", err
	}
	if err := f.Sync(); err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos falha de sync")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de sync")
		return "", "", err
	}
	if err := f.Close(); err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de close")
		return "", "", err
	}

	sha, err := fileSHA256(path)
	if err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha sha256")
		return "", "", err
	}

	u.logf("[selfupdate] download concluido: path=%s sha256=%s", path, sha[:12])
	return path, sha, nil
}

// reportEvent is a no-op — o endpoint /api/v1/agent-auth/me/update/report nao existe na API.
// Mantido como stub para nao quebrar chamadas existentes no codigo.
func (u *Updater) reportEvent(_ context.Context, _ string, _ reportOpts) {}
