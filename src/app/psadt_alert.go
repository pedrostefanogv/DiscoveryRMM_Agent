//go:build windows

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	psadt "github.com/pedrostefanogv/go-psadt"
	pstypes "github.com/pedrostefanogv/go-psadt/types"

	"discovery/internal/processutil"
)

// isPsadtAlertCommandType verifica se o commandType corresponde a ShowPsadtAlert (9).
// Aceita o valor numérico "9" ou aliases string para compatibilidade.
func isPsadtAlertCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "9", "showpsadtalert", "show_psadt_alert", "psadt_alert", "psadtalert":
		return true
	default:
		return false
	}
}

// parsePsadtAlertPayload faz o unmarshal do payload recebido no ExecuteCommand.
func parsePsadtAlertPayload(payload any) (PsadtAlertPayload, error) {
	if payload == nil {
		return PsadtAlertPayload{}, fmt.Errorf("payload ausente")
	}
	var raw []byte
	switch typed := payload.(type) {
	case string:
		raw = []byte(typed)
	default:
		var err error
		raw, err = json.Marshal(typed)
		if err != nil {
			return PsadtAlertPayload{}, fmt.Errorf("falha ao serializar payload psadt-alert: %w", err)
		}
	}
	var p PsadtAlertPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return PsadtAlertPayload{}, fmt.Errorf("payload psadt-alert invalido: %w", err)
	}
	p.Type = strings.ToLower(strings.TrimSpace(p.Type))
	p.Icon = normalizePsadtAlertIcon(p.Icon)
	if p.Type == "" {
		p.Type = "toast"
	}
	if p.TimeoutSeconds <= 0 {
		if p.Type == "toast" {
			p.TimeoutSeconds = 15
		} else {
			p.TimeoutSeconds = 120
		}
	}
	return p, nil
}

// normalizePsadtAlertIcon converte aliases para o formato esperado pelo PSADT.
func normalizePsadtAlertIcon(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "warning", "warn":
		return "Warning"
	case "error", "stop":
		return "Error"
	case "success", "info", "information":
		return "Information"
	case "question":
		return "Question"
	default:
		return "Information"
	}
}

// handlePsadtAlert executa o alerta PSADT usando a lib go-psadt nativa,
// eliminando concatenação manual de scripts PowerShell.
func (a *App) handlePsadtAlert(ctx context.Context, p PsadtAlertPayload) (int, string, string) {
	if runtime.GOOS != "windows" {
		body, _ := json.Marshal(map[string]string{"action": "skipped_non_windows"})
		if a != nil {
			a.logs.append("[agent] psadt-alert ignorado: não é windows type=" + p.Type + " alertId=" + p.AlertID)
		}
		return 0, string(body), ""
	}

	psadtCfg := a.GetAgentConfiguration().PSADT
	if psadtCfg.Enabled == nil || !*psadtCfg.Enabled {
		body, _ := json.Marshal(map[string]string{"action": "skipped_disabled"})
		if a != nil {
			a.logs.append("[agent] psadt-alert ignorado: psadt.enabled=false type=" + p.Type + " alertId=" + p.AlertID)
		}
		return 0, string(body), ""
	}

	if a != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-alert iniciando type=%s alertId=%s timeout=%ds via go-psadt", p.Type, p.AlertID, p.TimeoutSeconds))
	}

	timeout := time.Duration(p.TimeoutSeconds+15) * time.Second
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := psadt.NewClient(
		psadt.WithTimeout(timeout),
		psadt.WithMinModuleVersion(strings.TrimSpace(psadtCfg.RequiredVersion)),
	)
	if err != nil {
		errMsg := fmt.Sprintf("psadt.NewClient: %v", err)
		if a != nil {
			a.logs.append("[agent] psadt-alert [ERRO] " + errMsg)
		}
		return 1, "", errMsg
	}
	defer client.Close()

	session, err := client.OpenSessionWithContext(execCtx, pstypes.SessionConfig{
		AppVendor:      "Discovery",
		AppName:        "Discovery Agent",
		AppVersion:     "1.0",
		DeploymentType: pstypes.DeployInstall,
		DeployMode:     pstypes.DeployModeInteractive,
	})
	if err != nil {
		errMsg := fmt.Sprintf("psadt.OpenSession: %v", err)
		if a != nil {
			a.logs.append("[agent] psadt-alert [ERRO] " + errMsg)
		}
		return 1, "", errMsg
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	switch p.Type {
	case "toast":
		action, errMsg := a.showPSADTToast(session, p)
		if errMsg != "" {
			return 1, "", errMsg
		}
		body, _ := json.Marshal(map[string]string{"action": action})
		return 0, string(body), ""

	case "update-progress":
		action, errMsg := a.showPSADTProgress(session, p)
		if errMsg != "" {
			return 1, "", errMsg
		}
		body, _ := json.Marshal(map[string]string{"action": action})
		return 0, string(body), ""

	case "modal":
		action, errMsg := a.showPSADTModal(execCtx, session, p)
		if errMsg != "" {
			return 1, "", errMsg
		}
		body, _ := json.Marshal(map[string]string{"action": action})
		return 0, string(body), ""

	default:
		// Fallback: toast.
		action, errMsg := a.showPSADTToast(session, p)
		if errMsg != "" {
			return 1, "", errMsg
		}
		body, _ := json.Marshal(map[string]string{"action": action})
		return 0, string(body), ""
	}
}

