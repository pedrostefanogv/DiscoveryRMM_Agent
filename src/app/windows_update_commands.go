package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"discovery/app/agentcommands"
	"discovery/app/core/windowsupdate"
	"discovery/app/inventory"
)

// IsWindowsUpdateCommandType verifica se cmdType é do gerenciador de patches.
// Aceita as variantes usadas pelo wire mapper (windowsupdate) e aliases
// históricos para facilitar testes.
func IsWindowsUpdateCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "windowsupdate", "windows_update", "windows-update":
		return true
	default:
		return false
	}
}

// windowsUpdateInstallPayload é o payload do comando de instalação.
type windowsUpdateInstallPayload struct {
	Updates     []windowsupdate.InstallTarget
	Force       bool
	PostAction  string
	TTLSeconds  int
	IssuedAtUTC time.Time
	HasIssuedAt bool
}

// handleWindowsUpdateCommand processa comandos do gerenciador de patches.
//
// Payload esperado (action-based):
//
//	{"action":"scan"}
//	{"action":"install","updates":[{"updateKey":"guid:...:1"}],"postAction":"none|restart|shutdown","ttlSeconds":1800,"issuedAtUtc":"..."}
//	{"action":"hide"|"unhide","updateKeys":["guid:...:1"]}
//	{"action":"policy","settings":{...}}
//	{"action":"policy-rollback","backup":{...}}
//
// O agente NUNCA instala por título: os alvos são revalidados contra o catálogo
// atual do WUA por GUID/chave canônica.
func (a *App) handleWindowsUpdateCommand(ctx context.Context, payload any) (bool, int, string, string) {
	startedAt := time.Now()
	// O handler era silencioso: um clique em "Verificar atualizacoes" no site
	// nao deixava rastro no log do agente. Agora entrada e saida sao logadas.
	if a != nil {
		a.Logs.Append(fmt.Sprintf("[windows-update] comando recebido: payload=%v", payload))
	}

	payloadJSON, err := agentcommands.NormalizePayloadJSON(payload)
	if err != nil {
		a.logWindowsUpdateCommandResult("invalid", 2, err.Error(), startedAt)
		return true, 2, "", "payload windowsupdate invalido: " + err.Error()
	}

	action := strings.ToLower(strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "action")))
	if action == "" {
		action = "scan"
	}

	if a == nil || a.InventorySvc == nil {
		a.logWindowsUpdateCommandResult(action, 1, "servico de inventario indisponivel", startedAt)
		return true, 1, "", "servico de inventario indisponivel"
	}

	a.Logs.Append("[windows-update] comando: action=" + action)

	switch action {
	case "scan":
		// O resultado (mesmo com erro) sobe o envelope com status/errorCode;
		// o codigo de saida reflete a falha para o servidor/UI.
		result, err := a.InventorySvc.ScanAndSyncWindowsUpdatesTriggered(ctx, "command")
		code := inventoryScanErrorCode(err)
		if err != nil {
			output := ""
			if result != nil {
				output = marshalWindowsUpdateResult(map[string]any{
					"success":        false,
					"errorCode":      code,
					"error":          err.Error(),
					"pendingCount":   result.PendingCount,
					"installedCount": result.InstalledCount,
					"skippedCount":   result.SkippedCount,
					"skippedReasons": result.SkippedReasons,
					"pendingReboot":  result.PendingReboot,
					"serviceState":   result.State.ServiceState,
				})
			}
			a.logWindowsUpdateCommandResult(action, 1, fmt.Sprintf("scan falhou (%s): %v", code, err), startedAt)
			return true, 1, output, fmt.Sprintf("scan de windows update falhou (%s): %v", code, err)
		}
		// O estado que acabou de ser enviado vira a referencia da sonda: evita
		// um scan duplicado por "mudanca" logo depois do comando.
		if result != nil {
			signature, stable := a.windowsUpdateSignaturesOfScan(ctx)
			a.InventorySvc.SetWindowsUpdateBaselineWithStable(signature, stable)
		}
		a.logWindowsUpdateCommandResult(action, 0, fmt.Sprintf("pending=%d installed=%d skipped=%d reboot=%t",
			result.PendingCount, result.InstalledCount, result.SkippedCount, result.PendingReboot.Pending), startedAt)
		return true, 0, marshalWindowsUpdateResult(map[string]any{
			"success":        true,
			"pendingCount":   result.PendingCount,
			"installedCount": result.InstalledCount,
			"skippedCount":   result.SkippedCount,
			"skippedReasons": result.SkippedReasons,
			"pendingReboot":  result.PendingReboot,
			"durationMs":     result.DurationMs,
			"state":          result.State,
		}), ""

	case "install":
		installPayload, perr := parseWindowsUpdateInstallPayload(payloadJSON)
		if perr != nil {
			return true, 2, "", perr.Error()
		}
		if expired := installPayload.expired(time.Now().UTC()); expired {
			return true, 3, "", "comando expirado; nenhuma acao executada"
		}
		result, installErr := a.InventorySvc.InstallWindowsUpdates(ctx, installPayload.Updates)
		if installErr != nil {
			// WU-06: motivo ESTRUTURADO no output (preflight disk_space,
			// service_stopped, on_battery, ou erro tipado do WUA). A API usa o
			// errorCode para classificar a falha em vez de adivinhar por texto.
			code := windowsupdate.ErrorCode(installErr)
			a.logWindowsUpdateCommandResult(action, 1, "instalacao falhou ("+code+"): "+installErr.Error(), startedAt)
			output := marshalWindowsUpdateResult(map[string]any{
				"success":   false,
				"errorCode": code,
				"error":     installErr.Error(),
			})
			return true, 1, output, "instalacao de windows update falhou (" + code + "): " + installErr.Error()
		}
		a.logWindowsUpdateCommandResult(action, result.Code(), fmt.Sprintf("installed=%d failed=%d notFound=%d rebootRequired=%t",
			len(result.Installed), len(result.Failed), len(result.NotFound), result.RebootRequired), startedAt)

		// Atualiza o inventário local/servidor em segundo plano: o resultado do
		// comando precisa sair antes (a UI faz polling curto do snapshot).
		go a.refreshWindowsUpdatesAfterInstall()

		exitCode := result.Code()
		output := marshalWindowsUpdateResult(map[string]any{
			"success":        exitCode == 0,
			"installed":      result.Installed,
			"failed":         result.Failed,
			"notFound":       result.NotFound,
			"rebootRequired": result.RebootRequired,
			"durationMs":     result.DurationMs,
			"postAction":     installPayload.PostAction,
		})

		// Ação de energia pós-instalação: agenda com atraso curto para o result
		// chegar ao servidor antes do reboot (o reboot derruba o agente). O
		// payload já vem com TTL validado; force nunca é implícito.
		//
		// SEGURANÇA: só reinicia/desliga quando TODAS as atualizações do lote
		// instalaram. Falha parcial ou update ausente do catálogo NÃO pode
		// derrubar a máquina (o operador decide o próximo passo).
		canPowerOff := len(result.Installed) > 0 && len(result.Failed) == 0 && len(result.NotFound) == 0
		if installPayload.PostAction == "restart" || installPayload.PostAction == "shutdown" {
			if canPowerOff {
				a.Logs.Append(fmt.Sprintf("[windows-update] %s agendado apos instalar %d update(s)",
					installPayload.PostAction, len(result.Installed)))
				a.scheduleSilentPowerAction(installPayload.PostAction, powerCommandPayload{
					DelaySeconds: 90,
					Force:        installPayload.Force,
					Message:      "Atualizacoes do Windows instaladas. O computador sera reiniciado.",
				})
			} else {
				a.Logs.Append(fmt.Sprintf("[windows-update] acao de energia NAO agendada: installed=%d failed=%d notFound=%d",
					len(result.Installed), len(result.Failed), len(result.NotFound)))
			}
		}
		return true, exitCode, output, ""

	case "install-third-party":
		// WU-19/WU-20: upgrade de apps de terceiros (choco/winget). Mesmo
		// contrato de resultado do WUA para o servidor reutilizar o parser:
		// installed[]/failed[] com updateKey canonico (choco:<id>).
		tpSource, tpRequests, tpErr := parseThirdPartyInstallPayload(payloadJSON)
		if tpErr != nil {
			return true, 2, "", tpErr.Error()
		}
		outcomes, installErr := a.InventorySvc.InstallThirdParty(ctx, tpSource, tpRequests)
		if installErr != nil {
			code := "third_party_unavailable"
			a.logWindowsUpdateCommandResult(action, 1, installErr.Error(), startedAt)
			return true, 1, marshalWindowsUpdateResult(map[string]any{
				"success":   false,
				"errorCode": code,
				"error":     installErr.Error(),
			}), installErr.Error()
		}

		installed := make([]map[string]any, 0, len(outcomes))
		failed := make([]map[string]any, 0, 4)
		for _, outcome := range outcomes {
			entry := map[string]any{"updateKey": outcome.UpdateKey}
			if outcome.Error != "" {
				entry["error"] = outcome.Error
				failed = append(failed, entry)
				continue
			}
			installed = append(installed, entry)
		}
		exitCode := 0
		if len(failed) > 0 {
			exitCode = 1
		}
		a.logWindowsUpdateCommandResult(action, exitCode,
			fmt.Sprintf("source=%s installed=%d failed=%d", tpSource, len(installed), len(failed)), startedAt)
		// O inventario de apps muda: reenvia em background como no WUA.
		go a.refreshWindowsUpdatesAfterInstall()
		return true, exitCode, marshalWindowsUpdateResult(map[string]any{
			"success":        exitCode == 0,
			"installed":      installed,
			"failed":         failed,
			"rebootRequired": false,
			"source":         tpSource,
		}), ""

	case "hide", "unhide":
		keys := stringSliceField(payloadJSON, "updateKeys")
		if len(keys) == 0 {
			a.logWindowsUpdateCommandResult(action, 2, "campo 'updateKeys' ausente", startedAt)
			return true, 2, "", "campo 'updateKeys' e obrigatorio"
		}
		hidden, err := a.InventorySvc.HideWindowsUpdates(ctx, keys, action == "hide")
		if err != nil {
			a.logWindowsUpdateCommandResult(action, 1, err.Error(), startedAt)
			return true, 1, "", "falha ao " + action + " updates: " + err.Error()
		}
		a.logWindowsUpdateCommandResult(action, 0, fmt.Sprintf("changed=%d", hidden), startedAt)
		return true, 0, marshalWindowsUpdateResult(map[string]any{
			"success": true,
			"changed": hidden,
			"action":  action,
		}), ""

	case "policy":
		settings, perr := parseWindowsUpdatePolicySettings(payloadJSON)
		if perr != nil {
			return true, 2, "", perr.Error()
		}
		backup, applyErr := a.InventorySvc.ApplyWindowsUpdatePolicy(settings)
		if applyErr != nil {
			return true, 1, "", "falha ao aplicar politica: " + applyErr.Error()
		}
		return true, 0, marshalWindowsUpdateResult(map[string]any{
			"success": true,
			"backup":  backup,
		}), ""

	case "policy-rollback":
		backup, perr := parseWindowsUpdatePolicyBackup(payloadJSON)
		if perr != nil {
			return true, 2, "", perr.Error()
		}
		if restoreErr := a.InventorySvc.RestoreWindowsUpdatePolicy(*backup); restoreErr != nil {
			return true, 1, "", "falha ao restaurar politica: " + restoreErr.Error()
		}
		return true, 0, marshalWindowsUpdateResult(map[string]any{"success": true, "restored": true}), ""

	case "policy-read":
		settings, readErr := a.InventorySvc.ReadWindowsUpdatePolicy()
		if readErr != nil {
			return true, 1, "", "falha ao ler politica: " + readErr.Error()
		}
		return true, 0, marshalWindowsUpdateResult(map[string]any{"success": true, "settings": settings, "probe": a.InventorySvc.WindowsUpdateProbe(ctx)}), ""

	default:
		a.logWindowsUpdateCommandResult(action, 2, "acao nao suportada", startedAt)
		return true, 2, "", "acao de windows update nao suportada: " + action
	}
}

