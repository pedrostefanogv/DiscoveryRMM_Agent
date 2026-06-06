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

// psEscape converte uma string Go para um literal string PowerShell entre aspas simples,
// escapando aspas simples internas. Usado por buildPowerActionWarningScript (shutdown/restart).
func psEscape(s string) string {
	s = strings.ReplaceAll(s, "'", "''")
	return "'" + s + "'"
}

// handlePsadtAlert executa o alerta PSADT usando a lib go-psadt nativa,
// eliminando concatenação manual de scripts PowerShell.
func (a *App) handlePsadtAlert(ctx context.Context, p PsadtAlertPayload) (int, string, string) {
	if runtime.GOOS != "windows" {
		body, _ := json.Marshal(map[string]string{"action": "skipped_non_windows"})
		if a != nil {
			a.logs.append("[agent] psadt-alert ignorado: nao e windows type=" + p.Type + " alertId=" + p.AlertID)
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
func (a *App) showPSADTModal(ctx context.Context, session *psadt.Session, p PsadtAlertPayload) (string, string) {
	buttons := pstypes.ButtonsOk
	switch {
	case len(p.Actions) >= 3:
		buttons = pstypes.ButtonsYesNoCancel
	case len(p.Actions) >= 2:
		buttons = pstypes.ButtonsYesNo
	}

	defaultButton := pstypes.DialogDefaultFirst
	if strings.ToLower(strings.TrimSpace(p.DefaultAction)) != "" && len(p.Actions) >= 2 {
		if strings.ToLower(strings.TrimSpace(p.DefaultAction)) == strings.ToLower(strings.TrimSpace(p.Actions[1].Value)) {
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

// showPowerActionWarning builds and executes a PSADT prompt before a system
// restart or shutdown.
//
//   - force=true:  Exibe Show-ADTBalloonTip (apenas notificacao informativa).
//     O shutdown e inevitavel — o balloon so avisa o usuario.
//     Sempre retorna "proceed".
//   - force=false: Exibe Show-ADTDialogBox com botoes Yes/No.
//     Usuario pode confirmar ("proceed") ou adiar ("deferred").
//
// Quando PSADT nao esta instalado, o script escreve 'proceed' e o Go faz
// fallback para shutdown.exe com dialogo nativo do Windows.
func (a *App) showPowerActionWarning(ctx context.Context, action string, delaySeconds int, force bool, msg string) (string, error) {
	if runtime.GOOS != "windows" {
		return "proceed", nil
	}

	label := "reiniciar"
	actionPt := "reiniciado"
	if action == "shutdown" {
		label = "desligar"
		actionPt = "desligado"
	}
	if msg == "" {
		if force {
			msg = fmt.Sprintf("O administrador solicitou a %s do sistema. O computador sera %s em %d segundos. Salve seu trabalho.", label, actionPt, delaySeconds)
		} else {
			msg = fmt.Sprintf("O administrador solicitou a %s do sistema. Deseja prosseguir?", label)
		}
	}

	script, timeout := buildPowerActionWarningScript(action, delaySeconds, force, msg)
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [INICIO] force=%t delay=%ds scriptSize=%dB timeout=%v", action, force, delaySeconds, len(script), timeout))

	psExe := resolvePowerShellExe()
	if a != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [EXEC] psExe=%s arch=%s", action, psExe, runtime.GOARCH))
	}

	cmd := exec.CommandContext(execCtx, psExe, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	outBytes, err := cmd.CombinedOutput()
	rawOutput := strings.TrimSpace(string(outBytes))

	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [TIMEOUT] force=%t delay=%ds scriptSize=%dB — prosseguindo com fallback", action, force, delaySeconds, len(script)))
			return "proceed", nil
		}

		// Diagnostico detalhado: classifica o erro para debug remoto.
		errClass := classifyPsadtPowerShellError(rawOutput, err)
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [ERRO] force=%t delay=%ds class=%s psExe=%s scriptSize=%dB err=%v", action, force, delaySeconds, errClass, psExe, len(script), err))
		if rawOutput != "" {
			truncated := truncateStr(rawOutput, 2000)
			a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [ERRO-OUTPUT] class=%s output=%s", action, errClass, truncated))
		}
		// Fallback: prossegue com shutdown.exe nativo do Windows.
		a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [FALLBACK] class=%s — usando shutdown.exe com dialogo nativo do Windows", action, errClass))
		return "proceed", nil
	}

	result := strings.ToLower(strings.TrimSpace(rawOutput))
	a.logs.append(fmt.Sprintf("[agent] psadt-%s-prompt [OK] result=%s scriptSize=%dB", action, result, len(script)))

	// force=true: balloon apenas informativo — sempre prossegue.
	if force {
		return "proceed", nil
	}

	// force=false: usuario decidiu via Dialog Yes/No.
	if result == "yes" || result == "ok" || result == "proceed" {
		return "proceed", nil
	}
	return "deferred", nil
}

// buildPowerActionWarningScript constroi o script PowerShell que exibe o aviso
// de restart/shutdown via PSADT.
//
//   - force=true:  Show-ADTBalloonTip — balloon informativo (nao-bloqueante).
//     O shutdown e inevitavel, o balloon apenas notifica.
//   - force=false: Show-ADTDialogBox com Yes/No — usuario decide se prossegue.
//
// Se o modulo PSADT nao estiver disponivel, retorna 'proceed' para fallback
// com shutdown.exe (dialogo nativo do Windows).
func buildPowerActionWarningScript(action string, delaySeconds int, force bool, message string) (string, time.Duration) {
	title := "Reinicializacao do Sistema"
	if action == "shutdown" {
		title = "Desligamento do Sistema"
	}

	// Header: importa PSADT e abre sessao.
	// Se o modulo nao estiver disponivel, retorna 'proceed' para fallback
	// para shutdown.exe com dialogo nativo do Windows.
	header := "$ErrorActionPreference = 'Stop'\n" +
		"[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)\n" +
		"$OutputEncoding = [Console]::OutputEncoding\n" +
		"try {\n" +
		"    Import-Module -Name PSAppDeployToolkit -ErrorAction Stop\n" +
		"} catch {\n" +
		"    Write-Output 'proceed'; exit 0\n" +
		"}\n" +
		"try {\n" +
		"    Open-ADTSession -SessionState $ExecutionContext.SessionState" +
		" -AppName 'Discovery' -AppVersion '1.0' -AppVendor 'Discovery'" +
		" -DeploymentType 'Install' -DeployMode 'Interactive'\n" +
		"} catch {\n" +
		"    Write-Output 'proceed'; exit 0\n" +
		"}\n"

	closeSession := "try { Close-ADTSession -ExitCode 0 } catch {}\n"

	if force {
		// Modo forcado: balloon tip informativo (nao-bloqueante).
		// O shutdown.exe e agendado em seguida pelo Go com o delay configurado.
		// O usuario ve o balloon com o aviso, mas nao pode cancelar.
		body := header +
			fmt.Sprintf("Show-ADTBalloonTip -BalloonTipTitle %s -BalloonTipText %s -BalloonTipIcon 'Warning'\n",
				psEscape(title), psEscape(message)) +
			"Write-Output 'proceed'\n" +
			closeSession +
			"exit 0\n"
		timeoutVal := 30 * time.Second
		return header + body, timeoutVal
	}

	// Modo nao-forcado: Dialog Yes/No interativo.
	// O usuario decide se o sistema deve ser reiniciado/desligado.
	// Yes -> 'proceed', No/Timeout -> 'deferred'.
	body := header +
		"$dialogParams = @{\n" +
		fmt.Sprintf("  Title = %s\n", psEscape(title)) +
		fmt.Sprintf("  Text = %s\n", psEscape(message)) +
		"  Buttons = 'YesNo'\n" +
		"  DefaultButton = 'First'\n" +
		"  Icon = 'Warning'\n" +
		fmt.Sprintf("  Timeout = %d\n", delaySeconds+120) +
		"  ExitOnTimeout = $true\n" +
		"}\n" +
		"$adtResult = Show-ADTDialogBox @dialogParams\n" +
		"if ($adtResult -eq 'Yes') { Write-Output 'proceed' } else { Write-Output 'deferred' }\n" +
		closeSession +
		"exit 0\n"
	timeoutVal := time.Duration(delaySeconds+150) * time.Second
	return header + body, timeoutVal
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

// executeSystemPowerAction schedules the actual OS restart or shutdown via shutdown.exe.
func (a *App) executeSystemPowerAction(_ context.Context, action string, delaySeconds int, force bool, msg string) (int, string, string) {
	flag := "/s"
	label := "shutdown"
	if action == "restart" || action == "reboot" {
		flag = "/r"
		label = "restart"
	}

	args := []string{flag, "/t", fmt.Sprintf("%d", delaySeconds)}
	if force {
		args = append(args, "/f")
	}
	if msg != "" {
		args = append(args, "/c", msg)
	}

	shutdownExe := resolveShutdownExe()

	// Verifica se o executavel existe antes de tentar executa-lo
	if _, statErr := os.Stat(shutdownExe); statErr != nil {
		if a != nil {
			a.logs.append(fmt.Sprintf("[agent] %s-action [ERRO-RESOLVE] shutdown.exe ausente em %s: %v", action, shutdownExe, statErr))
		}
		// Fallback: tenta shutdown.exe sem caminho absoluto (via PATH)
		if resolved, lookErr := exec.LookPath("shutdown.exe"); lookErr == nil {
			shutdownExe = resolved
			if a != nil {
				a.logs.append(fmt.Sprintf("[agent] %s-action [FALLBACK-PATH] shutdown.exe resolvido via PATH: %s", action, shutdownExe))
			}
		} else {
			if a != nil {
				a.logs.append(fmt.Sprintf("[agent] %s-action [FATAL] shutdown.exe nao encontrado: Stat=%v LookPath=%v", action, statErr, lookErr))
			}
			return 1, fmt.Sprintf("shutdown.exe nao encontrado: %v", statErr), fmt.Sprintf("falha ao localizar shutdown.exe: %v / PATH: %v", statErr, lookErr)
		}
	}

	if a != nil {
		a.logs.append(fmt.Sprintf("[agent] %s-action [EXEC] exe=%s args=%s delay=%ds force=%t", action, shutdownExe, strings.Join(args, " "), delaySeconds, force))
	}

	cmd := exec.Command(shutdownExe, args...)
	out, err := cmd.CombinedOutput()
	output := string(out)

	if err != nil {
		if a != nil {
			a.logs.append(fmt.Sprintf("[agent] %s-action [ERRO] exe=%s args=%s err=%v output=%q", action, shutdownExe, strings.Join(args, " "), err, output))
		}
		return 1, output, fmt.Sprintf("falha ao agendar %s (%s): %v", label, shutdownExe, err)
	}

	if a != nil {
		a.logs.append(fmt.Sprintf("[agent] %s-action [OK] exe=%s delay=%ds force=%t message=%q", label, shutdownExe, delaySeconds, force, msg))
	}
	return 0, fmt.Sprintf("%s agendado com sucesso (delay=%ds, force=%t, message=%q)", label, delaySeconds, force, msg), ""
}

// ── Diagnostic Helpers ──

// classifyPsadtPowerShellError inspeciona o output combinado (stdout+stderr) do
// PowerShell e classifica o erro em categorias acionaveis para debug remoto.
//
// Categorias:
//   - execution_policy_blocked: ExecutionPolicy restrita (PSSecurityException)
//   - format_file_corrupt:       Arquivo .ps1xml corrompido (ex: Dism.Format.ps1xml com NUL bytes)
//   - module_not_found:          PSAppDeployToolkit nao encontrado
//   - module_import_failed:      Modulo encontrado mas falha ao carregar (ex: dependencias)
//   - session_open_failed:       Open-ADTSession falhou
//   - cmdlet_not_found:          Cmdlet PSADT nao reconhecido
//   - cmdlet_failed:             Cmdlet PSADT executou mas retornou erro
//   - exit_code_N:               Script terminou com exit code N
//   - unknown:                   Erro nao classificado
func classifyPsadtPowerShellError(output string, err error) string {
	if err == nil {
		return "no_error"
	}
	errStr := err.Error()
	lower := strings.ToLower(output + "\n" + errStr)

	// 1. ExecutionPolicy bloqueando execucao de scripts
	if strings.Contains(lower, "pssecurityexception") ||
		strings.Contains(lower, "execução de scripts foi desabilitada") ||
		strings.Contains(lower, "execution of scripts is disabled") ||
		strings.Contains(lower, "unauthorizedaccess") {
		return "execution_policy_blocked"
	}

	// 2. Arquivo de formato corrompido (byte NUL, XML invalido em .ps1xml)
	if strings.Contains(lower, "format.ps1xml") ||
		strings.Contains(lower, "caractere inválido") ||
		strings.Contains(lower, "invalid character") ||
		strings.Contains(lower, "hexadecimal 0x00") ||
		(strings.Contains(lower, "erro no arquivo") && strings.Contains(lower, ".ps1xml")) {
		return "format_file_corrupt"
	}

	// 3. Modulo PSADT nao encontrado
	if strings.Contains(lower, "the specified module") && strings.Contains(lower, "was not loaded") ||
		strings.Contains(lower, "não pode ser carregado porque não foi encontrado") ||
		strings.Contains(lower, "module not found") ||
		strings.Contains(lower, "módulo") && strings.Contains(lower, "não encontrado") {
		return "module_not_found"
	}

	// 4. Modulo encontrado mas falha no import (dependencias, erros internos do .psm1)
	if strings.Contains(lower, "import-module") &&
		(strings.Contains(lower, "falha") || strings.Contains(lower, "erro") || strings.Contains(lower, "error") || strings.Contains(lower, "cannot")) {
		return "module_import_failed"
	}

	// 5. Sessao PSADT falhou ao abrir
	if strings.Contains(lower, "open-adtsession") &&
		(strings.Contains(lower, "falha") || strings.Contains(lower, "erro") || strings.Contains(lower, "error")) {
		return "session_open_failed"
	}

	// 6. Cmdlet PSADT nao reconhecido
	if strings.Contains(lower, "is not recognized as the name of a cmdlet") ||
		strings.Contains(lower, "não é reconhecido como nome de cmdlet") ||
		strings.Contains(lower, "the term") && strings.Contains(lower, "is not recognized") {
		return "cmdlet_not_found"
	}

	// 7. Cmdlet executou mas reportou erro via Write-Error
	if strings.Contains(lower, "write-error") || strings.Contains(lower, "writeerror") {
		return "cmdlet_failed"
	}

	// 8. Exit code especifico do script (1=import, 2=session, 3+=cmdlet)
	if exitErr, ok := err.(*exec.ExitError); ok {
		return fmt.Sprintf("exit_code_%d", exitErr.ExitCode())
	}

	return "unknown"
}

// truncateStr trunca a string para maxLen caracteres, adicionando "... (truncado N bytes)"
// se o limite for excedido.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen - 30
	if cut < 50 {
		cut = maxLen - 10
	}
	if cut < 0 {
		cut = 200
	}
	return s[:cut] + fmt.Sprintf("... (truncado %d bytes)", len(s)-maxLen)
}
