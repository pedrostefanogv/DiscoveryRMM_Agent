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

// showForceRestartBalloon exibe um BalloonTip Warning (não-bloqueante) do PSADT
// informando que o restart forçado ocorrerá em delaySeconds.
//
// Diferente do diálogo interativo, este apenas notifica — o usuário não pode
// cancelar nem adiar. O caller é responsável por aguardar o delay e executar
// o shutdown.
//
// Retorna:
//   - "shown" — balloon exibido com sucesso.
//   - "skipped" — PSADT indisponível (disabled, sessão falhou, erro).
func (a *App) showForceRestartBalloon(action string, delaySeconds int, message string) string {
	if runtime.GOOS != "windows" {
		return "skipped"
	}

	psadtCfg := a.GetAgentConfiguration().PSADT
	if psadtCfg.Enabled == nil || !*psadtCfg.Enabled {
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-force [SKIP] psadt.enabled=false", action))
		return "skipped"
	}

	timeout := 30 * time.Second
	execCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client, err := psadt.NewClient(
		psadt.WithTimeout(timeout),
		psadt.WithMinModuleVersion(strings.TrimSpace(psadtCfg.RequiredVersion)),
	)
	if err != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-force [ERRO] NewClient: %v", action, err))
		return "skipped"
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
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-force [ERRO] OpenSession: %v", action, err))
		return "skipped"
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	balloonText := fmt.Sprintf("O computador será reiniciado em %d segundos.", delaySeconds)
	if strings.TrimSpace(message) != "" {
		balloonText = fmt.Sprintf("%s\n\nReinicialização em %d segundos.", message, delaySeconds)
	}

	err = session.ShowBalloonTip(pstypes.BalloonTipOptions{
		BalloonTipTitle: "Reinicialização Forçada",
		BalloonTipText:  balloonText,
		BalloonTipIcon:  pstypes.BalloonWarning,
		NoWait:          true,
	})
	if err != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-force [ERRO] ShowBalloonTip: %v", action, err))
		return "skipped"
	}

	a.logs.append(fmt.Sprintf("[agent] psadt-%s-force [OK] balloon exibido delay=%ds", action, delaySeconds))
	return "shown"
}

// showDeferrableRestartPrompt exibe um diálogo PSADT bloqueante com opção de adiar.
//
// Usa ShowDialogBox com botões Yes/No:
//   - "Yes" → restart agora ("restart_now")
//   - "No"  → adiar ("defer")
//   - timeout (delaySeconds) → restart ("restart_now")
//
// Se PSADT indisponível, retorna "fallback" para o caller usar
// DispatchNotification como alternativa.
//
// Retorna: "restart_now", "defer", ou "fallback".
func (a *App) showDeferrableRestartPrompt(action string, delaySeconds int, message string, deferMinutes int) string {
	if runtime.GOOS != "windows" {
		return "restart_now"
	}

	psadtCfg := a.GetAgentConfiguration().PSADT
	if psadtCfg.Enabled == nil || !*psadtCfg.Enabled {
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-defer [SKIP] psadt.enabled=false — fallback notification", action))
		return "fallback"
	}

	if delaySeconds <= 0 {
		delaySeconds = 300
	}

	dialogText := fmt.Sprintf("O computador precisa ser reiniciado.\n\nTempo restante: %d segundos.\n\nClique em Sim para reiniciar agora ou Não para adiar", delaySeconds)
	if deferMinutes > 0 {
		dialogText = fmt.Sprintf("O computador precisa ser reiniciado.\n\nTempo restante: %d segundos.\n\nClique em Sim para reiniciar agora ou Não para adiar por %d minutos", delaySeconds, deferMinutes)
	}
	if strings.TrimSpace(message) != "" {
		dialogText = message
		if deferMinutes > 0 {
			dialogText += fmt.Sprintf("\n\nAdiamento disponível: %d minutos", deferMinutes)
		}
	}

	timeout := time.Duration(delaySeconds+60) * time.Second
	execCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client, err := psadt.NewClient(
		psadt.WithTimeout(timeout),
		psadt.WithMinModuleVersion(strings.TrimSpace(psadtCfg.RequiredVersion)),
	)
	if err != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-defer [ERRO] NewClient: %v — fallback", action, err))
		return "fallback"
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
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-defer [ERRO] OpenSession: %v — fallback", action, err))
		return "fallback"
	}
	defer func() {
		closeCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	a.logs.append(fmt.Sprintf("[agent] psadt-%s-defer [EXEC] delay=%ds deferMinutes=%d via ShowDialogBox", action, delaySeconds, deferMinutes))

	// Usa ShowDialogBox com Yes/No para oferecer escolha clara.
	// Yes = reiniciar agora, No = adiar.
	result, err := session.ShowDialogBox(pstypes.DialogBoxOptions{
		Title:         "Reinicialização Necessária",
		Text:          dialogText,
		Buttons:       pstypes.ButtonsYesNo,
		DefaultButton: pstypes.DialogDefaultFirst,
		Icon:          pstypes.IconExclamation,
		Timeout:       delaySeconds,
		ExitOnTimeout: true,
	})
	if err != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-defer [ERRO] ShowDialogBox: %v — fallback", action, err))
		return "fallback"
	}

	resultStr := strings.ToLower(strings.TrimSpace(string(result)))
	a.logs.append(fmt.Sprintf("[agent] psadt-%s-defer [OK] result=%s", action, resultStr))

	switch resultStr {
	case "yes":
		return "restart_now"
	case "timeout":
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-defer timeout após %ds — restart forçado", action, delaySeconds))
		return "restart_now"
	default:
		// "no" ou qualquer outro resultado → adiar
		return "defer"
	}
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
// Usa shutdown.exe com diálogo nativo do Windows (countdown + botão "Fechar").
// force=true adiciona /f (fecha apps sem confirmação adicional).
// message (se não vazia) é exibida no diálogo nativo via /c.
func (a *App) executeSystemPowerAction(_ context.Context, action string, delaySeconds int, force bool, message string) (int, string, string) {
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

	if delaySeconds <= 0 {
		delaySeconds = 60
	}

	args := []string{flag, "/t", fmt.Sprintf("%d", delaySeconds)}

	if force {
		args = append(args, "/f")
	}

	if strings.TrimSpace(message) != "" {
		args = append(args, "/c", message)
	}

	if a != nil {
		a.logs.append(fmt.Sprintf("[agent] %s-action [EXEC] exe=%s args=%s (modo=delay %ds force=%t)", label, shutdownExe, strings.Join(args, " "), delaySeconds, force))
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
		a.logs.append(fmt.Sprintf("[agent] %s-action [OK] exe=%s (modo=delay %ds)", label, shutdownExe, delaySeconds))
	}
	return 0, fmt.Sprintf("%s agendado com sucesso (delay=%ds)", label, delaySeconds), ""
}