// logWindowsUpdateCommandResult registra a saida de um comando windowsupdate
// (a UI depende de saber que o comando chegou e o que aconteceu).
func (a *App) logWindowsUpdateCommandResult(action string, code int, detail string, startedAt time.Time) {
	if a == nil {
		return
	}
	elapsed := time.Since(startedAt).Milliseconds()
	line := fmt.Sprintf("[windows-update] comando concluido: action=%s exitCode=%d (%dms)", action, code, elapsed)
	if strings.TrimSpace(detail) != "" {
		line += " — " + detail
	}
	a.Logs.Append(line)
}

// refreshWindowsUpdatesAfterInstall re-coleta e reenvia o inventário depois de
// uma instalação (o estado das KBs mudou).
func (a *App) refreshWindowsUpdatesAfterInstall() {
	if a == nil || a.InventorySvc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if _, err := a.InventorySvc.ScanAndSyncWindowsUpdatesTriggered(ctx, "install"); err != nil {
		a.Logs.Append("[windows-update] refresh pos-instalacao falhou: " + err.Error())
		return
	}
	signature, stable := a.windowsUpdateSignaturesOfScan(ctx)
	a.InventorySvc.SetWindowsUpdateBaselineWithStable(signature, stable)
}

// parseThirdPartyInstallPayload valida a origem e os ids do comando
// install-third-party. Os ids sao validados de novo no provider (allowlist) —
// aqui so barramos lixo obvio e limite de lote.
func parseThirdPartyInstallPayload(payloadJSON map[string]any) (string, []inventory.ThirdPartyInstallRequest, error) {
	source := strings.ToLower(strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "source")))
	switch source {
	case "choco", "winget":
	default:
		return "", nil, fmt.Errorf("origem de terceiros invalida: %q", source)
	}

	rawApps, ok := payloadJSON["apps"]
	if !ok {
		return "", nil, fmt.Errorf("campo 'apps' e obrigatorio")
	}
	items, ok := rawApps.([]any)
	if !ok || len(items) == 0 {
		return "", nil, fmt.Errorf("campo 'apps' deve ser uma lista nao vazia")
	}
	if len(items) > 100 {
		return "", nil, fmt.Errorf("lote de apps excede o limite de 100")
	}

	requests := make([]inventory.ThirdPartyInstallRequest, 0, len(items))
	for _, item := range items {
		switch value := item.(type) {
		case string:
			if id := strings.TrimSpace(value); id != "" {
				requests = append(requests, inventory.ThirdPartyInstallRequest{ID: id})
			}
		case map[string]any:
			id := strings.TrimSpace(agentcommands.GetStringField(value, "id"))
			if id == "" {
				continue
			}
			requests = append(requests, inventory.ThirdPartyInstallRequest{
				ID: id,
				// Campos do catálogo curado (WU-21): quando presentes, o agente
				// baixa do fornecedor e valida o SHA-256 antes de executar.
				InstallerURL:  strings.TrimSpace(agentcommands.GetStringField(value, "installerUrl")),
				SHA256:        strings.TrimSpace(agentcommands.GetStringField(value, "sha256")),
				SilentArgs:    strings.TrimSpace(agentcommands.GetStringField(value, "silentArgs")),
				InstallerType: strings.TrimSpace(agentcommands.GetStringField(value, "installerType")),
			})
		}
	}
	if len(requests) == 0 {
		return "", nil, fmt.Errorf("nenhum app valido no payload")
	}
	return source, requests, nil
}

