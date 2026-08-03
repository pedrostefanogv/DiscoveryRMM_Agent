package psadt

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

var versionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}$`)

// ModuleStatus representa o status do módulo PSAppDeployToolkit.
type ModuleStatus struct {
	Installed    bool   `json:"installed"`
	Version      string `json:"version"`
	Message      string `json:"message"`
	CheckedAtUTC string `json:"checkedAtUtc"`
}

// DebugState representa o estado de debug do PSADT.
type DebugState struct {
	RuntimeDebugMode     bool          `json:"runtimeDebugMode"`
	Configuration        interface{}   `json:"configuration"`
	ModuleStatus         ModuleStatus  `json:"moduleStatus"`
	NotificationBranding interface{}   `json:"notificationBranding"`
	NotificationPolicies []interface{} `json:"notificationPolicies"`
}

// DebugNotificationRequest é o payload de uma notificação de debug.
type DebugNotificationRequest struct {
	Title      string `json:"title"`
	Message    string `json:"message"`
	Mode       string `json:"mode"`
	Severity   string `json:"severity"`
	Layout     string `json:"layout"`
	Accent     string `json:"accent"`
	RequireAck bool   `json:"requireAck"`
}

// ScriptResult representa o resultado da execução de um script PSADT.
type ScriptResult struct {
	Success       bool   `json:"success"`
	ExitCode      int    `json:"exitCode"`
	Output        string `json:"output"`
	Error         string `json:"error"`
	ExecutedAtUTC string `json:"executedAtUtc"`
	DurationMS    int64  `json:"durationMs"`
}

// VisualNotificationRequest define os parametros para um teste visual nativo.
type VisualNotificationRequest struct {
	NotifType           string `json:"notifType"`
	Title               string `json:"title"`
	Message             string `json:"message"`
	AppName             string `json:"appName"`
	DurationSeconds     int    `json:"durationSeconds"`
	DialogButtons       string `json:"dialogButtons"`
	DialogDefault       string `json:"dialogDefault"`
	DialogIcon          string `json:"dialogIcon"`
	DialogTimeout       int    `json:"dialogTimeout"`
	DialogNoWait        bool   `json:"dialogNoWait"`
	DialogExitOnTimeout bool   `json:"dialogExitOnTimeout"`
	DialogNotTopMost    bool   `json:"dialogNotTopMost"`
	DialogForce         bool   `json:"dialogForce"`
}

// PreflightResult representa o resultado dos preflight checks.
type PreflightResult struct {
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

// Deps são as dependências injetadas no PSADTService.
type Deps struct {
	// Logf appends a log line.
	Logf func(string)
	// GetAgentConfiguration returns the agent configuration.
	GetAgentConfiguration func() AgentConfiguration
	// RuntimeDebugMode returns whether debug mode is active.
	RuntimeDebugMode func() bool
	// DispatchNotification dispatches a notification.
	DispatchNotification func(NotificationRequest) NotificationResponse
}

// AgentConfiguration é uma visão mínima da configuração do agente.
type AgentConfiguration struct {
	PSADT PSADTConfig
}

// PSADTConfig é a configuração PSADT.
type PSADTConfig struct {
	Enabled         *bool
	RequiredVersion string
	InstallSource   string
}

// NotificationRequest é o payload de uma notificação.
type NotificationRequest struct {
	NotificationID string
	Title          string
	Message        string
	Mode           string
	Severity       string
	EventType      string
	Layout         string
	TimeoutSeconds int
	Metadata       map[string]any
}

// NotificationResponse é a resposta de uma notificação.
type NotificationResponse struct {
	Accepted bool
	Message  string
}

// Service encapsula a lógica do domínio PSADT.
type Service struct {
	logf                  func(string)
	getAgentConfiguration func() AgentConfiguration
	runtimeDebugMode      func() bool
	dispatchNotification  func(NotificationRequest) NotificationResponse
}

// New cria um PSADTService.
func New(deps Deps) *Service {
	return &Service{
		logf:                  deps.Logf,
		getAgentConfiguration: deps.GetAgentConfiguration,
		runtimeDebugMode:      deps.RuntimeDebugMode,
		dispatchNotification:  deps.DispatchNotification,
	}
}

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

// CheckModuleStatus verifica o status do módulo PSAppDeployToolkit.
func (s *Service) CheckModuleStatus() ModuleStatus {
	s.logf("[psadt] verificando status do modulo PSAppDeployToolkit...")
	status := ModuleStatus{CheckedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	if runtime.GOOS != "windows" {
		status.Message = "PSADT suportado apenas no Windows"
		s.logf("[psadt] verificação ignorada: não é Windows")
		return status
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-Command",
		"$m = Get-Module -ListAvailable -Name PSAppDeployToolkit | Sort-Object Version -Descending | Select-Object -First 1; if ($m) { Write-Output $m.Version.ToString() }")
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	text := decodePowerShellOutput(out)
	if err != nil {
		status.Message = strings.TrimSpace(err.Error())
		if status.Message == "" {
			status.Message = "falha ao consultar módulo PSADT"
		}
		s.logf("[psadt] erro ao verificar modulo: " + status.Message)
		return status
	}
	if text == "" {
		status.Message = "módulo PSAppDeployToolkit não encontrado"
		s.logf("[psadt] módulo PSAppDeployToolkit não instalado")
		return status
	}
	status.Installed = true
	status.Version = text
	status.Message = "módulo PSAppDeployToolkit disponível"
	s.logf("[psadt] módulo instalado: versão " + text)
	return status
}

// InstallModule instala o módulo PSAppDeployToolkit.
func (s *Service) InstallModule(version string) ModuleStatus {
	status := ModuleStatus{CheckedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	if runtime.GOOS != "windows" {
		status.Message = "PSADT suportado apenas no Windows"
		s.logf("[psadt] instalação ignorada: não é Windows")
		return status
	}
	version = strings.TrimSpace(version)
	if version == "" {
		version = "4.1.8"
	}
	if !versionPattern.MatchString(version) {
		status.Message = "versão inválida"
		s.logf("[psadt] instalação rejeitada: versão inválida '" + version + "'")
		return status
	}
	installSource := "powershell_gallery"
	if s.getAgentConfiguration != nil {
		installSource = strings.TrimSpace(s.getAgentConfiguration().PSADT.InstallSource)
	}
	sourceType, sourceValue := parseInstallSource(installSource)
	s.logf("[psadt] iniciando instalação do módulo versão " + version + " via source=" + sourceType)
	script := buildInstallScript(version, sourceType, sourceValue)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-Command", script)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	text := decodePowerShellOutput(out)
	if err != nil {
		status.Message = strings.TrimSpace(err.Error())
		if text != "" {
			status.Message = text
		}
		if status.Message == "" {
			status.Message = "falha ao instalar PSADT"
		}
		if sourceType != "powershell_gallery" {
			s.logf("[psadt] source " + sourceType + " falhou; fallback para powershell_gallery")
			fallbackScript := buildInstallScript(version, "powershell_gallery", "")
			fallbackCmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-Command", fallbackScript)
			hideWindow(fallbackCmd)
			fallbackOut, fallbackErr := fallbackCmd.CombinedOutput()
			fallbackText := decodePowerShellOutput(fallbackOut)
			if fallbackErr == nil {
				status.Installed = true
				status.Version = fallbackText
				status.Message = "instalação concluída (fallback powershell_gallery)"
				s.logf("[psadt] módulo instalado com fallback PSGallery: versão " + fallbackText)
				return status
			}
		}
		s.logf("[psadt] falha na instalação do módulo: " + status.Message)
		return status
	}
	status.Installed = true
	status.Version = text
	status.Message = "instalação concluída (source=" + sourceType + ")"
	s.logf("[psadt] módulo instalado com sucesso: versão " + text)
	return status
}

func parseInstallSource(raw string) (string, string) {
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

func buildInstallScript(version, sourceType, sourceValue string) string {
	installCmd := ""
	sourceValue = strings.TrimSpace(sourceValue)
	switch sourceType {
	case "internal":
		repo := escapeSingleQuoted(sourceValue)
		if repo == "" {
			repo = "Internal"
		}
		installCmd = fmt.Sprintf("Install-Module -Name PSAppDeployToolkit -RequiredVersion %s -Repository '%s' -Scope AllUsers -Force -AllowClobber", version, repo)
	case "offline":
		path := escapeSingleQuoted(sourceValue)
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

func escapeSingleQuoted(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// EmitDebugNotification emite uma notificação de debug.
func (s *Service) EmitDebugNotification(req DebugNotificationRequest) error {
	if strings.TrimSpace(req.Title) == "" {
		req.Title = "Teste PSADT"
	}
	if strings.TrimSpace(req.Message) == "" {
		req.Message = "Notificação de teste"
	}
	if strings.TrimSpace(req.Mode) == "" {
		req.Mode = "notify_only"
	}
	if strings.TrimSpace(req.Severity) == "" {
		req.Severity = "medium"
	}
	if strings.TrimSpace(req.Layout) == "" {
		req.Layout = "toast"
	}
	notificationID := fmt.Sprintf("psadt-debug-%d", time.Now().UnixNano())
	resp := s.dispatchNotification(NotificationRequest{
		NotificationID: notificationID,
		Title:          req.Title,
		Message:        req.Message,
		Mode:           req.Mode,
		Severity:       req.Severity,
		EventType:      "psadt_debug_runtime",
		Layout:         req.Layout,
		TimeoutSeconds: 45,
		Metadata: map[string]any{
			"source":     "psadt-debug",
			"accent":     req.Accent,
			"requireAck": req.RequireAck,
		},
	})
	if !resp.Accepted {
		return fmt.Errorf("notificação rejeitada: %s", strings.TrimSpace(resp.Message))
	}
	s.logf("[notification] evento PSADT emitido via dispatch id=" + notificationID)
	return nil
}

// GetScriptTemplate retorna um template de script PSADT.
func (s *Service) GetScriptTemplate() string {
	return `# PSAppDeployToolkit - Deploy Script Template
