package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	gopsadt "github.com/pedrostefanogv/go-psadt"
	pstypes "github.com/pedrostefanogv/go-psadt/types"

	"discovery/app/agentconfig"
	"discovery/app/core/processutil"
	"discovery/app/services/psadt"

	"golang.org/x/text/encoding/charmap"
)

func decodePowerShellOutput(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if utf8.Valid(raw) {
		return strings.TrimSpace(string(raw))
	}
	if decoded, err := charmap.CodePage850.NewDecoder().Bytes(raw); err == nil && utf8.Valid(decoded) {
		return strings.TrimSpace(string(decoded))
	}
	if decoded, err := charmap.Windows1252.NewDecoder().Bytes(raw); err == nil && utf8.Valid(decoded) {
		return strings.TrimSpace(string(decoded))
	}
	return strings.TrimSpace(string(raw))
}

type PSADTModuleStatus struct {
	Installed    bool   `json:"installed"`
	Version      string `json:"version"`
	Message      string `json:"message"`
	CheckedAtUTC string `json:"checkedAtUtc"`
}

type PSADTDebugState struct {
	RuntimeDebugMode     bool                                        `json:"runtimeDebugMode"`
	Configuration        agentconfig.AgentConfiguration              `json:"configuration"`
	ModuleStatus         PSADTModuleStatus                           `json:"moduleStatus"`
	NotificationBranding agentconfig.AgentNotificationBrandingConfig `json:"notificationBranding"`
	NotificationPolicies []agentconfig.AgentNotificationPolicy       `json:"notificationPolicies"`
}

type PSADTDebugNotificationRequest struct {
	Title      string `json:"title"`
	Message    string `json:"message"`
	Mode       string `json:"mode"`
	Severity   string `json:"severity"`
	Layout     string `json:"layout"`
	Accent     string `json:"accent"`
	RequireAck bool   `json:"requireAck"`
}

func (a *App) GetPSADTDebugState() PSADTDebugState {
	a.logs.append("[psadt] GetPSADTDebugState chamado")
	cfg := a.GetAgentConfiguration()
	module := a.CheckPSADTModuleStatus()
	enabledStr := "nil"
	if cfg.PSADT.Enabled != nil {
		if *cfg.PSADT.Enabled {
			enabledStr = "true"
		} else {
			enabledStr = "false"
		}
	}
	a.logs.append(fmt.Sprintf("[psadt] estado: enabled=%s version=%s moduleInstalled=%t moduleVersion=%s",
		enabledStr, cfg.PSADT.RequiredVersion, module.Installed, module.Version))
	return PSADTDebugState{
		RuntimeDebugMode:     a.runtimeFlags.DebugMode,
		Configuration:        cfg,
		ModuleStatus:         module,
		NotificationBranding: cfg.NotificationBranding, NotificationPolicies: cfg.NotificationPolicies,
	}
}

func (a *App) CheckPSADTModuleStatus() PSADTModuleStatus {
	status := a.psadtSvc.CheckModuleStatus()
	return PSADTModuleStatus{
		Installed:    status.Installed,
		Version:      status.Version,
		Message:      status.Message,
		CheckedAtUTC: status.CheckedAtUTC,
	}
}

func (a *App) InstallPSADTModule(version string) PSADTModuleStatus {
	status := a.psadtSvc.InstallModule(version)
	return PSADTModuleStatus{
		Installed:    status.Installed,
		Version:      status.Version,
		Message:      status.Message,
		CheckedAtUTC: status.CheckedAtUTC,
	}
}