// showPSADTToast exibe um BalloonTip não-bloqueante.
func (a *App) showPSADTToast(session *psadt.Session, p PsadtAlertPayload) (string, string) {
	icon := pstypes.BalloonInfo
	switch strings.ToLower(p.Icon) {
	case "warning":
		icon = pstypes.BalloonWarning
	case "error":
		icon = pstypes.BalloonError
	}

	err := session.ShowBalloonTip(pstypes.BalloonTipOptions{
		BalloonTipTitle: strings.TrimSpace(p.Title),
		BalloonTipText:  strings.TrimSpace(p.Message),
		BalloonTipIcon:  icon,
		NoWait:          true,
	})
	if err != nil {
		errMsg := fmt.Sprintf("ShowBalloonTip: %v", err)
		if a != nil {
			a.logs.append("[agent] psadt-alert [ERRO] type=toast alertId=" + p.AlertID + " " + errMsg)
		}
		return "", errMsg
	}

	if a != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-alert [OK] type=toast alertId=%s action=shown", p.AlertID))
	}
	return "shown", ""
}

// showPSADTModal exibe um DialogBox bloqueante com botões de ação.
func (a *App) showPSADTModal(_ context.Context, session *psadt.Session, p PsadtAlertPayload) (string, string) {
	buttons := pstypes.ButtonsOk
	switch {
	case len(p.Actions) >= 3:
		buttons = pstypes.ButtonsYesNoCancel
	case len(p.Actions) >= 2:
		buttons = pstypes.ButtonsYesNo
	}

	defaultButton := pstypes.DialogDefaultFirst
	if strings.TrimSpace(p.DefaultAction) != "" && len(p.Actions) >= 2 {
		if strings.EqualFold(strings.TrimSpace(p.DefaultAction), strings.TrimSpace(p.Actions[1].Value)) {
			defaultButton = pstypes.DialogDefaultSecond
		}
	}

	icon := pstypes.IconInformation
	switch strings.ToLower(p.Icon) {
	case "warning":
		icon = pstypes.IconExclamation
	case "error":
		icon = pstypes.IconHand
	case "question":
		icon = pstypes.IconQuestion
	}

	result, err := session.ShowDialogBox(pstypes.DialogBoxOptions{
		Title:         strings.TrimSpace(p.Title),
		Text:          strings.TrimSpace(p.Message),
		Buttons:       buttons,
		DefaultButton: defaultButton,
		Icon:          icon,
		Timeout:       p.TimeoutSeconds,
		ExitOnTimeout: true,
	})
	if err != nil {
		errMsg := fmt.Sprintf("ShowDialogBox: %v", err)
		if a != nil {
			a.logs.append("[agent] psadt-alert [ERRO] type=modal alertId=" + p.AlertID + " " + errMsg)
		}
		return "", errMsg
	}

	action := mapPSADTDialogResult(result, p)
	if a != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-alert [OK] type=modal alertId=%s action=%s", p.AlertID, action))
	}
	return action, ""
}

// showPSADTProgress exibe uma barra de progresso não-bloqueante.
func (a *App) showPSADTProgress(session *psadt.Session, p PsadtAlertPayload) (string, string) {
	statusText := p.StatusText
	if statusText == "" {
		statusText = p.Title
	}
	progressPercent := p.ProgressPercent
	if progressPercent < 0 {
		progressPercent = 0
	}
	if progressPercent > 100 {
		progressPercent = 100
	}

	err := session.ShowInstallationProgress(pstypes.ProgressOptions{
		StatusMessage:       strings.TrimSpace(statusText),
		StatusMessageDetail: strings.TrimSpace(p.Subtitle),
		StatusBarPercentage: float64(progressPercent),
	})
	if err != nil {
		errMsg := fmt.Sprintf("ShowInstallationProgress: %v", err)
		if a != nil {
			a.logs.append("[agent] psadt-alert [ERRO] type=update-progress alertId=" + p.AlertID + " " + errMsg)
		}
		return "", errMsg
	}

	if a != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-alert [OK] type=update-progress alertId=%s action=shown", p.AlertID))
	}
	return "shown", ""
}