# Baseado em: https://psappdeploytoolkit.com/docs/4.1.x/

# Importar o módulo PSAppDeployToolkit
try {
    Import-Module -Name PSAppDeployToolkit -ErrorAction Stop
} catch {
    Write-Error "Falha ao importar PSAppDeployToolkit: $_"
    exit 1
}

# Variáveis de Configuração da Aplicação
[string]$appVendor = "Company"
[string]$appName = "MyApp"
[string]$appVersion = "1.0"
[string]$appArch = "x64"
[string]$appLang = "pt-BR"
[string]$appRevision = "01"
[string]$appScriptVersion = "1.0"
[string]$deploymentType = "Install"

# Diretórios
[string]$appDeployToolkitPath = Split-Path -Parent $MyInvocation.MyCommand.Definition
[string]$appSourcePath = Join-Path -Path $appDeployToolkitPath -ChildPath 'Files'
[string]$appDestinationPath = "$env:ProgramFiles\$appVendor\$appName"

# Configurações de Log
[string]$appScriptLogPath = Join-Path -Path $appDeployToolkitPath -ChildPath 'Logs'

Write-Host ""
Write-Host "Iniciando Deploy: $appName v$appVersion"
Write-Host "Tipo: $deploymentType"
Write-Host ""

# ===== INSTALAÇÃO =====
if ($deploymentType -eq 'Install') {
	Write-Host "Instalando $appName..."
	New-Item -ItemType Directory -Path $appDestinationPath -Force | Out-Null
	Write-Host "Instalação finalizada"
}

