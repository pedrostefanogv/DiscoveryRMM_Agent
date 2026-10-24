//go:build windows

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"discovery/app/core/notifyui"
	"discovery/app/core/processutil"
)

// maxPsadtDialogTimeoutSeconds era o limite do Show-ADTDialogBox do PSADT
// (UI.DefaultTimeout = 3300s). Mantido como guarda de segurança para prompts
// "sem timeout" do backend nativo não ficarem eternos.
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

// normalizePsadtAlertIcon converte aliases de ícone (mantido pelo contrato do
// servidor; o cartão nativo usa tema/acento do Windows).
func normalizePsadtAlertIcon(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "warning", "warn":
		return "Warning"
	case "error", "stop":
		return "Error"
	case "question":
		return "Question"
	default:
		return "Information"
	}
}

// mapPSADTPromptResult mantém o mapeamento legado texto→value (usado pelos
// testes e compatível com o contrato psadt-alert: o resultado vazio/timeout
// usa DefaultAction quando configurado).
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

// psadtActionLabel devolve o texto exibido no botão da ação (label ou value).
func psadtActionLabel(action PsadtAlertAction) string {
	if label := strings.TrimSpace(action.Label); label != "" {
		return label
	}
	return strings.TrimSpace(action.Value)
}

// handlePsadtAlert executa o alerta do servidor (commandType 9) pelo backend
// NATIVO (cartão winui/toast do Windows Runtime) — sem PSAppDeployToolkit.
// Contrato preservado: (exitCode, body, err) com action mostrada no body.
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
		// Modal que aguarda o usuário: 0 = sem timeout.
		p.TimeoutSeconds = 0
	case p.TimeoutSeconds <= 0:
		p.TimeoutSeconds = 120
	}
	return p, nil
}

// handlePsadtAlert executa o alerta do servidor (commandType 9) pelo backend
// NATIVO (cartão winui/toast do Windows Runtime) — sem PSAppDeployToolkit.
func (a *App) handlePsadtAlert(ctx context.Context, p PsadtAlertPayload) (int, string, string) {
	// Tipos desconhecidos caem no toast; o parse ja converte vazio em "toast".
	switch p.Type {
	case "modal", "toast", "update-progress":
	default:
		p.Type = "toast"
	}

	// Guarda do timeout para prompts "sem timeout" (waitForUser).
	if p.Type == "modal" && !p.WaitForUser && p.TimeoutSeconds > maxPsadtDialogTimeoutSeconds {
		a.Logs.Append(fmt.Sprintf("[agent] psadt-alert timeout=%ds acima do limite (%ds); clamp aplicado", p.TimeoutSeconds, maxPsadtDialogTimeoutSeconds))
		p.TimeoutSeconds = maxPsadtDialogTimeoutSeconds
	}

	var code int
	var body, errMsg string
	var handled bool
	switch p.Type {
	case "toast":
		code, body, errMsg, handled = a.nativeToastAlert(ctx, p)
	case "modal":
		code, body, errMsg, handled = a.nativeModalAlert(ctx, p)
	case "update-progress":
		code, body, errMsg, handled = a.nativeProgressAlert(ctx, p)
	}
	if handled {
		return code, body, errMsg
	}

	// Backend nativo indisponível (janela não criada etc.): erro explícito no
// contrato — não há mais caminho PSADT de fallback.
	errMsg = strings.TrimSpace(errMsg)
	if errMsg == "" {
		errMsg = "backend nativo de notificacoes indisponivel"
	}
	a.Logs.Append("[agent] psadt-alert [ERRO] type=" + p.Type + " alertId=" + p.AlertID + " " + errMsg)
	return 1, "", errMsg
}

