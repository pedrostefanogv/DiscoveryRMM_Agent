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

	"discovery/app/core/processutil"
)

// maxPsadtDialogTimeoutSeconds é o maior -Timeout que o Show-ADTDialogBox
// aceita com o config.psd1 padrão do PSADT (UI.DefaultTimeout = 3300s).
// Um valor acima disso faz o cmdlet lançar erro de validação e o diálogo não
// é exibido — por isso o timeout é clampado antes de chegar ao PSADT.
const maxPsadtDialogTimeoutSeconds = 3300

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
	switch {
	case p.Type == "toast":
		// Toast sempre auto-fecha; default curto quando não informado.
		if p.TimeoutSeconds <= 0 {
			p.TimeoutSeconds = 15
		}
	case p.WaitForUser:
		// Modal que aguarda o usuário clicar em OK: 0 significa "sem timeout"
		// e é propagado assim para o Show-ADTDialogBox.
		p.TimeoutSeconds = 0
	case p.TimeoutSeconds <= 0:
		p.TimeoutSeconds = 120
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

// registerPSADTSessionHooks registra hooks de lifecycle (OnClose/OnError) na
// sessão para logging padronizado e consistente em todos os fluxos PSADT.
func (a *App) registerPSADTSessionHooks(session *psadt.Session, tag string) {
	if session == nil {
		return
	}
	session.OnClose(func(exitCode int) {
		if a != nil {
			a.Logs.Append(fmt.Sprintf("[agent] psadt-%s [OK] sessão fechada exitCode=%d", tag, exitCode))
		}
	})
	session.OnError(func(err error) {
		if a != nil {
			a.Logs.Append(fmt.Sprintf("[agent] psadt-%s [ERRO] %v", tag, err))
		}
	})
}

// handlePsadtAlert executa o alerta PSADT usando a lib go-psadt nativa,
// eliminando concatenação manual de scripts PowerShell.
func (a *App) handlePsadtAlert(ctx context.Context, p PsadtAlertPayload) (int, string, string) {
	if runtime.GOOS != "windows" {
		body, _ := json.Marshal(map[string]string{"action": "skipped_non_windows"})
		if a != nil {
			a.Logs.Append("[agent] psadt-alert ignorado: não é windows type=" + p.Type + " alertId=" + p.AlertID)
		}
		return 0, string(body), ""
	}

	psadtCfg := a.GetAgentConfiguration().PSADT
	if psadtCfg.Enabled == nil || !*psadtCfg.Enabled {
		body, _ := json.Marshal(map[string]string{"action": "skipped_disabled"})
		if a != nil {
			a.Logs.Append("[agent] psadt-alert ignorado: psadt.enabled=false type=" + p.Type + " alertId=" + p.AlertID)
		}
		return 0, string(body), ""
	}

	// Tipos desconhecidos caem no toast (balloon nativo); o parse ja converte
	// vazio em "toast".
	switch p.Type {
	case "modal", "toast", "update-progress":
	default:
		p.Type = "toast"
	}

	// O PSADT rejeita -Timeout acima de UI.DefaultTimeout do config.psd1
	// (padrão 3300s): o cmdlet lança erro e nada aparece na tela do usuário.
	// Clampa para garantir a entrega.
	if p.Type == "modal" && !p.WaitForUser && p.TimeoutSeconds > maxPsadtDialogTimeoutSeconds {
		if a != nil {
			a.Logs.Append(fmt.Sprintf("[agent] psadt-alert timeout=%ds acima do limite PSADT (%ds); clamp aplicado", p.TimeoutSeconds, maxPsadtDialogTimeoutSeconds))
		}
		p.TimeoutSeconds = maxPsadtDialogTimeoutSeconds
	}

	// Notificação modal: usa o prompt nativo do PSADT
	// (Show-ADTInstallationPrompt, estilo Fluent) — o MESMO caminho da
	// "Notificação Visual Nativa" (Prompt) do console de debug do agent — em
	// vez da MessageBox Win32 do Show-ADTDialogBox, que não tem o visual
	// Fluent/branding. Retorna antes de criar o client go-psadt (o prompt
	// nativo roda via script próprio).
	if p.Type == "modal" {
		return a.showPSADTFluentPrompt(p)
	}

	// Toast: usa o balloon nativo do PSADT com branding (TrayTitle/TrayIcon)
	// em vez do caminho go-psadt, que exibia "PSAppDeployToolkit" no título e o
	// ícone default do toolkit. Também retorna antes de criar o client.
	if p.Type == "toast" {
		return a.showPSADTBalloon(p)
	}

	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] psadt-alert iniciando type=%s alertId=%s timeout=%ds via go-psadt", p.Type, p.AlertID, p.TimeoutSeconds))
	}

	// Init timeout: Import-Module + Get-Module -ListAvailable pode demorar.
	// Mínimo de 90s. Modal já retornou acima (prompt nativo); aqui só restam
	// toast/update-progress, ambos não-bloqueantes.
	initTimeout := 90 * time.Second
	execCtx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()

	client, err := psadt.NewClient(
		psadt.WithTimeout(initTimeout),
		psadt.WithMinModuleVersion(strings.TrimSpace(psadtCfg.RequiredVersion)),
	)
	if err != nil {
		errMsg := fmt.Sprintf("psadt.NewClient: %v", err)
		if a != nil {
			a.Logs.Append("[agent] psadt-alert [ERRO] " + errMsg)
		}
		return 1, "", errMsg
	}
	defer client.Close()

	session, err := client.OpenSessionWithContext(execCtx, pstypes.NewSessionConfig().
		App("Discovery", "Discovery Agent", "1.0").
		Install().
		Interactive().
		Build())
	if err != nil {
		errMsg := fmt.Sprintf("psadt.OpenSession: %v", err)
		if a != nil {
			a.Logs.Append("[agent] psadt-alert [ERRO] " + errMsg)
		}
		return 1, "", errMsg
	}
	a.registerPSADTSessionHooks(session, "alert")
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	// So "update-progress" chega aqui (modal/toast retornaram antes).
	action, errMsg := a.showPSADTProgress(session, p)
	if errMsg != "" {
		return 1, "", errMsg
	}
	body, _ := json.Marshal(map[string]string{"action": action})
	return 0, string(body), ""
}