# ===== DESINSTALAÇÃO =====
if ($deploymentType -eq 'Uninstall') {
    Write-Host "Desinstalando $appName..."
	if (Test-Path -Path $appDestinationPath) {
		Remove-Item -Path $appDestinationPath -Recurse -Force
	}
	Write-Host "Desinstalação concluída"
}

exit 0
`
}

// ExecuteCustomScript executa um script PSADT customizado.
func (s *Service) ExecuteCustomScript(scriptContent string) ScriptResult {
	result := ScriptResult{ExecutedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	if runtime.GOOS != "windows" {
		result.Success = false
		result.Error = "PSADT suportado apenas em Windows"
		result.ExitCode = 1
		s.logf("[psadt] custom script ignorado: não é Windows")
		return result
	}
	if strings.TrimSpace(scriptContent) == "" {
		result.Success = false
		result.Error = "Script vazio"
		result.ExitCode = 1
		s.logf("[psadt] custom script rejeitado: conteúdo vazio")
		return result
	}
	s.logf(fmt.Sprintf("[psadt] executando script customizado (%d bytes)...", len(strings.TrimSpace(scriptContent))))
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-Command", scriptContent)
	hideWindow(cmd)
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
		s.logf(fmt.Sprintf("[psadt] custom script falhou: %v", err))
		return result
	}
	result.Success = true
	result.ExitCode = 0
	s.logf(fmt.Sprintf("[psadt] custom script executado com sucesso em %dms", elapsed))
	return result
}

// hideWindow esconde a janela do processo (no-op em não-Windows).
var hideWindow = func(cmd *exec.Cmd) {}
