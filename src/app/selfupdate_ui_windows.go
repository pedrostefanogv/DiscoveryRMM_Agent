//go:build windows

package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	psadt "github.com/pedrostefanogv/go-psadt"
	pstypes "github.com/pedrostefanogv/go-psadt/types"

	"discovery/internal/selfupdate"
)

// psadtClientResult transports the result of psadt.NewClient across a channel.
type psadtClientResult struct {
	client *psadt.Client
	err    error
}

// selfUpdateInstallWithPSADT implementa o callback OnSelfUpdateInstall do selfupdate.Updater.
//
// Fluxo SEM interação do usuário:
//  1. Detecta sessão não-interativa → desvia para modo silencioso.
//  2. Cria ctx com timeout de 3min para todo o fluxo PSADT.
//  3. NewClient em goroutine dedicada com timeout — a lib usa context.Background()
//     hardcoded no ImportModule, então envolvemos em goroutine+select para garantir
//     que travamentos no PowerShell (spawn, scanner.Scan) não bloqueiem para sempre.
//  4. Abre sessão PSADT com o ctx de timeout.
//  5. Welcome/Progress/BalloonTip usam session.WithContext(psadtCtx) para timeout.
//  6. Lança o instalador via LaunchInstallerElevated (UAC) — sem diálogo nativo.
//  7. Retorna nil — o instalador NSIS faz taskkill do agente, matando esta sessão.
//
// O usuário NÃO interage — apenas vê a barra de progresso.
func (a *App) selfUpdateInstallWithPSADT(ctx context.Context, exePath, targetVersion string) (err error) {
	// ── Panic guard: se PSADT ou qualquer chamada interna panicar,
	// transforma em erro para que launchInstallerWithUI possa fazer
	// fallback para launchInstaller direto (ShellExecuteEx runas).
	defer func() {
		if r := recover(); r != nil {
			a.logs.append(fmt.Sprintf("[selfupdate] PANIC na UI PSADT: %v — propagando como erro para fallback", r))
			err = fmt.Errorf("psadt ui panic: %v", r)
		}
	}()

	a.logs.append("[selfupdate] iniciando PSADT UI (auto-close): exePath=" + exePath + " targetVersion=" + targetVersion)

	// ── Detecção de sessão não-interativa ──
	if !hasInteractiveSession() {
		a.logs.append("[selfupdate] sessão não-interativa detectada — modo silencioso (sem UI PSADT)")
		if _, statErr := os.Stat(exePath); statErr != nil {
			return fmt.Errorf("instalador nao encontrado: %w", statErr)
		}
		if err := selfupdate.LaunchInstallerElevated(exePath, "/S /UPDATE"); err != nil {
			return fmt.Errorf("LaunchInstallerElevated (silent): %w", err)
		}
		a.logs.append("[selfupdate] instalador lançado em modo silencioso — aguardando taskkill pelo NSIS")
		return nil
	}

	// ── Context com timeout dedicado para todo o fluxo PSADT ──
	// O ctx original do Wails não tem deadline. Se o PowerShell travar no spawn
	// ou no Import-Module, a goroutine bloqueia indefinidamente. Com timeout de
	// 3min, o callback retorna erro e o launchInstallerWithUI faz fallback.
	psadtTimeout := 3 * time.Minute
	psadtCtx, psadtCancel := context.WithTimeout(ctx, psadtTimeout)
	defer psadtCancel()

	// ── NewClient em goroutine dedicada com timeout ──
	// psadt.NewClient() usa context.Background() hardcoded no ImportModule.
	// Não podemos passar nosso psadtCtx para ele. A solução é rodar NewClient
	// em uma goroutine separada e usar select com o psadtCtx.Done() como timeout.
	// Se o PowerShell travar no spawn/scanner, o select dispara após 3min e
	// retornamos erro. A goroutine interna fica leaked, mas é aceitável porque
	// o instalador NSIS vai matar o processo do agente em seguida.
	a.logs.append(fmt.Sprintf("[selfupdate] PSADT: chamando NewClient em goroutine com timeout=%.0fs", psadtTimeout.Seconds()))
	clientCh := make(chan psadtClientResult, 1)
	go func() {
		c, err := psadt.NewClient(
			psadt.WithTimeout(psadtTimeout),
		)
		clientCh <- psadtClientResult{c, err}
	}()

	var client *psadt.Client
	select {
	case res := <-clientCh:
		if res.err != nil {
			a.logs.append("[selfupdate] PSADT: NewClient falhou: " + res.err.Error())
			return fmt.Errorf("psadt.NewClient: %w", res.err)
		}
		client = res.client
	case <-psadtCtx.Done():
		a.logs.append("[selfupdate] PSADT: NewClient timeout apos " + psadtTimeout.String() + " — PowerShell travou no spawn/Import-Module")
		return fmt.Errorf("psadt.NewClient: timeout apos %v (PowerShell travado)", psadtTimeout)
	}
	defer client.Close()
	a.logs.append("[selfupdate] PSADT: NewClient OK — chamando OpenSession")

	session, err := client.OpenSessionWithContext(psadtCtx, pstypes.SessionConfig{
		AppVendor:      "Discovery",
		AppName:        "Discovery Agent",
		AppVersion:     targetVersion,
		DeploymentType: pstypes.DeployInstall,
		DeployMode:     pstypes.DeployModeInteractive,
	})
	if err != nil {
		a.logs.append("[selfupdate] PSADT: OpenSession falhou: " + err.Error())
		return fmt.Errorf("psadt.OpenSession: %w", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()
	a.logs.append("[selfupdate] PSADT: OpenSession OK — chamando ShowInstallationWelcome")

	// ── Welcome INFORMATIVO (sem fechar processos) ──
	// Usamos session.WithContext(psadtCtx) para que o Welcome respeite o timeout
	// e não bloqueie indefinidamente se o PSADT travar na UI.
	timedSession := session.WithContext(psadtCtx)
	if err := timedSession.ShowInstallationWelcome(pstypes.WelcomeOptions{
		Title:           "Atualização do Discovery Agent",
		Subtitle:        fmt.Sprintf("Versão %s", targetVersion),
		AllowDefer:      false,
		HideCloseButton: true,
		CheckDiskSpace:  true,
	}); err != nil {
		a.logs.append("[selfupdate] PSADT: Welcome dialog falhou (pode estar sem sessao interativa): " + err.Error())
	} else {
		a.logs.append("[selfupdate] PSADT: Welcome OK — chamando ShowInstallationProgress")
	}

	// ── Progress: mostra que está instalando ──
	if err := timedSession.ShowInstallationProgress(pstypes.ProgressOptions{
		StatusMessage: fmt.Sprintf("Instalando Discovery Agent %s...", targetVersion),
	}); err != nil {
		a.logs.append("[selfupdate] PSADT: Progress falhou: " + err.Error())
	} else {
		a.logs.append("[selfupdate] PSADT: Progress OK — chamando LaunchInstallerElevated")
	}
	defer func() {
		if cerr := session.CloseInstallationProgress(); cerr != nil {
			a.logs.append("[selfupdate] aviso: falha ao fechar progresso: " + cerr.Error())
		}
	}()

	// ── Lança o instalador com elevação UAC ──
	if _, statErr := os.Stat(exePath); statErr != nil {
		return fmt.Errorf("instalador nao encontrado: %w", statErr)
	}

	a.logs.append("[selfupdate] PSADT: lancando instalador via LaunchInstallerElevated: " + exePath)
	if err := selfupdate.LaunchInstallerElevated(exePath, "/S /UPDATE"); err != nil {
		a.logs.append("[selfupdate] LaunchInstallerElevated falhou: " + err.Error())
		balloonErr := timedSession.ShowBalloonTip(pstypes.BalloonTipOptions{
			BalloonTipTitle: "Falha na atualização",
			BalloonTipText:  fmt.Sprintf("Não foi possível iniciar o instalador. Verifique as permissões de administrador.\n\nErro: %v", err),
			BalloonTipIcon:  pstypes.BalloonError,
			NoWait:          false,
		})
		if balloonErr != nil {
			a.logs.append("[selfupdate] aviso: falha ao exibir balloon de erro: " + balloonErr.Error())
		}
		time.Sleep(2 * time.Second)
		return fmt.Errorf("LaunchInstallerElevated: %w", err)
	}

	// ── Aguarda o instalador iniciar ──
	a.logs.append("[selfupdate] PSADT: instalador lancado — aguardando taskkill pelo NSIS...")
	time.Sleep(2 * time.Second)

	a.logs.append("[selfupdate] PSADT: UI concluido — instalador NSIS assumiu")
	return nil
}

// hasInteractiveSession verifica se o processo atual está rodando em uma
// sessão interativa de usuário (com desktop). Retorna false para sessão 0
// (serviços), sessões não-interativas ou SESSIONNAME vazio/Services.
//
// NOTA: NÃO usamos GetConsoleWindow / GetProcessWindowStation para detectar
// interatividade porque:
//   - GetConsoleWindow está em kernel32.dll, não user32.dll (confusão comum).
//   - A Wails UI não tem console window (hwnd=0) mas tem sessão interativa.
//   - O SESSIONNAME já é suficiente: vazio/"Services" = não-interativo,
//     qualquer outro valor = sessão de usuário com desktop.
func hasInteractiveSession() bool {
	sessionName := strings.TrimSpace(os.Getenv("SESSIONNAME"))
	if sessionName == "" || strings.EqualFold(sessionName, "Services") {
		return false
	}
	return true
}
