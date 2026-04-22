package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
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

// buildPsadtAlertScript constrói o script PowerShell que executa o alerta PSADT.
// Retorna o script e o timeout máximo para execução.
func buildPsadtAlertScript(p PsadtAlertPayload) (string, time.Duration) {
	balloonIcon := "Info"
	switch strings.ToLower(p.Icon) {
	case "Warning":
		balloonIcon = "Warning"
	case "Error":
		balloonIcon = "Error"
	}

	header := "$ErrorActionPreference = 'Stop'\n" +
		"[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)\n" +
		"$OutputEncoding = [Console]::OutputEncoding\n" +
		"try {\n" +
		"    Import-Module -Name PSAppDeployToolkit -ErrorAction Stop\n" +
		"} catch {\n" +
		"    Write-Error 'Falha ao importar PSAppDeployToolkit'; exit 1\n" +
		"}\n"

	openSession := "try {\n" +
		"    Open-ADTSession -SessionState $ExecutionContext.SessionState" +
		" -AppName 'Discovery' -AppVersion '1.0' -AppVendor 'Discovery'" +
		" -DeploymentType 'Install' -DeployMode 'Interactive'\n" +
		"} catch {\n" +
		"    Write-Error \"Falha ao abrir sessao PSADT: $_\"; exit 2\n" +
		"}\n"

	closeSession := "try { Close-ADTSession -ExitCode 0 } catch {}\n"

	switch p.Type {
	case "toast":
		// Toast: exibido via BalloonTip, non-blocking, fechamento automático pelo SO.
		body := openSession +
			fmt.Sprintf("Show-ADTBalloonTip -BalloonTipTitle %s -BalloonTipText %s -BalloonTipIcon '%s'\n",
				psEscape(p.Title), psEscape(p.Message), balloonIcon) +
			"Write-Host 'shown'\n" +
			closeSession +
			"exit 0\n"
		timeout := time.Duration(p.TimeoutSeconds+30) * time.Second
		return header + body, timeout

	case "modal":
		// Modal: DialogBox bloqueante com botões de ação.
		// Constrói a lista de botões a partir das actions; usa YesNo como fallback.
		buttons := buildDialogButtons(p.Actions)
		defaultBtn := "First"
		if strings.ToLower(strings.TrimSpace(p.DefaultAction)) != "" && len(p.Actions) >= 2 {
			defaultBtn = mapDefaultAction(p.DefaultAction, p.Actions)
		}

		body := openSession +
			"$dialogParams = @{\n" +
			fmt.Sprintf("  Title = %s\n", psEscape(p.Title)) +
			fmt.Sprintf("  Text = %s\n", psEscape(p.Message)) +
			fmt.Sprintf("  Buttons = '%s'\n", buttons) +
			fmt.Sprintf("  DefaultButton = '%s'\n", defaultBtn) +
			fmt.Sprintf("  Icon = '%s'\n", p.Icon) +
			"}\n" +
			fmt.Sprintf("if (%d -gt 0) { $dialogParams.Timeout = %d }\n", p.TimeoutSeconds, p.TimeoutSeconds) +
			"$dialogParams.ExitOnTimeout = $true\n" +
			"$adtResult = Show-ADTDialogBox @dialogParams\n" +
			"if ($null -ne $adtResult) { Write-Host $adtResult } else { Write-Host 'timeout' }\n" +
			closeSession +
			"exit 0\n"
		timeout := time.Duration(p.TimeoutSeconds+30) * time.Second
		return header + body, timeout

	default:
		// Fallback: trata como toast.
		body := openSession +
			fmt.Sprintf("Show-ADTBalloonTip -BalloonTipTitle %s -BalloonTipText %s -BalloonTipIcon 'Info'\n",
				psEscape(p.Title), psEscape(p.Message)) +
			"Write-Host 'shown'\n" +
			closeSession +
			"exit 0\n"
		return header + body, 45 * time.Second
	}
}

// buildDialogButtons retorna o valor do parâmetro -Buttons para Show-ADTDialogBox.
// Se as actions tiverem correspondência com preset PSADT, usa o preset; senão YesNo.
func buildDialogButtons(actions []PsadtAlertAction) string {
	if len(actions) == 0 {
		return "Ok"
	}
	// Tenta mapear para preset nativo do PSADT baseado nos valores das actions.
	values := make([]string, 0, len(actions))
	for _, a := range actions {
		values = append(values, strings.ToLower(strings.TrimSpace(a.Value)))
	}
	key := strings.Join(values, "_")
	switch key {
	case "yes_no":
		return "YesNo"
	case "ok_cancel":
		return "OkCancel"
	case "yes_no_cancel":
		return "YesNoCancel"
	case "retry_cancel":
		return "RetryCancel"
	case "ok":
		return "Ok"
	default:
		if len(actions) >= 2 {
			return "YesNo"
		}
		return "Ok"
	}
}

