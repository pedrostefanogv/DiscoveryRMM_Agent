package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"discovery/app/core/buildinfo"
	"discovery/app/core/errutil"
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
		InstallerPath:   tempPath,
	}); err != nil {
		u.lastError.Store("persistencia falhou: " + err.Error())
		errutil.LogIfErr(os.Remove(tempPath), "selfupdate: limpar temp apos falha de persistencia")
		return err
	}

	u.pendingTargetVersion.Store(targetVersion)

	if err := u.launchInstallerWithUI(ctx, tempPath, targetVersion); err != nil {
		return u.handleLaunchFailure(ctx, tempPath, targetVersion, err)
	}

	u.logf("installer iniciado em background: %s", tempPath)
	return nil
}

// launchInstallerWithUI resolve o callback OnSelfUpdateInstall ou, como fallback,
// chama launchInstaller diretamente.
//
// O rename .old (truque para liberar path no Windows) é feito pelo NSIS
// (PrepareForInPlaceUpdate), que roda elevado via ShellExecuteEx(runas).
func (u *Updater) launchInstallerWithUI(ctx context.Context, exePath, targetVersion string) error {
	// Escreve marcador no installer.log antes de lançar o instalador, criando
	// a linha do tempo agente→instalador (Fase 3.2).
	u.writeInstallerLogMarker(fmt.Sprintf("agente iniciou update para versao %s (installer=%s)", targetVersion, exePath))

	if u.OnSelfUpdateInstall != nil {
		u.logf("[selfupdate] usando callback OnSelfUpdateInstall para %s", targetVersion)
		if err := u.OnSelfUpdateInstall(ctx, exePath, targetVersion); err != nil {
			u.logf("[selfupdate] callback falhou: %v — tentando launchInstaller direto", err)
			return u.launchInstaller(exePath)
		}
		return nil
	}
	u.logf("[selfupdate] executando instalador via ShellExecuteEx runas: %s", exePath)
	return u.launchInstaller(exePath)
}

// handleLaunchFailure trata uma falha ao lançar o instalador. Diferente do
// comportamento anterior (que apagava o pending state), aqui o estado pendente
// é PRESERVADO para que o contador InstallAttempts acumule e o retry aconteça
// no próximo ciclo do Run loop (ou no próximo startup via ResumePendingInstallReport).
//
// O arquivo .exe baixado também é preservado (não removido) — o cleanupOldDownloads
// protege o InstallerPath do pending state ativo, então ele não será limpo.
//
// Retorna o erro original para o caller reportar.
func (u *Updater) handleLaunchFailure(ctx context.Context, tempPath, targetVersion string, launchErr error) error {
	u.lastError.Store("launch falhou: " + launchErr.Error())
	u.incLaunchFail()

	// Carrega o estado pendente para incrementar o contador de tentativas.
	// Se não conseguir carregar (ex.: já foi limpo), persiste um novo estado
	// mínimo para que o retry tenha referência.
	if state, err := u.loadPendingInstallState(); err == nil {
		state.InstallAttempts++
		u.logf("[selfupdate] launch falhou (tentativa %d/%d para target=%s): %v",
			state.InstallAttempts, maxInstallAttempts, targetVersion, launchErr)
		if state.InstallAttempts >= maxInstallAttempts {
			u.logf("[selfupdate] maximo de %d tentativas de launch atingido para target=%s — abortando",
				maxInstallAttempts, targetVersion)
			u.clearPendingInstallState()
			u.reportInstallFailed(ctx, state, "launch-failed: "+strconv.Itoa(state.InstallAttempts)+" tentativas")
			return launchErr
		}
		// Mantém o estado pendente (com contador incrementado) para retry.
		if persistErr := u.persistPendingInstallState(state); persistErr != nil {
			u.logf("[selfupdate] aviso: nao foi possivel persistir contador de tentativas: %v", persistErr)
		}
		return launchErr
	}

	// Sem estado pendente prévio: persiste um novo para permitir retry.
	u.logf("[selfupdate] launch falhou sem estado pendente previo: %v", launchErr)
	_ = u.persistPendingInstallState(pendingInstallState{
		CurrentVersion:  strings.TrimSpace(buildinfo.Version),
		TargetVersion:   targetVersion,
		CorrelationID:   uuid.NewString(),
		RecordedAtUTC:   time.Now().UTC().Format(time.RFC3339),
		InstalledCommit: strings.TrimSpace(buildinfo.Commit),
		InstallerPath:   tempPath,
		InstallAttempts: 1,
	})
	return launchErr
}