func parseWindowsUpdateInstallPayload(payloadJSON map[string]any) (windowsUpdateInstallPayload, error) {
	result := windowsUpdateInstallPayload{
		PostAction: "none",
		TTLSeconds: 1800,
	}

	rawUpdates, ok := payloadJSON["updates"]
	if !ok {
		return result, fmt.Errorf("campo 'updates' e obrigatorio")
	}
	items, ok := rawUpdates.([]any)
	if !ok {
		return result, fmt.Errorf("campo 'updates' deve ser uma lista")
	}
	if len(items) == 0 {
		return result, fmt.Errorf("campo 'updates' nao pode ser vazio")
	}
	if len(items) > windowsupdate.MaxUpdatesPerCommand {
		return result, fmt.Errorf("lote de updates excede o limite de %d", windowsupdate.MaxUpdatesPerCommand)
	}
	targets := make([]windowsupdate.InstallTarget, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		target := windowsupdate.InstallTarget{
			UpdateKey: strings.TrimSpace(stringField(entry, "updateKey")),
			UpdateID:  strings.TrimSpace(stringField(entry, "updateId")),
			Revision:  intField(entry, "revision"),
			KBIDs:     stringSliceField(entry, "kbIds"),
		}
		if !windowsupdate.ValidUpdateKey(target.UpdateKey) {
			return result, fmt.Errorf("updateKey invalida: %q", target.UpdateKey)
		}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return result, fmt.Errorf("nenhum update valido no payload")
	}
	result.Updates = targets

	result.Force = agentcommands.GetBoolField(payloadJSON, "force")

	if postAction := strings.ToLower(strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "postAction"))); postAction != "" {
		switch postAction {
		case "none", "restart", "shutdown":
			result.PostAction = postAction
		default:
			return result, fmt.Errorf("postAction invalido: %q", postAction)
		}
	}

	if ttl := intField(payloadJSON, "ttlSeconds"); ttl > 0 {
		if ttl > 7200 {
			ttl = 7200
		}
		if ttl < 60 {
			ttl = 60
		}
		result.TTLSeconds = ttl
	}
	if issued := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "issuedAtUtc")); issued != "" {
		if parsed, err := time.Parse(time.RFC3339, issued); err == nil {
			result.IssuedAtUTC = parsed.UTC()
			result.HasIssuedAt = true
		}
	}
	return result, nil
}