// showPSADTBalloon exibe a notificação como balloon/toast nativo do PSADT
// (Show-ADTBalloonTip) com branding próprio — TrayTitle (Toolkit.CompanyName)
// e TrayIcon (Assets.Logo) — em vez do caminho go-psadt, que mostrava
// "PSAppDeployToolkit" e o ícone default do toolkit.
func (a *App) showPSADTBalloon(p PsadtAlertPayload) (int, string, string) {
	notifType := "balloon_info"
	switch strings.ToLower(strings.TrimSpace(p.Icon)) {
	case "warning", "warn":
		notifType = "balloon_warning"
	case "error", "stop":
		notifType = "balloon_error"
	}

	// BalloonTipTime em segundos (o script converte para ms). O console de
	// debug limita 1..120s; 0 usa o default de 10s do PSADT.
	balloonTime := p.TimeoutSeconds
	if balloonTime <= 0 {
		balloonTime = 10
	}
	if balloonTime > 120 {
		balloonTime = 120
	}

	req := PSADTVisualNotificationRequest{
		NotifType:          notifType,
		Title:              strings.TrimSpace(p.Title),
		Message:            strings.TrimSpace(p.Message),
		AppName:            "Discovery Agent",
		BalloonTimeSeconds: balloonTime,
		// Não-bloqueante: o toast não prende o processamento do comando.
		BalloonNoWait: true,
	}

	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] psadt-alert iniciando type=toast (balloon nativo) alertId=%s notifType=%s", p.AlertID, notifType))
	}

	result := a.ExecutePSADTVisualNotification(req)
	if !result.Success {
		errMsg := strings.TrimSpace(result.Error)
		if errMsg == "" {
			errMsg = "falha ao exibir a notificação toast"
		}
		if a != nil {
			a.Logs.Append("[agent] psadt-alert [ERRO] type=toast alertId=" + p.AlertID + " " + errMsg)
		}
		return 1, result.Output, errMsg
	}

	body, _ := json.Marshal(map[string]string{"action": "shown", "mode": "balloon"})
	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] psadt-alert [OK] type=toast alertId=%s action=shown", p.AlertID))
	}
	return 0, string(body), ""
}

