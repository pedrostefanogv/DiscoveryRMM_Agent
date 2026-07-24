package selfupdate

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"discovery/internal/buildinfo"
	"discovery/internal/errutil"
)

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
	_ = token
	_ = agentID // endpoint publico nao exige auth, mas token/agentID usados em downloadFromURL

	currentVersion := strings.TrimSpace(buildinfo.Version)
	if currentVersion == "" {
		currentVersion = "0.0.0"
	}
	targetVersion := strings.TrimSpace(version)
	if targetVersion == "" {
		targetVersion = "unknown"
	}
	correlationID := uuid.NewString()

	u.logf("[selfupdate] install-direto: baixando de %s (target=%s correlationId=%s)", downloadURL, targetVersion, correlationID)

	// Se a URL for relativa (comeca com /), monta com o apiServer
	finalURL := downloadURL
	if strings.HasPrefix(finalURL, "/") {
		finalURL = u.apiScheme() + "://" + u.apiServer() + finalURL
	}

	// Para URLs nao-relativas que sao do endpoint publico, usa P2P-first
	if strings.Contains(finalURL, "/api/v1/download/agent") {
		tempPath, fileSha256, _, err := u.downloadFromCacheOrPublic(ctx, "")
		if err != nil {
			return err
		}
		_ = fileSha256
		return u.finishInstall(ctx, tempPath, targetVersion, currentVersion, correlationID)
	}

	// URLs externas: download direto
	tempPath, fileSha256, err := u.downloadFromURL(ctx, finalURL)
	if err != nil {
		return err
	}

	// Publica no P2P com artifactID baseado no SHA256 real do binario.
	artifactID := "selfupdate:" + strings.ToLower(fileSha256)
	if u.OnArtifactReady != nil {
		_ = u.OnArtifactReady(ctx, tempPath, artifactID, fileSha256, targetVersion)
	}

	return u.finishInstall(ctx, tempPath, targetVersion, currentVersion, correlationID)
}

// finishInstall persiste o estado pendente e lanca o instalador.
func (u *Updater) finishInstall(ctx context.Context, tempPath, targetVersion, currentVersion, correlationID string) error {
	currentCommit := strings.TrimSpace(buildinfo.Commit)
	if err := u.persistPendingInstallState(pendingInstallState{
		CurrentVersion:  currentVersion,
		TargetVersion:   targetVersion,
		CorrelationID:   correlationID,
		RecordedAtUTC:   time.Now().UTC().Format(time.RFC3339),
		InstalledCommit: currentCommit,
	}); err != nil {
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de persistencia")
		return err
	}

	if err := u.launchInstallerWithUI(ctx, tempPath, targetVersion); err != nil {
		u.clearPendingInstallState()
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de launch")
		return err
	}

	u.logf("installer iniciado em background: %s", tempPath)
	return nil
}

// launchInstallerWithUI resolve o callback OnSelfUpdateInstall ou, como fallback,
// chama RenameAgentSelfAndLaunch (rename .old + launch) diretamente.
//
// IMPORTANTE: Quando OnSelfUpdateInstall é chamado com sucesso, o callback JÁ
// lançou o instalador via LaunchInstallerElevated. NÃO chamamos launchInstaller
// novamente — seria dupla execução.
func (u *Updater) launchInstallerWithUI(ctx context.Context, exePath, targetVersion string) error {
	if u.OnSelfUpdateInstall != nil {
		u.logf("[selfupdate] usando callback OnSelfUpdateInstall com PSADT UI para %s", targetVersion)
		if err := u.OnSelfUpdateInstall(ctx, exePath, targetVersion); err != nil {
			u.logf("[selfupdate] callback PSADT falhou: %v — tentando launchInstaller direto sem UI", err)
			// Fallback: callback falhou, tenta RenameAgentSelfAndLaunch
			return u.RenameAgentSelfAndLaunch(exePath)
		}
		// Callback PSADT já lançou o instalador — não faz nada adicional.
		return nil
	}
	// Sem callback PSADT: renomeia o executável atual (.old) e lança o instalador.
	// O rename previne "Access Denied" no NSIS ao copiar o novo .exe.
	u.logf("[selfupdate] renomeando self .old e executando instalador via ShellExecuteEx runas: %s", exePath)
	return u.RenameAgentSelfAndLaunch(exePath)
}