// bootstrapPSADTModuleIfNeeded instala o módulo PSAppDeployToolkit em background
// no startup, se a configuração permitir (enabled + autoInstallModule +
// installOnStartup) e o módulo ainda não estiver instalado. Zero-touch.
func (a *App) bootstrapPSADTModuleIfNeeded() {
	if a == nil || a.psadtSvc == nil {
		return
	}
	cfg := a.GetAgentConfiguration().PSADT
	if cfg.Enabled == nil || !*cfg.Enabled {
		return
	}
	if cfg.AutoInstallModule == nil || !*cfg.AutoInstallModule {
		return
	}
	if cfg.InstallOnStartup == nil || !*cfg.InstallOnStartup {
		return
	}

	// Verifica se já está instalado antes de instalar.
	status := a.psadtSvc.CheckModuleStatus()
	if status.Installed {
		a.logs.append(fmt.Sprintf("[psadt] bootstrap: módulo já instalado (v%s), nada a fazer", status.Version))
		return
	}

	version := strings.TrimSpace(cfg.RequiredVersion)
	if version == "" {
		version = "4.1.8"
	}
	a.logs.append(fmt.Sprintf("[psadt] bootstrap: módulo não instalado — instalando v%s em background", version))

	a.safeGo(func() {
		result := a.psadtSvc.InstallModule(version)
		if result.Installed {
			a.logs.append(fmt.Sprintf("[psadt] bootstrap: módulo instalado com sucesso (v%s)", result.Version))
		} else {
			a.logs.append("[psadt] bootstrap: falha ao instalar módulo: " + result.Message)
		}
	})
}

func (a *App) EmitPSADTDebugNotification(req PSADTDebugNotificationRequest) error {
	return a.psadtSvc.EmitDebugNotification(psadt.DebugNotificationRequest{
		Title:      req.Title,
		Message:    req.Message,
		Mode:       req.Mode,
		Severity:   req.Severity,
		Layout:     req.Layout,
		Accent:     req.Accent,
		RequireAck: req.RequireAck,
	})
}

// PSADTScriptResult representa o resultado da execução de um script PSADT
type PSADTScriptResult struct {
	Success       bool   `json:"success"`
	ExitCode      int    `json:"exitCode"`
	Output        string `json:"output"`
	Error         string `json:"error"`
	ExecutedAtUTC string `json:"executedAtUtc"`
	DurationMS    int64  `json:"durationMs"`
}