// showPSADTFluentPrompt exibe a notificação como prompt nativo do PSADT
// (Show-ADTInstallationPrompt), com o estilo Fluent e o branding/logo padrão do
// Discovery Agent. É o mesmo caminho da "Notificação Visual Nativa" (Prompt) do
// console de debug do agent, em vez da MessageBox Win32 do Show-ADTDialogBox.
//
// waitForUser (ou timeout <= 0) usa o maior timeout aceito pelo PSADT
// (UI.DefaultTimeout, 3300s); com timeout positivo o prompt auto-fecha.
func (a *App) showPSADTFluentPrompt(p PsadtAlertPayload) (int, string, string) {
	timeout := p.TimeoutSeconds
	if p.WaitForUser || timeout <= 0 {
		timeout = maxPsadtDialogTimeoutSeconds
	}
	if timeout > maxPsadtDialogTimeoutSeconds {
		timeout = maxPsadtDialogTimeoutSeconds
	}

	// PSADT 4.1.x: o prompt custom (Show-ADTInstallationPrompt) NAO tem UI de
	// contagem regressiva - o -Timeout apenas fecha a janela (confirmado na
	// documentacao oficial e visualmente). Quando ha auto-fechamento, informamos
	// o prazo na propria mensagem para o usuario saber que ela fecha sozinha.
	message := strings.TrimSpace(p.Message)
	if !p.WaitForUser && p.TimeoutSeconds > 0 {
		message = fmt.Sprintf("%s\n\nEsta janela fechara automaticamente em %d segundos.", message, timeout)
	}

	req := PSADTVisualNotificationRequest{
		NotifType: "prompt_ok",
		Title:     strings.TrimSpace(p.Title),
		Message:   message,
		Subtitle:  strings.TrimSpace(p.Subtitle),
		AppName:   "Discovery Agent",
		// p.Icon já vem normalizado ("Warning"|"Error"|"Information"|"Question");
		// normalizePromptIcon aceita essas formas e converte para DialogSystemIcon.
		PromptIcon:    p.Icon,
		PromptTimeout: timeout,
		// DialogStyle vazio mantém o default do módulo (Fluent) e, sem logo
		// customizado, writePSADTVisualBranding aplica o appiconPSADT.png.
	}

	// O prompt Fluent aceita até 3 botões (-ButtonLeftText/-ButtonMiddleText/
	// -ButtonRightText). Alertas agendados podem trazer ações customizadas
	// (ActionsJson); sem ações, o padrão é um único botão OK.
	switch {
	case len(p.Actions) >= 3:
		req.PromptLeftText = psadtActionLabel(p.Actions[0])
		req.PromptMiddleText = psadtActionLabel(p.Actions[1])
		req.PromptRightText = psadtActionLabel(p.Actions[2])
	case len(p.Actions) == 2:
		req.PromptLeftText = psadtActionLabel(p.Actions[0])
		req.PromptRightText = psadtActionLabel(p.Actions[1])
	case len(p.Actions) == 1:
		req.PromptRightText = psadtActionLabel(p.Actions[0])
	default:
		req.PromptRightText = "OK"
	}

	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] psadt-alert iniciando type=modal (prompt Fluent) alertId=%s timeout=%ds", p.AlertID, timeout))
	}

	result := a.ExecutePSADTVisualNotification(req)
	if !result.Success && req.PromptTimeout > 0 &&
		strings.Contains(strings.ToLower(result.Error+" "+result.Output), "timeout") {
		// O UI.DefaultTimeout do config.psd1 da máquina pode ser menor que o
		// global; nesse caso o PSADT rejeita o -Timeout e nenhum prompt aparece.
		// Reexibe com um timeout conservador para não perder o aviso.
		if a != nil {
			a.Logs.Append(fmt.Sprintf("[agent] psadt-alert [WARN] prompt Fluent com timeout=%ds falhou (%s); retry com timeout menor", req.PromptTimeout, strings.TrimSpace(result.Error)))
		}
		retry := req
		retry.PromptTimeout = 120
		result = a.ExecutePSADTVisualNotification(retry)
	}
	if !result.Success {
		errMsg := strings.TrimSpace(result.Error)
		if errMsg == "" {
			errMsg = "falha ao exibir prompt nativo do PSADT"
		}
		if a != nil {
			a.Logs.Append("[agent] psadt-alert [ERRO] type=modal alertId=" + p.AlertID + " " + errMsg)
		}
		return 1, result.Output, errMsg
	}

	action := mapPSADTPromptResult(result.Result, p)
	body, _ := json.Marshal(map[string]string{
		"action": action,
		"mode":   "fluent-prompt",
		"result": strings.TrimSpace(result.Result),
	})
	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] psadt-alert [OK] type=modal alertId=%s action=%s", p.AlertID, action))
	}
	return 0, string(body), ""
}

// psadtActionLabel devolve o texto exibido no botão da ação (label ou value).
func psadtActionLabel(action PsadtAlertAction) string {
	if label := strings.TrimSpace(action.Label); label != "" {
		return label
	}
	return strings.TrimSpace(action.Value)
}

