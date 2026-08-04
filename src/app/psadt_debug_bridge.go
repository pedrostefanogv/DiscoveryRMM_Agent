package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"discovery/app/agentconfig"
	"discovery/app/core/processutil"
	"discovery/app/services/psadt"

	"golang.org/x/text/encoding/charmap"
)

var psadtVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}$`)

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

func parsePSADTInstallSource(raw string) (string, string) {
	text := strings.TrimSpace(strings.ToLower(raw))
	if text == "" || text == "powershell_gallery" || text == "psgallery" {
		return "powershell_gallery", ""
	}
	if strings.HasPrefix(text, "internal:") {
		return "internal", strings.TrimSpace(raw[len("internal:"):])
	}
	if strings.HasPrefix(text, "offline:") {
		return "offline", strings.TrimSpace(raw[len("offline:"):])
	}
	return "powershell_gallery", ""
}

func buildPSADTInstallScript(version, sourceType, sourceValue string) string {
	installCmd := ""
	sourceValue = strings.TrimSpace(sourceValue)

	switch sourceType {
	case "internal":
		repo := escapePowerShellSingleQuoted(sourceValue)
		if repo == "" {
			repo = "Internal"
		}
		installCmd = fmt.Sprintf("Install-Module -Name PSAppDeployToolkit -RequiredVersion %s -Repository '%s' -Scope AllUsers -Force -AllowClobber", version, repo)
	case "offline":
		path := escapePowerShellSingleQuoted(sourceValue)
		installCmd = fmt.Sprintf("$offlinePath='%s'; if (-not (Test-Path $offlinePath)) { throw 'offline source não encontrada' }; Copy-Item -Path $offlinePath -Destination (Join-Path $env:ProgramFiles 'WindowsPowerShell\\Modules\\PSAppDeployToolkit') -Recurse -Force", path)
	default:
		sourceType = "powershell_gallery"
		installCmd = fmt.Sprintf("Install-Module -Name PSAppDeployToolkit -RequiredVersion %s -Scope AllUsers -Force -AllowClobber", version)
	}

	return fmt.Sprintf(`$ErrorActionPreference='Stop'
try {
  %s
} catch {
  if ('%s' -ne 'offline') {
    Install-Module -Name PSAppDeployToolkit -RequiredVersion %s -Scope CurrentUser -Force -AllowClobber
  } else {
    throw
  }
}
$m = Get-Module -ListAvailable -Name PSAppDeployToolkit | Sort-Object Version -Descending | Select-Object -First 1
if (-not $m) { throw 'PSADT não encontrado após instalação' }
Write-Output $m.Version.ToString()`, installCmd, sourceType, version)
}

func escapePowerShellSingleQuoted(value string) string {
	return strings.ReplaceAll(value, "'", "''")
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

	// Script PSADT real que valida modulo, versao e comandos exportados
	psadtScript := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
try {
    Import-Module -Name PSAppDeployToolkit -ErrorAction Stop
} catch {
    Write-Error "Falha ao importar PSADT: $_"
    exit 1
}

[string]$appVendor = "Discovery"
[string]$appName = "%s"
[string]$appVersion = "%s"
[string]$appArch = "x64"
[string]$appLang = "pt-BR"
[string]$deploymentType = "Install"
[string]$requiredVersion = "4.1.8"

Write-Host "=========================================="
Write-Host "Validação PSADT Real do Discovery Agent"
Write-Host "=========================================="
Write-Host "Nome: $appName"
Write-Host "Versão: $appVersion"
Write-Host "Vendor: $appVendor"
Write-Host "Timestamp: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')"
Write-Host ""

$psadtModule = Get-Module -ListAvailable -Name PSAppDeployToolkit | Sort-Object Version -Descending | Select-Object -First 1
if (-not $psadtModule) {
	Write-Error "Módulo PSAppDeployToolkit não encontrado"
	exit 2
}

Write-Host "Módulo detectado: $($psadtModule.Name)"
Write-Host "Versão detectada: $($psadtModule.Version)"

if ([version]$psadtModule.Version -lt [version]$requiredVersion) {
	Write-Error "Versão do módulo abaixo da requerida. Atual: $($psadtModule.Version), Requerida: $requiredVersion"
	exit 3
}

$commands = Get-Command -Module PSAppDeployToolkit -ErrorAction Stop
$commandCount = @($commands).Count
if ($commandCount -le 0) {
	Write-Error "Módulo carregado, mas sem comandos exportados"
	exit 4
}

Write-Host "Comandos exportados: $commandCount"
Write-Host "Primeiros comandos:"
$commands | Select-Object -First 10 -ExpandProperty Name | ForEach-Object { Write-Host " - $_" }

Write-Host ""
Write-Host "✓ Validação real concluída com sucesso"
Write-Host "ExitCode: 0"
Write-Host "Timestamp: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')"
Write-Host "=========================================="

exit 0
`, appName, appVersion)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-Command", psadtScript)
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
		a.logs.append(fmt.Sprintf("[psadt] test script falhou: %v", err))
		return result
	}

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