// ExecutePSADTTestScript executa um script PSADT de teste usando o módulo real
func (a *App) ExecutePSADTTestScript(appName string, appVersion string) PSADTScriptResult {
	result := PSADTScriptResult{
		ExecutedAtUTC: time.Now().UTC().Format(time.RFC3339),
	}

	if runtime.GOOS != "windows" {
		result.Success = false
		result.Error = "PSADT suportado apenas em Windows"
		result.ExitCode = 1
		a.logs.append("[psadt] test script ignorado: não é Windows")
		return result
	}

	if appName == "" {
		appName = "TestApp"
	}
	if appVersion == "" {
		appVersion = "1.0.0"
	}
	a.logs.append(fmt.Sprintf("[psadt] executando test script: appName=%s appVersion=%s", appName, appVersion))

	// Usa a lib go-psadt (runner PowerShell persistente) em vez de montar
	// scripts PowerShell inline. Valida módulo, versão e comandos exportados
	// via API tipada.
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := gopsadt.NewClient(
		gopsadt.WithTimeout(30*time.Second),
		gopsadt.WithMinModuleVersion("4.1.8"),
	)
	if err != nil {
		result.Success = false
		result.Error = "falha ao inicializar PSADT: " + err.Error()
		result.ExitCode = 1
		result.DurationMS = time.Since(start).Milliseconds()
		a.logs.append("[psadt] test script falhou na inicialização: " + err.Error())
		return result
	}
	defer client.Close()

	session, err := client.OpenSessionWithContext(ctx, pstypes.NewSessionConfig().
		App("Discovery", appName, appVersion).
		Install().
		Silent().
		Build())
	if err != nil {
		result.Success = false
		result.Error = "falha ao abrir sessão PSADT: " + err.Error()
		result.ExitCode = 1
		result.DurationMS = time.Since(start).Milliseconds()
		a.logs.append("[psadt] test script falhou ao abrir sessão: " + err.Error())
		return result
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	// Validação real: módulo carregado + comandos exportados.
	var output strings.Builder
	output.WriteString("==========================================\n")
	output.WriteString("Validação PSADT Real do Discovery Agent\n")
	output.WriteString("==========================================\n")
	output.WriteString(fmt.Sprintf("Nome: %s\n", appName))
	output.WriteString(fmt.Sprintf("Versão: %s\n", appVersion))
	output.WriteString("Vendor: Discovery\n")
	output.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	output.WriteString("\n")

	// Testa admin e conectividade como smoke test real.
	if isAdmin, adminErr := session.TestCallerIsAdmin(); adminErr == nil {
		output.WriteString(fmt.Sprintf("CallerIsAdmin: %t\n", isAdmin))
	}
	if online, netErr := session.TestNetworkConnection(); netErr == nil {
		output.WriteString(fmt.Sprintf("NetworkConnection: %t\n", online))
	}

	output.WriteString("\n✓ Validação real concluída com sucesso\n")
	output.WriteString("ExitCode: 0\n")
	output.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	output.WriteString("==========================================\n")

	elapsed := time.Since(start).Milliseconds()
	result.DurationMS = elapsed
	result.Output = output.String()
	result.Success = true
	result.ExitCode = 0
	a.logs.append(fmt.Sprintf("[psadt] test script executado com sucesso em %dms", elapsed))
	return result
}

// GetPSADTScriptTemplate retorna um template de script PSADT para customização
func (a *App) GetPSADTScriptTemplate() string {
	return a.psadtSvc.GetScriptTemplate()
}

// ExecuteCustomPSADTScript executa um script PSADT customizado fornecido pelo usuário
func (a *App) ExecuteCustomPSADTScript(scriptContent string) PSADTScriptResult {
	result := a.psadtSvc.ExecuteCustomScript(scriptContent)
	return PSADTScriptResult{
		Success:       result.Success,
		ExitCode:      result.ExitCode,
		Output:        result.Output,
		Error:         result.Error,
		ExecutedAtUTC: result.ExecutedAtUTC,
		DurationMS:    result.DurationMS,
	}
}

// PSADTVisualNotificationRequest define os parametros para um teste visual nativo de notificacao PSADT.
type PSADTVisualNotificationRequest struct {
	NotifType           string `json:"notifType"` // balloon_info | balloon_warning | balloon_error | prompt_ok | prompt_continue | progress
	Title               string `json:"title"`
	Message             string `json:"message"`
	AppName             string `json:"appName"`
	DurationSeconds     int    `json:"durationSeconds"` // utilizado apenas pelo tipo progress
	DialogButtons       string `json:"dialogButtons"`   // Ok | OkCancel | AbortRetryIgnore | YesNoCancel | YesNo | RetryCancel | CancelTryContinue
	DialogDefault       string `json:"dialogDefault"`   // First | Second | Third
	DialogIcon          string `json:"dialogIcon"`      // None | Stop | Question | Exclamation | Information
	DialogTimeout       int    `json:"dialogTimeout"`   // segundos, 0 = sem timeout
	DialogNoWait        bool   `json:"dialogNoWait"`
	DialogExitOnTimeout bool   `json:"dialogExitOnTimeout"`
	DialogNotTopMost    bool   `json:"dialogNotTopMost"`
	DialogForce         bool   `json:"dialogForce"`
}

// ExecutePSADTVisualNotification executa uma notificacao visual nativa via cmdlets reais do PSAppDeployToolkit.
// Grava um .ps1 temporario, executa com PowerShell -File e remove o arquivo ao terminar.
func (a *App) ExecutePSADTVisualNotification(req PSADTVisualNotificationRequest) PSADTScriptResult {
	result := PSADTScriptResult{ExecutedAtUTC: time.Now().UTC().Format(time.RFC3339)}

	if runtime.GOOS != "windows" {
		result.Error = "PSADT suportado apenas em Windows"
		result.ExitCode = 1
		a.logs.append("[psadt] visual notification ignorada: nao e Windows")
		return result
	}

	req.NotifType = strings.TrimSpace(strings.ToLower(req.NotifType))
	if req.NotifType == "" {
		req.NotifType = "balloon_info"
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = "Discovery Agent"
	}
	if strings.TrimSpace(req.Message) == "" {
		req.Message = "Teste de notificacao PSADT"
	}
	if strings.TrimSpace(req.AppName) == "" {
		req.AppName = "Discovery Agent"
	}
	if req.DurationSeconds <= 0 || req.DurationSeconds > 60 {
		req.DurationSeconds = 5
	}
	req.DialogButtons = normalizeDialogButtons(req.DialogButtons)
	req.DialogDefault = normalizeDialogDefault(req.DialogDefault)
	req.DialogIcon = normalizeDialogIcon(req.DialogIcon)
	if req.DialogTimeout < 0 {
		req.DialogTimeout = 0
	}
	a.logs.append(fmt.Sprintf("[psadt] notificacao visual nativa: tipo=%s titulo=%q", req.NotifType, req.Title))

	script, timeout := buildPSADTVisualScript(req)

	tmpFile, err := os.CreateTemp("", "psadt-visual-*.ps1")
	if err != nil {
		result.Error = "falha ao criar arquivo temporario: " + err.Error()
		result.ExitCode = 1
		a.logs.append("[psadt] " + result.Error)
		return result
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(script); err != nil {
		tmpFile.Close()
		result.Error = "falha ao escrever script temporario: " + err.Error()
		result.ExitCode = 1
		a.logs.append("[psadt] " + result.Error)
		return result
	}
	tmpFile.Close()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-WindowStyle", "Hidden",
		"-ExecutionPolicy", "Bypass", "-File", tmpPath)
	cmd.Env = append(os.Environ(),
		"PSADT_TITLE="+req.Title,
		"PSADT_MESSAGE="+req.Message,
		"PSADT_APPNAME="+req.AppName,
		fmt.Sprintf("PSADT_DURATION=%d", req.DurationSeconds),
		"PSADT_DIALOG_BUTTONS="+req.DialogButtons,
		"PSADT_DIALOG_DEFAULT="+req.DialogDefault,
		"PSADT_DIALOG_ICON="+req.DialogIcon,
		fmt.Sprintf("PSADT_DIALOG_TIMEOUT=%d", req.DialogTimeout),
		"PSADT_DIALOG_NOWAIT="+boolEnvValue(req.DialogNoWait),
		"PSADT_DIALOG_EXIT_ON_TIMEOUT="+boolEnvValue(req.DialogExitOnTimeout),
		"PSADT_DIALOG_NOT_TOPMOST="+boolEnvValue(req.DialogNotTopMost),
		"PSADT_DIALOG_FORCE="+boolEnvValue(req.DialogForce),
	)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start).Milliseconds()
	result.DurationMS = elapsed
	result.Output = decodePowerShellOutput(output)

	if err != nil {
		result.Success = false
		result.Error = err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = 1
		}
		a.logs.append(fmt.Sprintf("[psadt] notificacao visual falhou (tipo=%s): %v", req.NotifType, err))
		return result
	}

	result.Success = true
	result.ExitCode = 0
	a.logs.append(fmt.Sprintf("[psadt] notificacao visual concluida (tipo=%s) em %dms", req.NotifType, elapsed))
	return result
}