// mapPSADTDialogResult converte o resultado tipado do PSADT para o valor da action.
// DialogBoxResult é uma string (ex: "Ok", "Yes", "No", "Cancel", "Timeout").
func mapPSADTDialogResult(result pstypes.DialogBoxResult, p PsadtAlertPayload) string {
	r := strings.ToLower(strings.TrimSpace(string(result)))

	if r == "timeout" {
		if strings.TrimSpace(p.DefaultAction) != "" {
			return strings.TrimSpace(p.DefaultAction)
		}
		return "timeout"
	}

	// Mapeia o texto do botão para a action correspondente.
	for i, a := range p.Actions {
		if strings.EqualFold(r, strings.TrimSpace(a.Label)) || strings.EqualFold(r, strings.TrimSpace(a.Value)) {
			return strings.TrimSpace(a.Value)
		}
		_ = i
	}

	// Fallback: mapeia strings comuns do PSADT.
	switch r {
	case "yes":
		if len(p.Actions) > 0 {
			return strings.TrimSpace(p.Actions[0].Value)
		}
		return "yes"
	case "no":
		if len(p.Actions) > 1 {
			return strings.TrimSpace(p.Actions[1].Value)
		}
		return "no"
	case "cancel":
		if len(p.Actions) > 2 {
			return strings.TrimSpace(p.Actions[2].Value)
		}
		return "cancel"
	}

	if strings.TrimSpace(p.DefaultAction) != "" {
		return strings.TrimSpace(p.DefaultAction)
	}
	return r
}

// showPowerActionWarning exibe o prompt PSADT de restart/shutdown usando
// Show-ADTInstallationRestartPrompt via lib go-psadt.
//
//   - force=false: CountdownSeconds = delaySeconds. O usuário vê countdown
//     visual + botão "Reiniciar Agora". Ao esgotar: action padrão = proceed.
//     O PSADT NÃO executa o restart — apenas retorna quando o countdown acaba
//     ou o usuário clica. O caller deve chamar executeSystemPowerAction depois.
//   - force=true:  CountdownSeconds = min(30, delaySeconds). Countdown visível
//     SEM botão de cancelar. Ao esgotar: proceed.
//
// Retorna:
//   - "proceed" — PSADT countdown concluído, prosseguir com restart.
//   - "proceed_fallback" — PSADT indisponível (disabled, sessão falhou, erro).
//     Caller deve usar shutdown.exe com /t <delay> + diálogo nativo do Windows.
func (a *App) showPowerActionWarning(ctx context.Context, action string, delaySeconds int, force bool, _ string) (string, error) {
	if runtime.GOOS != "windows" {
		return "proceed", nil
	}

	psadtCfg := a.GetAgentConfiguration().PSADT
	if psadtCfg.Enabled == nil || !*psadtCfg.Enabled {
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [SKIP] psadt.enabled=false — fallback shutdown.exe", action))
		return "proceed_fallback", nil
	}

	if delaySeconds <= 0 {
		delaySeconds = 60
	}

	// Cap do countdown forçado: mesmo forçado, dá tempo de salvar trabalho.
	countdown := delaySeconds
	if force && countdown > 30 {
		countdown = 30
	}

	timeout := time.Duration(countdown+120) * time.Second
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := psadt.NewClient(
		psadt.WithTimeout(timeout),
		psadt.WithMinModuleVersion(strings.TrimSpace(psadtCfg.RequiredVersion)),
	)
	if err != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [ERRO] NewClient: %v — fallback", action, err))
		return "proceed_fallback", nil
	}
	defer client.Close()

	session, err := client.OpenSessionWithContext(execCtx, pstypes.SessionConfig{
		AppVendor:      "Discovery",
		AppName:        "Discovery Agent",
		AppVersion:     "1.0",
		DeploymentType: pstypes.DeployInstall,
		DeployMode:     pstypes.DeployModeInteractive,
	})
	if err != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [ERRO] OpenSession: %v — fallback", action, err))
		return "proceed_fallback", nil
	}
	defer func() {
		closeCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	// Show-ADTInstallationRestartPrompt exibe countdown visual nativo:
	// barra de progresso + segundos restantes + botão "Reiniciar Agora".
	// Ao esgotar o countdown, o cmdlet retorna (não executa restart).
	// O caller (handleAgentRuntimeCommand) é responsável pelo shutdown.exe.
	opts := pstypes.RestartPromptOptions{
		CountdownSeconds:       countdown,
		CountdownNoHideSeconds: 5,
		SilentRestart:          false,
		NoCountdown:            false,
	}

	a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [EXEC] countdown=%ds force=%t via ShowInstallationRestartPrompt",
		action, countdown, force))

	if err := session.ShowInstallationRestartPrompt(opts); err != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [ERRO] ShowInstallationRestartPrompt: %v — fallback", action, err))
		return "proceed_fallback", nil
	}

	a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [OK] countdown concluido — proceed", action))
	return "proceed", nil
}

