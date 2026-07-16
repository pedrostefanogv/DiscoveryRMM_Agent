//go:build windows

package app

import (
	"context"
	"fmt"
	"os"
	"time"

	psadt "github.com/pedrostefanogv/go-psadt"
	pstypes "github.com/pedrostefanogv/go-psadt/types"

	"discovery/internal/selfupdate"
)

// selfUpdateInstallWithPSADT implementa o callback OnSelfUpdateInstall do selfupdate.Updater.
//
// Fluxo SEM interação do usuário:
//  1. Abre sessão PSADT
//  2. Welcome com CloseProcesses=["discovery-agent"] — PSADT fecha o agente atual
//     automaticamente (CloseProcessesCountdown + ForceCloseProcessesCountdown)
//  3. Exibe Progress: "Instalando Discovery Agent vX.Y.Z..."
//  4. Lança o instalador via LaunchInstallerElevated (UAC) — sem diálogo nativo
//  5. Aguarda alguns segundos para o instalador iniciar
//  6. Retorna nil — o instalador NSIS faz taskkill do agente, matando esta sessão
//
// O usuário NÃO interage — apenas vê a barra de progresso.
func (a *App) selfUpdateInstallWithPSADT(ctx context.Context, exePath, targetVersion string) error {
	a.logs.append("[selfupdate] iniciando PSADT UI (auto-close): exePath=" + exePath + " targetVersion=" + targetVersion)

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

	// ── Welcome SEM interação: auto-close do agente atual ──
	// CloseProcesses = [discovery-agent.exe] → PSADT detecta e fecha automaticamente.
	// AllowDefer = false → usuário não pode adiar.
	// HideCloseButton = true → sem botão X.
	// BlockExecution = true → trava até processos estarem fechados.
	// CloseProcessesCountdown = 10s → fecha após 10s de inatividade.
	// ForceCloseProcessesCountdown = 5s → força fechamento 5s após o countdown.
	a.logs.append("[selfupdate] PSADT Welcome com CloseProcesses=[discovery-agent] (auto-close, sem interacao)")
	if err := session.ShowInstallationWelcome(pstypes.WelcomeOptions{
		Title:                        "Atualização do Discovery Agent",
		Subtitle:                     fmt.Sprintf("Versão %s", targetVersion),
		CloseProcesses:               []pstypes.ProcessDefinition{{Name: "discovery-agent"}},
		AllowDefer:                   false,
		HideCloseButton:              true,
		BlockExecution:               true,
		CloseProcessesCountdown:      10,
		ForceCloseProcessesCountdown: 5,
		CheckDiskSpace:               true,
	}); err != nil {
		a.logs.append("[selfupdate] aviso: Welcome dialog falhou (pode estar sem sessao interativa): " + err.Error())
		// Fallback: tenta prosseguir sem Welcome
	}

	// ── Progress: mostra que está instalando ──
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

	// ── Lança o instalador com elevação UAC ──
	// ShellExecuteEx("runas") é necessário porque o instalador NSIS /S /UPDATE
	// sempre requer admin (taskkill, Program Files, HKLM).
	if _, statErr := os.Stat(exePath); statErr != nil {
		return fmt.Errorf("instalador nao encontrado: %w", statErr)
	}

	a.logs.append("[selfupdate] lancando instalador via LaunchInstallerElevated: " + exePath)
	if err := selfupdate.LaunchInstallerElevated(exePath, "/S /UPDATE"); err != nil {
		return fmt.Errorf("LaunchInstallerElevated: %w", err)
	}

	// ── Aguarda alguns segundos para o instalador iniciar ──
	// O instalador NSIS faz taskkill do discovery-agent.exe.
	// Enquanto isso, o PSADT fica visível mostrando o progresso.
	a.logs.append("[selfupdate] instalador lancado — aguardando taskkill pelo NSIS...")
	time.Sleep(3 * time.Second)

	a.logs.append("[selfupdate] PSADT UI concluido — instalador NSIS assumiu")
	return nil
}