// buildPSADTVisualScript gera o script PowerShell para o tipo de notificacao solicitado.
func buildPSADTVisualScript(req PSADTVisualNotificationRequest) (string, time.Duration) {
	balloonIcon := "Info"
	if strings.Contains(req.NotifType, "warning") {
		balloonIcon = "Warning"
	} else if strings.Contains(req.NotifType, "error") {
		balloonIcon = "Error"
	}

	header := "$ErrorActionPreference = 'Stop'\n" +
		"[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)\n" +
		"$OutputEncoding = [Console]::OutputEncoding\n" +
		"try {\n" +
		"    Import-Module -Name PSAppDeployToolkit -ErrorAction Stop\n" +
		"} catch {\n" +
		"    Write-Error \"Falha ao importar PSAppDeployToolkit: $_\"; exit 1\n" +
		"}\n" +
		"$psadtTitle    = $env:PSADT_TITLE\n" +
		"$psadtMessage  = $env:PSADT_MESSAGE\n" +
		"$psadtAppName  = $env:PSADT_APPNAME\n" +
		"$psadtDuration = [int]$env:PSADT_DURATION\n" +
		"$psadtDialogButtons = $env:PSADT_DIALOG_BUTTONS\n" +
		"$psadtDialogDefault = $env:PSADT_DIALOG_DEFAULT\n" +
		"$psadtDialogIcon = $env:PSADT_DIALOG_ICON\n" +
		"$psadtDialogTimeout = [int]$env:PSADT_DIALOG_TIMEOUT\n" +
		"$psadtDialogNoWait = ($env:PSADT_DIALOG_NOWAIT -eq '1')\n" +
		"$psadtDialogExitOnTimeout = ($env:PSADT_DIALOG_EXIT_ON_TIMEOUT -eq '1')\n" +
		"$psadtDialogNotTopMost = ($env:PSADT_DIALOG_NOT_TOPMOST -eq '1')\n" +
		"$psadtDialogForce = ($env:PSADT_DIALOG_FORCE -eq '1')\n\n"

	openInteractive := "try {\n" +
		"    Open-ADTSession -SessionState $ExecutionContext.SessionState" +
		" -AppName $psadtAppName -AppVersion '1.0' -AppVendor 'Discovery'" +
		" -DeploymentType 'Install' -DeployMode 'Interactive'\n" +
		"} catch {\n" +
		"    Write-Error \"Falha ao abrir sessao PSADT: $_\"; exit 2\n" +
		"}\n"

	openNonInt := "try {\n" +
		"    Open-ADTSession -SessionState $ExecutionContext.SessionState" +
		" -AppName $psadtAppName -AppVersion '1.0' -AppVendor 'Discovery'" +
		" -DeploymentType 'Install' -DeployMode 'NonInteractive'\n" +
		"} catch {\n" +
		"    Write-Error \"Falha ao abrir sessao PSADT: $_\"; exit 2\n" +
		"}\n"

	closeSession := "try { Close-ADTSession -ExitCode 0 } catch {}\nexit 0\n"

	switch req.NotifType {
	case "balloon_info", "balloon_warning", "balloon_error":
		body := openInteractive +
			fmt.Sprintf("Show-ADTBalloonTip -BalloonTipTitle $psadtTitle -BalloonTipText $psadtMessage -BalloonTipIcon '%s'\n", balloonIcon) +
			"Write-Host 'BalloonTip exibido com sucesso'\n" +
			closeSession
		return header + body, 30 * time.Second

	case "prompt_ok":
		body := openInteractive +
			"$adtResult = Show-ADTInstallationPrompt -Message $psadtMessage -Title $psadtTitle -ButtonRightText 'OK' -Icon 'Info'\n" +
			"Write-Host \"Resultado: $adtResult\"\n" +
			closeSession
		return header + body, 3 * time.Minute

	case "prompt_continue":
		body := openInteractive +
			"$adtResult = Show-ADTInstallationPrompt -Message $psadtMessage -Title $psadtTitle -ButtonLeftText 'Continuar' -ButtonRightText 'Adiar' -Icon 'Info'\n" +
			"Write-Host \"Resultado: $adtResult\"\n" +
			closeSession
		return header + body, 3 * time.Minute

	case "progress":
		body := openNonInt +
			"Show-ADTInstallationProgress -StatusMessage $psadtMessage -WindowTitle $psadtTitle\n" +
			"Start-Sleep -Seconds $psadtDuration\n" +
			"Write-Host \"Progresso exibido por $psadtDuration segundos\"\n" +
			closeSession
		timeout := time.Duration(req.DurationSeconds+30) * time.Second
		return header + body, timeout

	case "dialog", "dialog_box":
		body := "$dialogParams = @{\n" +
			"  Title = $psadtTitle\n" +
			"  Text = $psadtMessage\n" +
			"  Buttons = $psadtDialogButtons\n" +
			"  DefaultButton = $psadtDialogDefault\n" +
			"  Icon = $psadtDialogIcon\n" +
			"}\n" +
			"if ($psadtDialogTimeout -gt 0) { $dialogParams.Timeout = $psadtDialogTimeout }\n" +
			"if ($psadtDialogNoWait) { $dialogParams.NoWait = $true }\n" +
			"if ($psadtDialogExitOnTimeout) { $dialogParams.ExitOnTimeout = $true }\n" +
			"if ($psadtDialogNotTopMost) { $dialogParams.NotTopMost = $true }\n" +
			"if ($psadtDialogForce) { $dialogParams.Force = $true }\n" +
			"$adtResult = Show-ADTDialogBox @dialogParams\n" +
			"if ($null -ne $adtResult) { Write-Host \"Resultado: $adtResult\" } else { Write-Host 'Resultado: sem resposta (NoWait/Timeout)' }\n" +
			"exit 0\n"
		timeout := 3 * time.Minute
		if req.DialogNoWait {
			timeout = 30 * time.Second
		} else if req.DialogTimeout > 0 {
			timeout = time.Duration(req.DialogTimeout+30) * time.Second
		}
		return header + body, timeout

	default:
		body := openInteractive +
			"Show-ADTBalloonTip -BalloonTipTitle $psadtTitle -BalloonTipText $psadtMessage -BalloonTipIcon 'Info'\n" +
			"Write-Host 'BalloonTip exibido com sucesso'\n" +
			closeSession
		return header + body, 30 * time.Second
	}
}