func (p windowsUpdateInstallPayload) expired(now time.Time) bool {
	if !p.HasIssuedAt || p.TTLSeconds <= 0 {
		return false
	}
	return now.After(p.IssuedAtUTC.Add(time.Duration(p.TTLSeconds) * time.Second))
}

func parseWindowsUpdatePolicySettings(payloadJSON map[string]any) (windowsupdate.PolicySettings, error) {
	settings := windowsupdate.PolicySettings{}
	rawSettings, ok := payloadJSON["settings"].(map[string]any)
	if !ok {
		return settings, fmt.Errorf("campo 'settings' e obrigatorio")
	}
	settings.NoAutoUpdate = uint32PtrField(rawSettings, "noAutoUpdate")
	settings.AUOptions = uint32PtrField(rawSettings, "auOptions")
	settings.DeferQualityUpdates = uint32PtrField(rawSettings, "deferQualityUpdates")
	settings.DeferQualityUpdatesPeriodInDays = uint32PtrField(rawSettings, "deferQualityUpdatesPeriodInDays")
	settings.PauseQualityUpdates = uint32PtrField(rawSettings, "pauseQualityUpdates")
	if value := strings.TrimSpace(stringField(rawSettings, "pauseQualityUpdatesStartTime")); value != "" {
		settings.PauseQualityUpdatesStartTime = &value
	}
	return settings, nil
}