// mapPSADTPromptResult converte o texto do botão clicado no prompt Fluent para
// o value da ação correspondente. Resultado vazio/Timeout usa DefaultAction
// quando configurado. O Show-ADTInstallationPrompt devolve o texto do botão.
func mapPSADTPromptResult(resultText string, p PsadtAlertPayload) string {
	r := strings.ToLower(strings.TrimSpace(resultText))

	if r == "" || r == "timeout" {
		if v := strings.TrimSpace(p.DefaultAction); v != "" {
			return v
		}
		if r == "timeout" {
			return "timeout"
		}
		return "ok"
	}

	for _, action := range p.Actions {
		if strings.EqualFold(r, strings.TrimSpace(action.Label)) || strings.EqualFold(r, strings.TrimSpace(action.Value)) {
			return strings.TrimSpace(action.Value)
		}
	}

	return r
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
			a.Logs.Append("[agent] psadt-alert [ERRO] type=update-progress alertId=" + p.AlertID + " " + errMsg)
		}
		return "", errMsg
	}

	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] psadt-alert [OK] type=update-progress alertId=%s action=shown", p.AlertID))
	}
	return "shown", ""
}

// showPSADTFluentPowerCountdown exibe o aviso Fluent de reinicio/desligamento
// com CONTAGEM regressiva visivel e botoes de acao (mesmo visual da
// "Notificacao Visual Nativa" -> countdown). Reaproveita o NotifType
// "countdown" (Welcome + ForceCountdown) com textos customizados via staging.
//
// Retorna:
//   - "proceed"  — usuario confirmou (OK) ou o contador terminou.
//   - "defer"    — usuario clicou em adiar/fechar.
//   - "fallback" — nao foi possivel exibir (PSADT indisponivel/erro).
func (a *App) showPSADTFluentPowerCountdown(action string, delaySeconds int, message string) string {
	if runtime.GOOS != "windows" {
		return "fallback"
	}
	if delaySeconds <= 0 {
		delaySeconds = 60
	}

	title := "Reinicializacao Necessaria"
	okText := "Reiniciar agora"
	countdownLabel := "O computador sera reiniciado em:"
	defaultMsg := fmt.Sprintf("O computador sera reiniciado automaticamente em %d segundos.", delaySeconds)
	if action == "shutdown" {
		title = "Desligamento Necessario"
		okText = "Desligar agora"
		countdownLabel = "O computador sera desligado em:"
		defaultMsg = fmt.Sprintf("O computador sera desligado automaticamente em %d segundos.", delaySeconds)
	}

	body := strings.TrimSpace(message)
	if body == "" {
		body = defaultMsg
	}

	req := PSADTVisualNotificationRequest{
		NotifType:       "countdown",
		Title:           title,
		Message:         body,
		Subtitle:        "Discovery Agent",
		AppName:         "Discovery Agent",
		ButtonText:      okText,
		ButtonCloseText: "Adiar",
		CountdownLabel:  countdownLabel,
		PromptTimeout:   delaySeconds,
	}

	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] %s-action iniciando aviso Fluent com contador delay=%ds", action, delaySeconds))
	}

	result := a.ExecutePSADTVisualNotification(req)
	output := result.Output
	outputLower := strings.ToLower(output)

	// O script so chega ao fim (marcador) se o usuario nao adiou.
	if strings.Contains(output, "COUNTDOWN-PROCEED") {
		return "proceed"
	}
	if strings.Contains(outputLower, "deferred") || strings.Contains(outputLower, "adiad") {
		return "defer"
	}

	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] %s-action [WARN] aviso Fluent nao confirmou (exit=%d): %s", action, result.ExitCode, strings.TrimSpace(result.Error)))
	}
	return "fallback"
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
				a.Logs.Append(fmt.Sprintf("[agent] %s-action [FATAL] shutdown.exe nao encontrado: Stat=%v LookPath=%v", label, statErr, lookErr))
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
		a.Logs.Append(fmt.Sprintf("[agent] %s-action [EXEC] exe=%s args=%s (modo=delay %ds force=%t)", label, shutdownExe, strings.Join(args, " "), delaySeconds, force))
	}

	cmd := exec.Command(shutdownExe, args...)
	processutil.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	output := string(out)

	if err != nil {
		if a != nil {
			a.Logs.Append(fmt.Sprintf("[agent] %s-action [ERRO] exe=%s err=%v output=%q", label, shutdownExe, err, output))
		}
		return 1, output, fmt.Sprintf("falha ao executar %s (%s): %v", label, shutdownExe, err)
	}

	if a != nil {
		a.Logs.Append(fmt.Sprintf("[agent] %s-action [OK] exe=%s (modo=delay %ds)", label, shutdownExe, delaySeconds))
	}
	return 0, fmt.Sprintf("%s agendado com sucesso (delay=%ds)", label, delaySeconds), ""
}