// mapDefaultAction converte o defaultAction do payload para First/Second/Third do PSADT.
func mapDefaultAction(defaultAction string, actions []PsadtAlertAction) string {
	d := strings.ToLower(strings.TrimSpace(defaultAction))
	for i, a := range actions {
		if strings.ToLower(strings.TrimSpace(a.Value)) == d {
			switch i {
			case 0:
				return "First"
			case 1:
				return "Second"
			case 2:
				return "Third"
			}
		}
	}
	return "First"
}

// mapDialogOutput interpreta a saída stdout do script para o valor de ação do usuário.
// O script escreve o texto retornado pelo PSADT (ex: "Yes", "No", "shown", "timeout").
func mapDialogOutput(rawOutput string, p PsadtAlertPayload) string {
	line := strings.ToLower(strings.TrimSpace(rawOutput))

	// Toast sempre retorna "shown".
	if p.Type == "toast" {
		if strings.Contains(line, "shown") {
			return "shown"
		}
		return "shown"
	}

	// Para modal, mapeia a saída PSADT para o value da action correspondente.
	if strings.Contains(line, "timeout") {
		if strings.TrimSpace(p.DefaultAction) != "" {
			return strings.TrimSpace(p.DefaultAction)
		}
		return "timeout"
	}

	psadtToValue := map[string]string{
		"yes":    "yes",
		"no":     "no",
		"ok":     "ok",
		"cancel": "cancel",
		"retry":  "retry",
		"abort":  "abort",
		"ignore": "ignore",
	}
	for psadtKey, value := range psadtToValue {
		if strings.Contains(line, psadtKey) {
			// Prefere o value da action que faz match com o label/value.
			for _, a := range p.Actions {
				if strings.ToLower(strings.TrimSpace(a.Value)) == value ||
					strings.ToLower(strings.TrimSpace(a.Label)) == psadtKey {
					return a.Value
				}
			}
			return value
		}
	}

	// Fallback: retorna defaultAction se definido.
	if strings.TrimSpace(p.DefaultAction) != "" {
		return strings.TrimSpace(p.DefaultAction)
	}
	return "timeout"
}

// psEscape converte uma string Go para um literal string PowerShell entre aspas simples,
// escapando aspas simples internas.
func psEscape(s string) string {
	s = strings.ReplaceAll(s, "'", "''")
	return "'" + s + "'"
}

// handlePsadtAlert executa o alerta PSADT e retorna (exitCode, output JSON, errText).
func (a *App) handlePsadtAlert(ctx context.Context, p PsadtAlertPayload) (int, string, string) {
	if runtime.GOOS != "windows" {
		body, _ := json.Marshal(map[string]string{"action": "skipped_non_windows"})
		if a != nil {
			a.logs.append("[agent] psadt-alert ignorado: nao e windows type=" + p.Type + " alertId=" + p.AlertID)
		}
		return 0, string(body), ""
	}

	if a != nil {
		a.logs.append("[agent] psadt-alert iniciando type=" + p.Type + " alertId=" + p.AlertID)
	}

	script, timeout := buildPsadtAlertScript(p)

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	outBytes, err := cmd.CombinedOutput()
	rawOutput := strings.TrimSpace(string(outBytes))

	if err != nil {
		exitCode := 1
		if execCtx.Err() == context.DeadlineExceeded {
			// Timeout na execução do próprio script — trata como timeout do usuário.
			action := "timeout"
			if strings.TrimSpace(p.DefaultAction) != "" {
				action = strings.TrimSpace(p.DefaultAction)
			}
			body, _ := json.Marshal(map[string]string{"action": action})
			if a != nil {
				a.logs.append("[agent] psadt-alert timeout de execucao type=" + p.Type + " alertId=" + p.AlertID)
			}
			return 0, string(body), ""
		}
		errMsg := err.Error()
		if a != nil {
			a.logs.append(fmt.Sprintf("[agent] psadt-alert erro type=%s alertId=%s: %s | output: %s", p.Type, p.AlertID, errMsg, rawOutput))
		}
		return exitCode, "", errMsg
	}

	action := mapDialogOutput(rawOutput, p)
	body, _ := json.Marshal(map[string]string{"action": action})

	if a != nil {
		a.logs.append(fmt.Sprintf("[agent] psadt-alert concluido type=%s alertId=%s action=%s", p.Type, p.AlertID, action))
	}
	return 0, string(body), ""
}
