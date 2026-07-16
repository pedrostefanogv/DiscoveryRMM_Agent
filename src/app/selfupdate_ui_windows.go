//go:build windows

package app

import (
	"context"
	"fmt"
	"time"

	psadt "github.com/pedrostefanogv/go-psadt"
	pstypes "github.com/pedrostefanogv/go-psadt/types"
)

// selfUpdateInstallWithPSADT implementa o callback OnSelfUpdateInstall do selfupdate.Updater.
// Fluxo:
//  1. Abre client PSADT + sessão Interactive
//  2. Exibe Welcome SEM AllowDefer (força continuar), HideCloseButton=true
//  3. Exibe Progress durante a execução
//  4. NÃO executa o instalador — apenas mostra UI (Welcome+Progress) e retorna nil
//  5. O launchInstallerWithUI chama launchInstaller (ShellExecuteEx runas) em seguida
//
// Motivo: o PSADT StartProcess NÃO consegue elevar privilégios, e o instalador
// NSIS /S /UPDATE sempre requer admin (taskkill, Program Files, HKLM).
func (a *App) selfUpdateInstallWithPSADT(ctx context.Context, exePath, targetVersion string) error {
	a.logs.append("[selfupdate] iniciando PSADT UI: exePath=" + exePath + " targetVersion=" + targetVersion)

	client, err := psadt.NewClient(
		psadt.WithTimeout(10 * time.Minute),
	)
	if err != nil {
		return fmt.Errorf("psadt.NewClient: %w", err)
	}
	defer client.Close()

	session, err := client.OpenSessionWithContext(ctx, pstypes.SessionConfig{
		AppVendor:      "Discovery",
		AppName:        "Discovery Agent",
		AppVersion:     targetVersion,
		DeploymentType: pstypes.DeployInstall,
		DeployMode:     pstypes.DeployModeInteractive,
	})
	if err != nil {
		return fmt.Errorf("psadt.OpenSession: %w", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	// Welcome SEM AllowDefer — user não pode adiar
	if err := session.ShowInstallationWelcome(pstypes.WelcomeOptions{
		Title:           "Atualização do Discovery Agent",
		Subtitle:        fmt.Sprintf("Versão %s", targetVersion),
		HideCloseButton: true,
		Silent:          false,
		CheckDiskSpace:  true,
	}); err != nil {
		return fmt.Errorf("welcome dialog: %w", err)
	}

	// Exibe progresso (não-fatal)
	if err := session.ShowInstallationProgress(pstypes.ProgressOptions{
		StatusMessage: fmt.Sprintf("Instalando Discovery Agent %s...", targetVersion),
	}); err != nil {
		a.logs.append("[selfupdate] aviso: falha ao exibir progresso: " + err.Error())
	}
	defer func() {
		if cerr := session.CloseInstallationProgress(); cerr != nil {
			a.logs.append("[selfupdate] aviso: falha ao fechar progresso: " + cerr.Error())
		}
	}()

	// NÃO executa o instalador via PSADT — o launchInstallerWithUI chamará
	// launchInstaller (ShellExecuteEx runas) que tem elevação UAC.
	// Apenas mostra UI e retorna nil.
	a.logs.append("[selfupdate] PSADT UI exibido — delegando execucao para ShellExecuteEx runas")
	return nil
}