func parseWindowsUpdatePolicyBackup(payloadJSON map[string]any) (*windowsupdate.PolicyBackup, error) {
	rawBackup, ok := payloadJSON["backup"]
	if !ok {
		return nil, fmt.Errorf("campo 'backup' e obrigatorio")
	}
	encoded, err := json.Marshal(rawBackup)
	if err != nil {
		return nil, err
	}
	var backup windowsupdate.PolicyBackup
	if err := json.Unmarshal(encoded, &backup); err != nil {
		return nil, fmt.Errorf("backup invalido: %w", err)
	}
	return &backup, nil
}

func inventoryScanErrorCode(err error) string {
	switch {
	case err == nil:
		return windowsupdate.ScanErrorNone
	case errors.Is(err, windowsupdate.ErrUnsupportedOS):
		return windowsupdate.ScanErrorUnsupported
	case errors.Is(err, windowsupdate.ErrWuaUnavailable):
		return windowsupdate.ScanErrorWuaUnavailable
	case errors.Is(err, windowsupdate.ErrServiceDisabled):
		return windowsupdate.ScanErrorServiceDisabled
	case errors.Is(err, windowsupdate.ErrTimeout):
		return windowsupdate.ScanErrorTimeout
	default:
		return windowsupdate.ScanErrorFailed
	}
}

func marshalWindowsUpdateResult(value map[string]any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{\"success\":false}"
	}
	return string(encoded)
}

// ---- payload helpers -------------------------------------------------------

func stringField(m map[string]any, key string) string {
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}

func uint32PtrField(m map[string]any, key string) *uint32 {
	if _, ok := m[key]; !ok {
		return nil
	}
	value := intField(m, key)
	if value < 0 {
		return nil
	}
	casted := uint32(value)
	return &casted
}

func stringSliceField(m map[string]any, key string) []string {
	value, ok := m[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}
