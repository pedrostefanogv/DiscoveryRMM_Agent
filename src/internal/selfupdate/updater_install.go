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

// forceInstallFromPublicEndpoint baixa o instalador do endpoint público
// /api/v1/download/agent e executa /S /UPDATE sem nenhuma validação de
// versão, manifest ou sha256. Usado exclusivamente em modo force=true
// (comando de update vindo do servidor).
func (u *Updater) forceInstallFromPublicEndpoint(ctx context.Context, currentVersion, correlationID string) error {
	downloadURL := u.apiScheme() + "://" + u.apiServer() + "/api/v1/download/agent"
	u.logf("[selfupdate] force-install: baixando de %s", downloadURL)

	u.reportEvent(ctx, "UpdateAvailable", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  currentVersion, // Será atualizado após extrair versão real do arquivo
		CorrelationID:  correlationID,
		Message:        "force reinstall via public endpoint",
	})

	u.reportEvent(ctx, "DownloadStarted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  currentVersion, // Será atualizado após extrair versão real do arquivo
		CorrelationID:  correlationID,
	})

	tempPath, fileSha256, err := u.downloadFromURL(ctx, downloadURL)
	if err != nil {
		u.logf("[selfupdate] force-install download falhou: %v", err)
		u.reportEvent(ctx, "DownloadFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  currentVersion,
			CorrelationID:  correlationID,
			Message:        err.Error(),
		})
		return err
	}

	u.logf("[selfupdate] force-install download concluido: tempPath=%s sha256=%s", tempPath, fileSha256)

	// Extrair a versão real do arquivo baixado
	targetVersion := extractFileVersion(tempPath)
	if targetVersion == "" {
		u.logf("[selfupdate] force-install aviso: nao conseguiu extrair versao do arquivo, usando currentVersion=%s", currentVersion)
		targetVersion = currentVersion
	}
	u.logf("[selfupdate] force-install versao alvo determinada: %s", targetVersion)

	u.reportEvent(ctx, "DownloadCompleted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
		Message:        fmt.Sprintf("sha256=%s", fileSha256),
	})

	u.reportEvent(ctx, "InstallStarted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
	})

	if err := u.persistPendingInstallState(pendingInstallState{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
		RecordedAtUTC:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de persistencia")
		u.reportEvent(ctx, "InstallFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        "falha ao persistir estado pendente: " + err.Error(),
		})
		return err
	}

	if err := u.launchInstallerWithUI(ctx, tempPath, targetVersion); err != nil {
		u.clearPendingInstallState()
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de launch")
		u.reportEvent(ctx, "InstallFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        err.Error(),
		})
		return err
	}

	u.logf("[selfupdate] force-install: instalador iniciado em background: %s", tempPath)
	return nil
}

// InstallFromURL faz o download do instalador a partir de uma URL direta
// fornecida pelo servidor e o executa em background (/S /UPDATE).
// Usado quando o comando de update vem com action=install e url preenchida.
func (u *Updater) InstallFromURL(ctx context.Context, version, downloadURL string) error {
	downloadURL = strings.TrimSpace(downloadURL)
	if downloadURL == "" {
		return errors.New("url de download vazia")
	}

	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if token == "" {
		return errors.New("token vazio")
	}
	if agentID == "" {
		return errors.New("agentId vazio")
	}

	currentVersion := strings.TrimSpace(buildinfo.Version)
	if currentVersion == "" {
		currentVersion = "0.0.0"
	}
	targetVersion := strings.TrimSpace(version)
	if targetVersion == "" {
		targetVersion = "unknown"
	}
	correlationID := uuid.NewString()

	u.reportEvent(ctx, "UpdateAvailable", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
		Message:        "install direto via comando do servidor",
	})

	u.reportEvent(ctx, "DownloadStarted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
	})

	tempPath, fileSha256, err := u.downloadFromURL(ctx, downloadURL)
	if err != nil {
		u.reportEvent(ctx, "DownloadFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        err.Error(),
		})
		return err
	}

	u.reportEvent(ctx, "DownloadCompleted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
		Message:        fmt.Sprintf("sha256=%s", fileSha256),
	})

	u.reportEvent(ctx, "InstallStarted", reportOpts{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
	})

	if err := u.persistPendingInstallState(pendingInstallState{
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
		CorrelationID:  correlationID,
		RecordedAtUTC:  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de persistencia")
		u.reportEvent(ctx, "InstallFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        "falha ao persistir estado pendente: " + err.Error(),
		})
		return err
	}

	if err := u.launchInstallerWithUI(ctx, tempPath, targetVersion); err != nil {
		u.clearPendingInstallState()
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de launch")
		u.reportEvent(ctx, "InstallFailed", reportOpts{
			CurrentVersion: currentVersion,
			TargetVersion:  targetVersion,
			CorrelationID:  correlationID,
			Message:        err.Error(),
		})
		return err
	}

	u.logf("installer direto iniciado em background: version=%s url=%s", targetVersion, downloadURL)
	return nil
}

// downloadFromURL faz o download do instalador a partir de uma URL pública.
// Retorna o caminho do arquivo temporário e o SHA256 do conteúdo.
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

	// Usa o mesmo header de auth que os outros endpoints da API
	token := strings.TrimSpace(u.getToken())
	agentID := strings.TrimSpace(u.getAgentID())
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		errutil.LogIfErr(f.Close(), "selfupdate: fechar arquivo apos erro de header")
		errutil.LogIfErr(os.Remove(path), "selfupdate: limpar download credenciais invalidas")
		return "", "", err
	}

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

	return path, sha, nil
}

// launchInstallerWithUI resolve o callback OnSelfUpdateInstall ou, como fallback,
// chama launchInstaller diretamente (comportamento legado sem UI PSADT).
func (u *Updater) launchInstallerWithUI(ctx context.Context, exePath, targetVersion string) error {
	if u.OnSelfUpdateInstall != nil {
		u.logf("[selfupdate] usando callback OnSelfUpdateInstall com PSADT UI para %s", targetVersion)
		if err := u.OnSelfUpdateInstall(ctx, exePath, targetVersion); err == nil {
			return nil
		} else {
			u.logf("[selfupdate] callback PSADT falhou, tentando fallback launchInstaller: %v", err)
			if fallbackErr := u.launchInstaller(exePath); fallbackErr == nil {
				return nil
			} else {
				return fmt.Errorf("selfupdate callback falhou: %v; fallback falhou: %w", err, fallbackErr)
			}
		}
	}
	u.logf("[selfupdate] sem callback UI, usando launchInstaller direto")
	return u.launchInstaller(exePath)
}
