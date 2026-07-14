package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	u.reportEvent(ctx, "InstallSucceeded", reportOpts{
		ReleaseID:      state.ReleaseID,
		CurrentVersion: state.CurrentVersion,
		TargetVersion:  state.TargetVersion,
		CorrelationID:  state.CorrelationID,
		Message:        "instalacao confirmada apos reinicio do processo",
	})
	u.clearPendingInstallState()
}

func (u *Updater) fetchManifest(ctx context.Context) (*UpdateManifest, error) {
	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token == "" {
		return nil, errors.New("token vazio")
	}
	if agentID == "" {
		return nil, errors.New("agentId vazio")
	}

	endpoint := u.apiScheme() + "://" + u.apiServer() + "/api/v1/agent-auth/me/update/manifest"
	q := url.Values{}
	q.Set("currentVersion", strings.TrimSpace(buildinfo.Version))
	q.Set("platform", platformWindows)
	q.Set("architecture", architectureAMD64)
	q.Set("artifactType", normalizeArtifactType(u.policy().PreferredArtifactType))
	endpoint += "?" + q.Encode()

	ctxReq, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctxReq, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: manifestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return nil, fmt.Errorf("manifest status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var manifest UpdateManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (u *Updater) downloadToTemp(ctx context.Context, m *UpdateManifest) (string, error) {
	if m == nil {
		return "", errors.New("manifest nil")
	}
	if m.Sha256 == nil || strings.TrimSpace(*m.Sha256) == "" {
		return "", errors.New("sha256 ausente no manifest")
	}
	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token == "" {
		return "", errors.New("token vazio")
	}
	if agentID == "" {
		return "", errors.New("agentId vazio")
	}

	expectedSHA256 := strings.ToLower(strings.TrimSpace(*m.Sha256))
	releaseID := ""
	if m.ReleaseID != nil {
		releaseID = strings.TrimSpace(*m.ReleaseID)
	}
	targetVersion := ""
	if m.LatestVersion != nil {
		targetVersion = strings.TrimSpace(*m.LatestVersion)
	}

	// ── P2P-first: tenta baixar de peers antes de ir para HTTP ──
	artifactID := releaseID
	if artifactID == "" {
		// Fallback: deriva ArtifactID do SHA256 quando não há releaseID
		artifactID = "sha256:" + expectedSHA256
	}

	if u.FindPeersByReleaseID != nil && u.DownloadFromPeer != nil {
		peers, findErr := u.FindPeersByReleaseID(ctx, artifactID)
		if findErr != nil {
			u.logf("[selfupdate] consulta P2P falhou (não-crítico): %v", findErr)
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
				if !strings.EqualFold(actual, expectedSHA256) {
					u.logf("[selfupdate] P2P sha256 mismatch do peer %s: esperado=%s obtido=%s", peerID, expectedSHA256[:12], actual[:12])
					_ = os.Remove(path)
					continue
				}
				u.logf("[selfupdate] download P2P concluido: peer=%s artifactID=%s", peerID, artifactID)
				return path, nil
			}
			u.logf("[selfupdate] P2P exaurido (%d peers tentados), fallback para HTTP", len(peers))
		} else {
			u.logf("[selfupdate] P2P: nenhum peer com artifactID=%s, usando HTTP", artifactID)
		}
	}

	// ── HTTP download (comportamento original) ──
	if err := os.MkdirAll(u.TempDir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(u.TempDir, fmt.Sprintf("discovery-update-%s.exe", uuid.NewString()))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer func() {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo de download")
	}()

	endpoint := u.apiScheme() + "://" + u.apiServer() + "/api/v1/agent-auth/me/update/download"
	q := url.Values{}
	if m.ReleaseID != nil && strings.TrimSpace(*m.ReleaseID) != "" {
		q.Set("releaseId", strings.TrimSpace(*m.ReleaseID))
	}
	if m.LatestVersion != nil && strings.TrimSpace(*m.LatestVersion) != "" {
		q.Set("version", strings.TrimSpace(*m.LatestVersion))
	}
	policy := u.policy()
	artifactType := strings.TrimSpace(policy.PreferredArtifactType)
	if artifactType == "" {
		artifactType = strings.TrimSpace(m.ArtifactType)
	}
	artifactType = normalizeArtifactType(artifactType)
	q.Set("platform", platformWindows)
	q.Set("architecture", architectureAMD64)
	q.Set("artifactType", artifactType)
	endpoint += "?" + q.Encode()

	ctxDownload, cancel := context.WithDeadline(ctx, time.Now().Add(downloadDeadline))
	defer cancel()

	req, err := http.NewRequestWithContext(ctxDownload, http.MethodGet, endpoint, nil)
	if err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download temp")
		return "", err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download credenciais invalidas")
		return "", err
	}

	client := &http.Client{Timeout: downloadDeadline}
	resp, err := client.Do(req)
	if err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha HTTP")
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()

		// Fallback: tenta o endpoint público para rebuilds de mesma versão.
		publicURL := u.apiScheme() + "://" + u.apiServer() + "/api/v1/agent-download"
		u.logf("selfupdate: download autenticado retornou %d — tentando endpoint público: %s", resp.StatusCode, publicURL)

		req2, err2 := http.NewRequestWithContext(ctxDownload, http.MethodGet, publicURL, nil)
		if err2 != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback erro request")
			return "", fmt.Errorf("download status=%d (fallback request error: %v)", resp.StatusCode, err2)
		}
		// Public endpoint is AllowAnonymous but we still send auth for consistency.
		if err2 := netutil.SetAgentAuthHeadersWithAgentID(req2, token, agentID); err2 != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback credenciais")
			return "", fmt.Errorf("download status=%d (fallback auth error: %v)", resp.StatusCode, err2)
		}

		resp2, err2 := client.Do(req2)
		if err2 != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback falha HTTP")
			return "", fmt.Errorf("download status=%d (fallback HTTP error: %v)", resp.StatusCode, err2)
		}
		defer resp2.Body.Close()

		if resp2.StatusCode != http.StatusOK {
			body2, _ := io.ReadAll(io.LimitReader(resp2.Body, 8*1024))
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback status != 200")
			return "", fmt.Errorf("download status=%d (fallback status=%d body=%s)", resp.StatusCode, resp2.StatusCode, strings.TrimSpace(string(body2)))
		}

		u.logf("selfupdate: fallback público OK — baixando de %s", publicURL)
		buf := make([]byte, 128*1024)
		if _, err := io.CopyBuffer(f, resp2.Body, buf); err != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback apos falha de copy")
			return "", err
		}
		if err := f.Sync(); err != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback apos falha de sync")
			return "", err
		}
		if err := f.Close(); err != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback apos falha de close")
			return "", err
		}

		actual, err := fileSHA256(path)
		if err != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback apos falha sha256")
			return "", err
		}
		if expectedSHA256 != "" && actual != expectedSHA256 {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download fallback sha256 mismatch")
			return "", fmt.Errorf("sha256 mismatch (fallback): expected=%s got=%s", expectedSHA256, actual)
		}

		// Publica no P2P após download HTTP bem-sucedido
		if u.OnArtifactReady != nil {
			_ = u.OnArtifactReady(ctx, path, artifactID, expectedSHA256, targetVersion)
		}

		return path, nil
	}

	buf := make([]byte, 128*1024)
	if _, err := io.CopyBuffer(f, resp.Body, buf); err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de copy")
		return "", err
	}
	if err := f.Sync(); err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de sync")
		return "", err
	}
	if err := f.Close(); err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha de close")
		return "", err
	}

	actual, err := fileSHA256(path)
	if err != nil {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download apos falha sha256")
		return "", err
	}
	if expectedSHA256 != "" && actual != expectedSHA256 {
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download sha256 mismatch")
		return "", fmt.Errorf("sha256 mismatch: expected=%s got=%s", expectedSHA256, actual)
	}
	if policy.RequireSignatureValidation {
		if err := validateAuthenticodeSignature(ctx, path); err != nil {
			errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download assinatura invalida")
			return "", err
		}
	}

	// Publica no P2P após download HTTP bem-sucedido
	if u.OnArtifactReady != nil {
		_ = u.OnArtifactReady(ctx, path, artifactID, expectedSHA256, targetVersion)
	}

	return path, nil
}