// showNativePowerCountdown exibe o aviso nativo de reinicio/desligamento com
// CONTAGEM regressiva visível e ações (substituto do RestartPrompt/Welcome do
// PSADT). Retorna:
//
//	"proceed"  — usuario confirmou (OK) ou o contador terminou;
//	"defer"    — usuario clicou em adiar/fechar (force=false);
//	"fallback" — nao foi possivel exibir o cartão nativo.
func (a *App) showNativePowerCountdown(action string, delaySeconds int, message string, force bool) string {
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

	buttons := []notifyui.Button{}
	if !force {
		buttons = append(buttons, notifyui.Button{ID: "defer", Label: "Adiar"})
	}
	buttons = append(buttons, notifyui.Button{ID: "proceed", Label: okText, Kind: "primary"})

	// force=true: sem botão de adiamento — fechar/Esc também prossegue,
// replicando o CountdownNoDefer do PSADT.
	closeID := "defer"
	if force {
		closeID = "proceed"
	}

	req := notifyui.CardRequest{
		Title:            title,
		Subtitle:         "Discovery Agent",
		Message:          body,
		Buttons:          buttons,
		DefaultButtonID:  "proceed",
		TimeoutButtonID:  "proceed",
		CloseButtonID:    closeID,
		CountdownSeconds: delaySeconds,
		CountdownLabel:   countdownLabel,
		Hint:             "Enter confirma · Esc adia",
	}

	a.logPowerAction(action, fmt.Sprintf("exibindo aviso nativo com contador (delay=%ds, force=%t)", delaySeconds, force))
	cardCtx, cancel := context.WithTimeout(context.Background(), time.Duration(delaySeconds+30)*time.Second)
	defer cancel()

	result, ok := runCardAsUser(cardCtx, req, time.Duration(delaySeconds+30)*time.Second)
	if !ok || result.Outcome == notifyui.OutcomeError {
		a.logPowerAction(action, fmt.Sprintf("cartão nativo indisponivel (%s) — fallback", result.Reason))
		return "fallback"
	}
	switch {
	case result.Outcome == notifyui.OutcomeTimeout:
		return "proceed"
	case result.Outcome == notifyui.OutcomeClicked && result.ButtonID == "proceed":
		return "proceed"
	case result.Outcome == notifyui.OutcomeClicked && result.ButtonID == "defer":
		return "defer"
	default:
		// Fechado (Esc/X) ou cancelado: respeita a semântica do force.
		if force {
			return "proceed"
		}
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
		sysRoot = "C:\\Windows"
	}
	return filepath.Join(sysRoot, "System32", exeName)
}

// resolveShutdownExe returns the absolute path to shutdown.exe in System32.
func resolveShutdownExe() string {
	return resolveSystem32Exe("shutdown.exe")
}

// resolvePowerShellExe returns the absolute path to powershell.exe.
func resolvePowerShellExe() string {
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = os.Getenv("windir")
	}
	if sysRoot == "" {
		sysRoot = "C:\\Windows"
	}
	psPath := filepath.Join(sysRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(psPath); err == nil {
		return psPath
	}
	if resolved, err := exec.LookPath("powershell.exe"); err == nil {
		return resolved
	}
	return psPath
}

// executeSystemPowerAction executa o restart/shutdown no SO (shutdown.exe).
// delaySeconds == 0 executa imediatamente; force=true adiciona /f.
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
			a.logPowerAction(label, fmt.Sprintf("[FATAL] shutdown.exe nao encontrado: Stat=%v LookPath=%v", statErr, lookErr))
			return 1, fmt.Sprintf("shutdown.exe nao encontrado: %v", statErr), fmt.Sprintf("falha ao localizar shutdown.exe: %v / PATH: %v", statErr, lookErr)
		}
	}

	if delaySeconds < 0 {
		delaySeconds = 0
	}

	args := []string{flag, "/t", fmt.Sprintf("%d", delaySeconds)}
	if force {
		args = append(args, "/f")
	}
	if strings.TrimSpace(message) != "" {
		args = append(args, "/c", message)
	}

	a.logPowerAction(label, fmt.Sprintf("[EXEC] exe=%s args=%s (modo=delay %ds force=%t)", shutdownExe, strings.Join(args, " "), delaySeconds, force))
	cmd := exec.Command(shutdownExe, args...)
	processutil.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	output := string(out)

	if err != nil {
		a.logPowerAction(label, fmt.Sprintf("[ERRO] exe=%s err=%v output=%q", shutdownExe, err, output))
		return 1, output, fmt.Sprintf("falha ao executar %s (%s): %v", label, shutdownExe, err)
	}

	a.logPowerAction(label, fmt.Sprintf("[OK] exe=%s (modo=delay %ds)", shutdownExe, delaySeconds))
	return 0, fmt.Sprintf("%s agendado com sucesso (delay=%ds)", label, delaySeconds), ""
}