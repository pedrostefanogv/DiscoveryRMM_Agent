//go:build windows

package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"discovery/app/core/automation"
	"discovery/app/core/notifyui"
	"discovery/app/core/sysctrl"
)

// nativeAutomationWelcome exibe o prompt de confirmação nativo (substituto do
// Show-ADTInstallationWelcome) para automations "notificar usuario":
//
//	approved — usuario clicou em Instalar OU o contador expirou (ação padrão
//	           continuar, mesma semântica do PSADT);
//	deferred — usuario clicou em Adiar (ou fechou por Esc/X com adiamento
//	           permitido).
//
// handled=false indica indisponibilidade → chamador cai no caminho PSADT/toast.
func (a *App) nativeAutomationWelcome(req automation.AutomationNotificationRequest, timeoutSeconds int, appName string, closeProcs []string, runningProcs []notifyui.ProcessItem, allowDefer bool, message string) (automation.AutomationNotificationResponse, bool) {
	buttons := []notifyui.Button{}
	if allowDefer {
		buttons = append(buttons, notifyui.Button{ID: "defer", Label: "Adiar"})
	}
	buttons = append(buttons, notifyui.Button{ID: "install", Label: "Instalar", Kind: "primary"})

	cardReq := notifyui.CardRequest{
		Title:           "Instalando " + appName,
		ProcessList:     runningProcs,
		Subtitle:        "Instalação do Aplicativo",
		Message:         message,
		Buttons:         buttons,
		DefaultButtonID: "install",
		// Fim do contador = prosseguir (mesma semântica do Welcome do PSADT).
		TimeoutButtonID:  "install",
		CloseButtonID:    allowDeferID(allowDefer),
		CountdownSeconds: timeoutSeconds,
		CountdownLabel:   "A instalacao continuara automaticamente em:",
		Hint:             "Enter confirma · Esc adia",
	}
	cardCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds+30)*time.Second)
	defer cancel()

	// O Welcome roda no SERVIÇO (sessão 0): sem o worker de UI o cartão seria
	// invisível para o usuário (mesma lacuna já corrigida nos psadt-alerts).
	result, ok := runCardAsUser(cardCtx, cardReq, time.Duration(timeoutSeconds+30)*time.Second)
	if !ok || result.Outcome == notifyui.OutcomeError || result.Outcome == notifyui.OutcomeCanceled {
		reason := result.Reason
		if reason == "" {
			reason = result.Outcome
		}
		if a != nil {
			a.Logs.Append("[automation] cartão nativo Welcome indisponível (" + reason + ") — usando caminho PSADT/toast")
		}
		return automation.AutomationNotificationResponse{}, false
	}

	switch {
	case result.Outcome == notifyui.OutcomeTimeout ||
		(result.Outcome == notifyui.OutcomeClicked && result.ButtonID == "install"):
		// Mesma semântica do CloseProcesses do Welcome do PSADT: ao prosseguir
		// (Instalar ou fim do contador), os programas listados são encerrados
		// antes da tarefa executar. Best-effort: falha em um processo não
		// bloqueia a execução.
		a.closeProcessesByName(closeProcs)
		return automation.AutomationNotificationResponse{
			Accepted:    true,
			Result:      "approved",
			AgentAction: "native_welcome_continue",
			Message:     "usuario confirmou a execucao no prompt nativo",
		}, true
	default: // Adiar ou fechou
		return automation.AutomationNotificationResponse{
			Accepted:    true,
			Result:      "deferred",
			AgentAction: "native_welcome_defer",
			Message:     "usuario adiou a execucao no prompt nativo",
		}, true
	}
}

// closeProcessesByName encerra os processos da lista (case-insensitive, com ou
// sem sufixo .exe) antes de executar a tarefa. Falhas (permissão, processo
// protegido) são registradas no log e não interrompem o fluxo.
func (a *App) closeProcessesByName(names []string) {
	wanted := make(map[string]struct{}, len(names))
	for _, raw := range names {
		if key := normalizeProcessName(raw); key != "" {
			wanted[key] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return
	}

	procs, err := sysctrl.ListProcesses()
	if err != nil {
		if a != nil {
			a.Logs.Append(fmt.Sprintf("[automation] fechamento de processos falhou ao listar: %v", err))
		}
		return
	}

	closed, failed := 0, []string{}
	seen := make(map[uint32]struct{}, len(procs))
	for _, proc := range procs {
		if _, dup := seen[proc.PID]; dup {
			continue
		}
		seen[proc.PID] = struct{}{}
		if _, match := wanted[normalizeProcessName(proc.Name)]; !match {
			continue
		}
		if err := sysctrl.KillProcess(proc.PID); err != nil {
			failed = append(failed, fmt.Sprintf("%s(pid %d): %v", proc.Name, proc.PID, err))
			continue
		}
		closed++
	}

	if a != nil {
		logLine := fmt.Sprintf("[automation] fechamento de processos: %d encerrado(s)", closed)
		if len(failed) > 0 {
			logLine += fmt.Sprintf(", %d falha(s): %s", len(failed), strings.Join(failed, "; "))
		}
		a.Logs.Append(logLine)
	}
}

// normalizeProcessName padroniza um nome de processo/executável para
// comparação: minúsculas, sem espaços nas pontas e sem sufixo ".exe".
func normalizeProcessName(name string) string {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	return strings.TrimSuffix(trimmed, ".exe")
}

// allowDeferID define o desfecho do Esc/X: com adiamento permitido fecha =
// adiar; sem adiamento o Welcome do PSADT força a continuação.
func allowDeferID(allowDefer bool) string {
	if allowDefer {
		return "defer"
	}
	return "install"
}
// detectRunningCloseProcesses cruza os CloseProcesses configurados com o
// snapshot de processos em execução e devolve a lista do Welcome nativo
// (mesmo padrão do PSADT: só os que estão rodando, ícone + nome do exe).
func detectRunningCloseProcesses(names []string) []notifyui.ProcessItem {
	if len(names) == 0 {
		return nil
	}
	procs, err := sysctrl.ListProcesses()
	if err != nil {
		return nil
	}

	wanted := make(map[string]struct{}, len(names))
	for _, raw := range names {
		if key := normalizeProcessName(raw); key != "" {
			wanted[key] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	const maxRows = 5
	seen := make(map[string]struct{}, len(wanted))
	items := make([]notifyui.ProcessItem, 0, len(wanted))
	for _, proc := range procs {
		key := normalizeProcessName(proc.Name)
		if _, match := wanted[key]; !match {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		item := notifyui.ProcessItem{DisplayName: proc.Name}
		if path, err := sysctrl.ProcessImagePath(proc.PID); err == nil {
			item.ImagePath = path
		}
		items = append(items, item)
		if len(items) >= maxRows {
			break
		}
	}
	return items
}