// resolveSystem32Exe returns the absolute path to an executable in System32.
// On 64-bit Windows, uses Sysnative alias when running as 32-bit process
// to avoid WOW64 file system redirection.
func resolveSystem32Exe(exeName string) string {
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = os.Getenv("windir")
	}
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	return filepath.Join(sysRoot, "System32", exeName)
}

// resolveShutdownExe returns the absolute path to shutdown.exe in System32.
func resolveShutdownExe() string {
	return resolveSystem32Exe("shutdown.exe")
}

// resolvePowerShellExe returns the absolute path to powershell.exe.
// Tries System32\WindowsPowerShell\v1.0 first, then falls back to PATH.
func resolvePowerShellExe() string {
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = os.Getenv("windir")
	}
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	// Caminho canonico do powershell.exe no Windows
	psPath := filepath.Join(sysRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(psPath); err == nil {
		return psPath
	}
	// Fallback: tenta o PATH
	if resolved, err := exec.LookPath("powershell.exe"); err == nil {
		return resolved
	}
	return psPath // retorna o caminho canonico mesmo se nao encontrado, o erro sera tratado por quem chama
}

// executeSystemPowerAction executa o restart/shutdown no SO.
//
// delaySeconds:
//   - 0: PSADT ja tratou UX — shutdown.exe /t 0 (imediato, sem dialogo nativo).
//   - >0: PSADT falhou (proceed_fallback) — shutdown.exe /t <delay> com
//     dialogo nativo do Windows para dar tempo do usuario salvar trabalho.
func (a *App) executeSystemPowerAction(_ context.Context, action string, delaySeconds int, _ bool, _ string) (int, string, string) {
	flag := "/s"
	label := "shutdown"
	if action == "restart" || action == "reboot" {
		flag = "/r"
		label = "restart"
	}

	shutdownExe := resolveShutdownExe()
	if _, statErr := os.Stat(shutdownExe); statErr != nil {
		if resolved, lookErr := exec.LookPath("shutdown.exe"); lookErr == nil {
			shutdownExe = resolved
		} else {
			if a != nil {
				a.logs.append(fmt.Sprintf("[agent] %s-action [FATAL] shutdown.exe nao encontrado: Stat=%v LookPath=%v", label, statErr, lookErr))
			}
			return 1, fmt.Sprintf("shutdown.exe nao encontrado: %v", statErr), fmt.Sprintf("falha ao localizar shutdown.exe: %v / PATH: %v", statErr, lookErr)
		}
	}

	delayArg := "0"
	mode := "imediato"
	if delaySeconds > 0 {
		delayArg = fmt.Sprintf("%d", delaySeconds)
		mode = fmt.Sprintf("delay %ds (dialogo nativo)", delaySeconds)
	}

	// /f = forcado (fecha apps sem confirmacao adicional).
	args := []string{flag, "/t", delayArg, "/f"}

	if a != nil {
		a.logs.append(fmt.Sprintf("[agent] %s-action [EXEC] exe=%s args=%s (modo=%s)", label, shutdownExe, strings.Join(args, " "), mode))
	}

	cmd := exec.Command(shutdownExe, args...)
	processutil.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	output := string(out)

	if err != nil {
		if a != nil {
			a.logs.append(fmt.Sprintf("[agent] %s-action [ERRO] exe=%s err=%v output=%q", label, shutdownExe, err, output))
		}
		return 1, output, fmt.Sprintf("falha ao executar %s (%s): %v", label, shutdownExe, err)
	}

	if a != nil {
		a.logs.append(fmt.Sprintf("[agent] %s-action [OK] exe=%s (modo=%s)", label, shutdownExe, mode))
	}
	return 0, fmt.Sprintf("%s executado com sucesso", label), ""
}
