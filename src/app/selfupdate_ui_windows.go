//go:build windows

package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	psadt "github.com/pedrostefanogv/go-psadt"
	pstypes "github.com/pedrostefanogv/go-psadt/types"

	"discovery/internal/selfupdate"
)

// selfUpdateInstallWithPSADT implementa o callback OnSelfUpdateInstall do selfupdate.Updater.
//
// Fluxo SEM interação do usuário:
//  1. Detecta sessão não-interativa → desvia para modo silencioso.
//  2. Abre sessão PSADT
//  3. Welcome INFORMATIVO (sem CloseProcesses) — o instalador NSIS já faz taskkill.
//     Não fechamos o agente aqui para evitar race condition: se o PSADT matar
//     o processo antes do LaunchInstallerElevated, o instalador nunca executa.
//  4. Exibe Progress: "Instalando Discovery Agent vX.Y.Z..."
//  5. Lança o instalador via LaunchInstallerElevated (UAC) — sem diálogo nativo
//  6. Se falhar, exibe balloon de erro visível ao usuário.
//  7. Retorna nil — o instalador NSIS faz taskkill do agente, matando esta sessão
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
	// Serviço, sessão 0, ou SESSIONNAME vazio/Services → sem UI PSADT.
	// Lança instalador diretamente com elevação UAC.
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

	// ── Welcome INFORMATIVO (sem fechar processos) ──
	// NÃO usamos CloseProcesses/BlockExecution aqui porque:
	//   - Se o PSADT fechar discovery-agent.exe ANTES do LaunchInstallerElevated,
	//     o processo Go morre e o instalador NSIS nunca é lançado (race condition).
	//   - O instalador NSIS JÁ faz taskkill como primeira ação (/S /UPDATE).
	//   - O Welcome serve apenas para informar o usuário sobre a atualização.
	// AllowDefer = false → usuário não pode adiar.
	// HideCloseButton = true → sem botão X.
	a.logs.append("[selfupdate] PSADT Welcome informativo (instalador NSIS fara o taskkill)")
	if err := session.ShowInstallationWelcome(pstypes.WelcomeOptions{
		Title:           "Atualização do Discovery Agent",
		Subtitle:        fmt.Sprintf("Versão %s", targetVersion),
		AllowDefer:      false,
		HideCloseButton: true,
		CheckDiskSpace:  true,
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
		// ── Feedback visual de erro antes de propagar ──
		a.logs.append("[selfupdate] LaunchInstallerElevated falhou: " + err.Error())
		balloonErr := session.ShowBalloonTip(pstypes.BalloonTipOptions{
			BalloonTipTitle: "Falha na atualização",
			BalloonTipText:  fmt.Sprintf("Não foi possível iniciar o instalador. Verifique as permissões de administrador.\n\nErro: %v", err),
			BalloonTipIcon:  pstypes.BalloonError,
			NoWait:          false,
		})
		if balloonErr != nil {
			a.logs.append("[selfupdate] aviso: falha ao exibir balloon de erro: " + balloonErr.Error())
		}
		// Aguarda um pouco para o usuário ver o balloon
		time.Sleep(2 * time.Second)
		return fmt.Errorf("LaunchInstallerElevated: %w", err)
	}

	// ── Aguarda o instalador iniciar ──
	// O instalador NSIS faz taskkill do discovery-agent.exe.
	// 1s é suficiente para o launch; o defer session.CloseWithContext pode
	// não executar se o taskkill for rápido (aceitável — sessão PSADT é stateless).
	a.logs.append("[selfupdate] instalador lancado — aguardando taskkill pelo NSIS...")
	time.Sleep(1 * time.Second)

	a.logs.append("[selfupdate] PSADT UI concluido — instalador NSIS assumiu")
	return nil
}

// hasInteractiveSession verifica se o processo atual está rodando em uma
// sessão interativa de usuário (com desktop). Retorna false para sessão 0
// (serviços), sessões não-interativas ou SESSIONNAME vazio/Services.
func hasInteractiveSession() bool {
	sessionName := strings.TrimSpace(os.Getenv("SESSIONNAME"))
	// Sessão 0 = serviços, non-interactive
	// SESSIONNAME vazio = pode ser serviço ou processo sem sessão
	if sessionName == "" || strings.EqualFold(sessionName, "Services") {
		return false
	}

	// Verifica se o console atual está associado a uma window station interativa.
	// Se GetConsoleWindow retornar NULL + SESSIONNAME definido mas não "Services",
	// pode ser uma sessão RDP sem desktop — tratamos como interativa (otimista).
	var kernel32 = syscall.NewLazyDLL("user32.dll")
	procGetConsoleWindow := kernel32.NewProc("GetConsoleWindow")
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		// Sem console window — pode ser GUI (Wails) ou serviço puro.
		// Se SESSIONNAME não é Services e não é vazio, assume interativa.
		return true
	}
	return true
}