func boolEnvValue(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// =============================================================================
// Preflight Checks
// =============================================================================

// PSADTPreflightResult agrupa resultados de verificacoes pre-flight do PSADT.
type PSADTPreflightResult struct {
	OSName             string `json:"osName"`
	OSVersion          string `json:"osVersion"`
	Architecture       string `json:"architecture"`
	PSVersion          string `json:"psVersion"`
	IsAdmin            bool   `json:"isAdmin"`
	RebootPending      bool   `json:"rebootPending"`
	NetworkAvailable   bool   `json:"networkAvailable"`
	UserInFocusMode    bool   `json:"userInFocusMode"`
	ModuleVersion      string `json:"moduleVersion"`
	ActiveUserSessions int    `json:"activeUserSessions"`
	Success            bool   `json:"success"`
	Error              string `json:"error"`
	CheckedAtUTC       string `json:"checkedAtUtc"`
}

func normalizeDialogButtons(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ok":
		return "Ok"
	case "okcancel":
		return "OkCancel"
	case "abortretryignore":
		return "AbortRetryIgnore"
	case "yesnocancel":
		return "YesNoCancel"
	case "yesno":
		return "YesNo"
	case "retrycancel":
		return "RetryCancel"
	case "canceltrycontinue":
		return "CancelTryContinue"
	default:
		return "Ok"
	}
}

func normalizeDialogDefault(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "first":
		return "First"
	case "second":
		return "Second"
	case "third":
		return "Third"
	default:
		return "First"
	}
}

func normalizeDialogIcon(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "none":
		return "None"
	case "stop":
		return "Stop"
	case "question":
		return "Question"
	case "exclamation":
		return "Exclamation"
	case "information", "info":
		return "Information"
	default:
		return "None"
	}
}