func (u *Updater) reportEvent(ctx context.Context, eventType string, opts reportOpts) {
	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token == "" || agentID == "" {
		return
	}
	endpoint := u.apiScheme() + "://" + u.apiServer() + "/api/v1/agent-auth/me/update/report"

	payload := reportPayload{
		AgentID: agentID,
		Request: reportPayloadRequest{
			ReleaseID:      opts.ReleaseID,
			EventType:      strings.TrimSpace(eventType),
			CurrentVersion: strings.TrimSpace(opts.CurrentVersion),
			TargetVersion:  strings.TrimSpace(opts.TargetVersion),
			Message:        strings.TrimSpace(opts.Message),
			CorrelationID:  strings.TrimSpace(opts.CorrelationID),
			OccurredAtUTC:  time.Now().UTC().Format(time.RFC3339),
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		u.logf("reportEvent marshal falhou (%s): %v", eventType, err)
		return
	}

	ctxReq, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctxReq, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		u.logf("reportEvent request falhou (%s): %v", eventType, err)
		return
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		u.logf("reportEvent credenciais invalidas (%s): %v", eventType, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: reportTimeout}
	resp, err := client.Do(req)
	if err != nil {
		u.logf("reportEvent envio falhou (%s): %v", eventType, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payloadBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		u.logf("reportEvent status invalido (%s): %d body=%s", eventType, resp.StatusCode, strings.TrimSpace(string(payloadBody)))
	}
}
