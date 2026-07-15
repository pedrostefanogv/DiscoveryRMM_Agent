//go:build windows

package app

import (
	"context"
	"fmt"
	"os"
	"time"

	psadt "github.com/pedrostefanogv/go-psadt"
	pstypes "github.com/pedrostefanogv/go-psadt/types"
)

// selfUpdateInstallWithPSADT implementa o callback OnSelfUpdateInstall do selfupdate.Updater.
// Fluxo:
//  1. Abre client PSADT + sessão NonInteractive
//  2. Exibe Welcome SEM AllowDefer (força continuar), HideCloseButton=true
//  3. Exibe Progress durante a execução
//  4. Executa o instalador /S /UPDATE via StartProcess
//  5. Fecha progress e sessão
//
// O user NÃO pode adiar nem cancelar — só vê o progresso.
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
	// HideCloseButton evita fechar pelo X
	// Não tem AllowDefer nem DeferTimes — se fechar de alguma forma, o
	// ShowInstallationWelcome retorna erro e o fluxo falha.
	if err := session.ShowInstallationWelcome(pstypes.WelcomeOptions{
		Title:           "Atualização do Discovery Agent",
		Subtitle:        fmt.Sprintf("Versão %s", targetVersion),
		HideCloseButton: true,
		Silent:          false,
		CheckDiskSpace:  true,
	}); err != nil {
		return fmt.Errorf("welcome dialog: %w", err)
	}

	// Exibe progresso
	if err := session.ShowInstallationProgress(pstypes.ProgressOptions{
		StatusMessage: fmt.Sprintf("Instalando Discovery Agent %s...", targetVersion),
	}); err != nil {
		a.logs.append("[selfupdate] aviso: falha ao exibir progresso: " + err.Error())
		// Non-fatal — continua mesmo sem progresso
	}
	defer func() {
		if cerr := session.CloseInstallationProgress(); cerr != nil {
			a.logs.append("[selfupdate] aviso: falha ao fechar progresso: " + cerr.Error())
		}
	}()

	// Executa o instalador
	result, err := session.StartProcess(pstypes.StartProcessOptions{
		FilePath:     exePath,
		ArgumentList: []string{"/S", "/UPDATE"},
		WindowStyle:  pstypes.WindowHidden,
		PassThru:     true,
	})
	if err != nil {
		return fmt.Errorf("instalador falhou: %w", err)
	}

	// Remove o temp file após execução
	_ = os.Remove(exePath)

	if result.ExitCode != 0 {
		return fmt.Errorf("instalador retornou exit code %d", result.ExitCode)
	}

	a.logs.append("[selfupdate] PSADT UI concluído com sucesso: versão=" + targetVersion)
	return nil
}
