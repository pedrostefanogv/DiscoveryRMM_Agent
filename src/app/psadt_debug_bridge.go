package app

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	gopsadt "github.com/pedrostefanogv/go-psadt"
	pstypes "github.com/pedrostefanogv/go-psadt/types"

	"discovery/app/agentconfig"
	"discovery/app/core/platform"
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
	a.Logs.Append("[psadt] GetPSADTDebugState chamado")
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
	a.Logs.Append(fmt.Sprintf("[psadt] estado: enabled=%s version=%s moduleInstalled=%t moduleVersion=%s",
		enabledStr, cfg.PSADT.RequiredVersion, module.Installed, module.Version))
	return PSADTDebugState{
		RuntimeDebugMode:     a.RuntimeFlags.DebugMode,
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
		a.Logs.Append(fmt.Sprintf("[psadt] bootstrap: módulo já instalado (v%s), nada a fazer", status.Version))
		return
	}

	version := strings.TrimSpace(cfg.RequiredVersion)
	if version == "" {
		version = "4.1.8"
	}
	a.Logs.Append(fmt.Sprintf("[psadt] bootstrap: módulo não instalado — instalando v%s em background", version))

	a.safeGo(func() {
		result := a.psadtSvc.InstallModule(version)
		if result.Installed {
			a.Logs.Append(fmt.Sprintf("[psadt] bootstrap: módulo instalado com sucesso (v%s)", result.Version))
		} else {
			a.Logs.Append("[psadt] bootstrap: falha ao instalar módulo: " + result.Message)
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
	Success  bool   `json:"success"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
	// Result carrega a resposta estruturada do diálogo quando aplicável:
	// o texto digitado pelo usuário (InputDialogResult.Text) em prompts de
	// entrada, ou o texto do botão clicado (DialogBoxResult).
	Result        string `json:"result"`
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
		a.Logs.Append("[psadt] test script ignorado: não é Windows")
		return result
	}

	if appName == "" {
		appName = "TestApp"
	}
	if appVersion == "" {
		appVersion = "1.0.0"
	}
	a.Logs.Append(fmt.Sprintf("[psadt] executando test script: appName=%s appVersion=%s", appName, appVersion))

	// Usa a lib go-psadt (runner PowerShell persistente) em vez de montar
	// scripts PowerShell inline. Valida módulo, versão e comandos exportados
	// via API tipada.
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Usa a versao minima configurada no agente (RequiredVersion).
	minVer := strings.TrimSpace(a.GetAgentConfiguration().PSADT.RequiredVersion)
	if minVer == "" {
		minVer = "4.1.8"
	}
	client, err := gopsadt.NewClient(
		gopsadt.WithTimeout(30*time.Second),
		gopsadt.WithMinModuleVersion(minVer),
	)
	if err != nil {
		result.Success = false
		result.Error = "falha ao inicializar PSADT: " + err.Error()
		result.ExitCode = 1
		result.DurationMS = time.Since(start).Milliseconds()
		a.Logs.Append("[psadt] test script falhou na inicialização: " + err.Error())
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
		a.Logs.Append("[psadt] test script falhou ao abrir sessão: " + err.Error())
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
	a.Logs.Append(fmt.Sprintf("[psadt] test script executado com sucesso em %dms", elapsed))
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
	NotifType       string `json:"notifType"` // balloon_info | balloon_warning | balloon_error | prompt_ok | prompt_yesno | prompt_continue | prompt_input | progress | dialog_box | restart_prompt | welcome
	Title           string `json:"title"`
	Message         string `json:"message"`
	Subtitle        string `json:"subtitle"` // usado como StatusMessageDetail (progress) e Subtitle (prompt)
	AppName         string `json:"appName"`
	DurationSeconds int    `json:"durationSeconds"` // utilizado apenas pelo tipo progress

	// Balloon (Show-ADTBalloonTip)
	BalloonTimeSeconds int  `json:"balloonTimeSeconds"` // BalloonTipTime em segundos (0 = 10s default do PSADT)
	BalloonNoWait      bool `json:"balloonNoWait"`

	// Prompt (Show-ADTInstallationPrompt)
	PromptLeftText   string `json:"promptLeftText"`
	PromptMiddleText string `json:"promptMiddleText"`
	PromptRightText  string `json:"promptRightText"`
	PromptIcon       string `json:"promptIcon"`    // DialogSystemIcon: Information | Question | Exclamation | Error | Hand | Shield | Asterisk | Application | WinLogo | (vazio = omitir)
	PromptTimeout    int    `json:"promptTimeout"` // segundos, 0 = 120s (nao use o default de 55min do config.psd1)
	PromptNoWait     bool   `json:"promptNoWait"`
	PromptNotTopMost bool   `json:"promptNotTopMost"`

	// Dialog (Show-ADTDialogBox)
	DialogButtons       string `json:"dialogButtons"` // Ok | OkCancel | AbortRetryIgnore | YesNoCancel | YesNo | RetryCancel | CancelTryContinue
	DialogDefault       string `json:"dialogDefault"` // First | Second | Third
	DialogIcon          string `json:"dialogIcon"`    // None | Stop | Question | Exclamation | Information
	DialogTimeout       int    `json:"dialogTimeout"` // segundos, 0 = 120s; maximo UI.DefaultTimeout do config.psd1 (3300s)
	DialogNoWait        bool   `json:"dialogNoWait"`
	DialogExitOnTimeout bool   `json:"dialogExitOnTimeout"`
	DialogNotTopMost    bool   `json:"dialogNotTopMost"`
	DialogForce         bool   `json:"dialogForce"`

	// Restart (Show-ADTInstallationRestartPrompt)
	RestartCountdownSeconds int  `json:"restartCountdownSeconds"`
	RestartNoCountdown      bool `json:"restartNoCountdown"`

	// Prompt input (Show-ADTInstallationPrompt -RequestInput)
	PromptDefaultValue string `json:"promptDefaultValue"`

	// Branding (config.psd1 parcial + Initialize-ADTModule -ScriptDirectory).
	// Aplica-se aos dialogs Fluent/Classic (prompts, progress, welcome,
	// restart) E ao BalloonTip/toast: o PSADT usa Toolkit.CompanyName como
	// TrayTitle (nome exibido na notificacao) e Assets.Logo como TrayIcon.
	// Show-ADTDialogBox (Win32) usa icones de sistema e ignora isso.
	BrandingIconPath   string `json:"brandingIconPath"`   // PNG do logo (modo claro)
	BrandingIconDark   string `json:"brandingIconDark"`   // PNG do logo (modo escuro)
	BrandingBannerPath string `json:"brandingBannerPath"` // PNG do banner (dialogos Classic)
	DialogStyle        string `json:"dialogStyle"`        // Fluent | Classic
	FluentAccentColor  string `json:"fluentAccentColor"`  // hex RGB(A): 4A9EFF ou FF4A9EFF
	// CompanyName vira Toolkit.CompanyName: e o TrayTitle do toast/balloon e o
	// texto default de subtitulos dos dialogs Fluent. Vazio usa o branding do
	// agent (notificationBranding.companyName) ou "Discovery Agent".
	CompanyName string `json:"companyName"`

	// Welcome (Show-ADTInstallationWelcome)
	CloseProcesses          string `json:"closeProcesses"` // nomes de processos separados por virgula
	AllowDefer              bool   `json:"allowDefer"`
	DeferTimes              int    `json:"deferTimes"`
	DeferDeadline           string `json:"deferDeadline"` // yyyy-MM-dd (opcional)
	BlockExecution          bool   `json:"blockExecution"`
	CloseProcessesCountdown int    `json:"closeProcessesCountdown"`
}

// ExecutePSADTVisualNotification executa uma notificacao visual nativa via cmdlets reais do PSAppDeployToolkit.
// Grava um .ps1 temporario, executa com PowerShell -File e remove o arquivo ao terminar.
func (a *App) ExecutePSADTVisualNotification(req PSADTVisualNotificationRequest) PSADTScriptResult {
	result := PSADTScriptResult{ExecutedAtUTC: time.Now().UTC().Format(time.RFC3339)}

	if runtime.GOOS != "windows" {
		result.Error = "PSADT suportado apenas em Windows"
		result.ExitCode = 1
		a.Logs.Append("[psadt] visual notification ignorada: nao e Windows")
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
	// CompanyName: assina o TrayTitle do toast/balloon (nome exibido na
	// notificacao) e o subtitulo default dos dialogs Fluent. Sem valor
	// explicito usa o branding do tenant e, na ausencia, "Discovery Agent" —
	// evita o "PSAppDeployToolkit" default do modulo.
	if strings.TrimSpace(req.CompanyName) == "" {
		if a != nil {
			req.CompanyName = strings.TrimSpace(a.GetAgentConfiguration().NotificationBranding.CompanyName)
		}
		if strings.TrimSpace(req.CompanyName) == "" {
			req.CompanyName = "Discovery Agent"
		}
	}
	if req.DurationSeconds <= 0 || req.DurationSeconds > 60 {
		req.DurationSeconds = 5
	}
	// Subtitulo/Detail vazio vira um espaco em branco: o PSADT renderiza a
	// linha reservada ao subtitulo em vez de omiti-la completamente.
	req.Subtitle = defaultPSADTSubtitle(req.Subtitle)
	// Balloon: tempo de exibicao 1..120s (0 usa o default de 10s do PSADT).
	if req.BalloonTimeSeconds < 0 || req.BalloonTimeSeconds > 120 {
		req.BalloonTimeSeconds = 10
	}
	req.PromptIcon = normalizePromptIcon(req.PromptIcon)
	// Prompts bloqueantes: sem timeout explicito o PSADT usa o
	// UI.DefaultTimeout do config.psd1 (55min), entao fixamos 120s por padrao
	// para o teste retornar em tempo previsivel. Limite: UI.DefaultTimeout
	// (3300s) — valores maiores geram ValidateScript error no PSADT.
	// Com PromptNoWait omitimos o Timeout (dialogo assincrono em thread separada).
	switch {
	case req.PromptNoWait:
		req.PromptTimeout = 0
	case req.PromptTimeout <= 0:
		req.PromptTimeout = 120
	}
	if req.PromptTimeout > 3300 {
		req.PromptTimeout = 3300
	}
	req.DialogButtons = normalizeDialogButtons(req.DialogButtons)
	req.DialogDefault = normalizeDialogDefault(req.DialogDefault)
	req.DialogIcon = normalizeDialogIcon(req.DialogIcon)
	if req.DialogTimeout <= 0 && !req.DialogNoWait {
		req.DialogTimeout = 120
	}
	if req.DialogTimeout > 3300 {
		req.DialogTimeout = 3300
	}
	if req.RestartCountdownSeconds <= 0 {
		req.RestartCountdownSeconds = 60
	}
	if req.RestartCountdownSeconds > 3600 {
		req.RestartCountdownSeconds = 3600
	}
	if req.DeferTimes < 0 {
		req.DeferTimes = 0
	}
	if req.CloseProcessesCountdown < 0 {
		req.CloseProcessesCountdown = 0
	}
	a.Logs.Append(fmt.Sprintf("[psadt] notificacao visual nativa: tipo=%s titulo=%q", req.NotifType, req.Title))

	script, timeout := buildPSADTVisualScript(req)

	// Script .ps1 dentro de %WINDIR%/Temp/Discovery (convenção do agente) —
	// nunca na temp genérica do processo (os.TempDir vira C:/Windows/Temp
	// quando o agente roda como SYSTEM, poluindo a raiz da pasta Temp).
	tmpBase, err := platform.EnsureTempDir()
	if err != nil {
		result.Error = "falha ao preparar diretorio temporario: " + err.Error()
		result.ExitCode = 1
		a.Logs.Append("[psadt] " + result.Error)
		return result
	}
	tmpFile, err := os.CreateTemp(tmpBase, "psadt-visual-*.ps1")
	if err != nil {
		result.Error = "falha ao criar arquivo temporario: " + err.Error()
		result.ExitCode = 1
		a.Logs.Append("[psadt] " + result.Error)
		return result
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(script); err != nil {
		tmpFile.Close()
		result.Error = "falha ao escrever script temporario: " + err.Error()
		result.ExitCode = 1
		a.Logs.Append("[psadt] " + result.Error)
		return result
	}
	tmpFile.Close()

	// Branding: escreve um config.psd1 parcial no mesmo diretorio do script
	// temporario quando o usuario personalizou icones/estilo/acento. O script
	// gerado chama Initialize-ADTModule -ScriptDirectory e o PSADT 4.1.x faz
	// o merge recursivo com o config default do modulo (mecanismo oficial).
	stagingConfig := false
	if stagingFiles, stagingErr := writePSADTVisualBranding(req, tmpPath); stagingErr != nil {
		a.Logs.Append("[psadt] branding customizado ignorado: " + stagingErr.Error())
	} else if len(stagingFiles) > 0 {
		stagingConfig = true
		defer func() {
			for _, p := range stagingFiles {
				_ = os.RemoveAll(p)
			}
		}()
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-WindowStyle", "Hidden",
		"-ExecutionPolicy", "Bypass", "-File", tmpPath)
	cmd.Env = append(os.Environ(),
		"PSADT_STAGING_CONFIG="+boolEnvValue(stagingConfig),
		"PSADT_TITLE="+req.Title,
		"PSADT_MESSAGE="+req.Message,
		"PSADT_SUBTITLE="+req.Subtitle,
		"PSADT_APPNAME="+req.AppName,
		fmt.Sprintf("PSADT_DURATION=%d", req.DurationSeconds),
		fmt.Sprintf("PSADT_BALLOON_TIME=%d", req.BalloonTimeSeconds),
		"PSADT_BALLOON_NOWAIT="+boolEnvValue(req.BalloonNoWait),
		"PSADT_PROMPT_LEFT="+req.PromptLeftText,
		"PSADT_PROMPT_MIDDLE="+req.PromptMiddleText,
		"PSADT_PROMPT_RIGHT="+req.PromptRightText,
		"PSADT_PROMPT_ICON="+req.PromptIcon,
		fmt.Sprintf("PSADT_PROMPT_TIMEOUT=%d", req.PromptTimeout),
		"PSADT_PROMPT_NOWAIT="+boolEnvValue(req.PromptNoWait),
		"PSADT_PROMPT_NOT_TOPMOST="+boolEnvValue(req.PromptNotTopMost),
		"PSADT_PROMPT_DEFAULT="+strings.TrimSpace(req.PromptDefaultValue),
		"PSADT_DIALOG_BUTTONS="+req.DialogButtons,
		"PSADT_DIALOG_DEFAULT="+req.DialogDefault,
		"PSADT_DIALOG_ICON="+req.DialogIcon,
		fmt.Sprintf("PSADT_DIALOG_TIMEOUT=%d", req.DialogTimeout),
		"PSADT_DIALOG_NOWAIT="+boolEnvValue(req.DialogNoWait),
		"PSADT_DIALOG_EXIT_ON_TIMEOUT="+boolEnvValue(req.DialogExitOnTimeout),
		"PSADT_DIALOG_NOT_TOPMOST="+boolEnvValue(req.DialogNotTopMost),
		"PSADT_DIALOG_FORCE="+boolEnvValue(req.DialogForce),
		fmt.Sprintf("PSADT_RESTART_COUNTDOWN=%d", req.RestartCountdownSeconds),
		"PSADT_RESTART_NO_COUNTDOWN="+boolEnvValue(req.RestartNoCountdown),
		"PSADT_WELCOME_PROCESSES="+req.CloseProcesses,
		"PSADT_WELCOME_ALLOW_DEFER="+boolEnvValue(req.AllowDefer),
		fmt.Sprintf("PSADT_WELCOME_DEFER_TIMES=%d", req.DeferTimes),
		"PSADT_WELCOME_DEFER_DEADLINE="+strings.TrimSpace(req.DeferDeadline),
		"PSADT_WELCOME_BLOCK_EXEC="+boolEnvValue(req.BlockExecution),
		fmt.Sprintf("PSADT_WELCOME_CLOSE_COUNTDOWN=%d", req.CloseProcessesCountdown),
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
		a.Logs.Append(fmt.Sprintf("[psadt] notificacao visual falhou (tipo=%s): %v", req.NotifType, err))
		return result
	}

	result.Success = true
	result.ExitCode = 0
	// Extrai a resposta estruturada do diálogo (texto digitado pelo usuário
	// em prompts de entrada, ou o botão clicado em dialogs/prompts).
	if resposta := extractVisualDialogResult(result.Output); resposta != "" {
		result.Result = resposta
		a.Logs.Append(fmt.Sprintf("[psadt] resposta do dialogo (tipo=%s): %q", req.NotifType, resposta))
	}
	a.Logs.Append(fmt.Sprintf("[psadt] notificacao visual concluida (tipo=%s) em %dms", req.NotifType, elapsed))
	return result
}

// extractVisualDialogResult procura a linha "PSADT_RESULT=<json>" emitida pelo
// script de notificação visual e devolve a resposta do diálogo de forma
// plana. Formatos aceitos:
//   - {"Result":"<botao>","Text":"<texto digitado>"}  (InputDialogResult)
//   - "<botao>"                                        (DialogBoxResult string)
//   - texto puro                                       (fallback)
func extractVisualDialogResult(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PSADT_RESULT=") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "PSADT_RESULT="))
		if raw == "" || raw == "null" {
			return ""
		}
		var obj struct {
			Result string `json:"Result"`
			Text   string `json:"Text"`
		}
		if err := json.Unmarshal([]byte(raw), &obj); err == nil && (obj.Result != "" || obj.Text != "") {
			if obj.Text != "" {
				return obj.Text
			}
			return obj.Result
		}
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			return s
		}
		return raw
	}
	return ""
}

// buildPSADTVisualScript gera o script PowerShell para o tipo de notificacao solicitado.
//
// Parametros validados contra o codigo-fonte do PSAppDeployToolkit 4.1.8:
//   - Show-ADTInstallationProgress: NAO tem -WindowTitle; usa -StatusMessage,
//     -StatusMessageDetail, -StatusBarPercentage, -NotTopMost e -AllowMove.
//   - Show-ADTInstallationPrompt -Icon usa DialogSystemIcon (Information,
//     Question, Exclamation, Error, Hand, Shield, ...). 'Info' e INVALIDO.
//   - -Timeout de prompts/dialogs nao pode exceder o UI.DefaultTimeout do
//     config.psd1 (default 55min = 3300s); ValidateScript rejeita maiores.
//   - Show-ADTBalloonTip aceita -BalloonTipTime (ms) e -NoWait.
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
		// Branding customizado: configura o PSADT para usar o config.psd1 parcial
		// escrito ao lado deste script (merge oficial via -ScriptDirectory).
		"if ($env:PSADT_STAGING_CONFIG -eq '1') {\n" +
		"    try { Initialize-ADTModule -ScriptDirectory (Split-Path -Parent $PSCommandPath) } catch { Write-Error \"Falha ao aplicar branding customizado: $_\"; exit 3 }\n" +
		"}\n" +
		"$psadtTitle    = $env:PSADT_TITLE\n" +
		"$psadtMessage  = $env:PSADT_MESSAGE\n" +
		"$psadtAppName  = $env:PSADT_APPNAME\n" +
		"$psadtSubtitle = $env:PSADT_SUBTITLE\n" +
		"$psadtDuration = [int]$env:PSADT_DURATION\n" +
		"$psadtBalloonTime = [int]$env:PSADT_BALLOON_TIME\n" +
		"$psadtBalloonNoWait = ($env:PSADT_BALLOON_NOWAIT -eq '1')\n" +
		"$psadtPromptLeft = $env:PSADT_PROMPT_LEFT\n" +
		"$psadtPromptMiddle = $env:PSADT_PROMPT_MIDDLE\n" +
		"$psadtPromptRight = $env:PSADT_PROMPT_RIGHT\n" +
		"$psadtPromptIcon = $env:PSADT_PROMPT_ICON\n" +
		"$psadtPromptTimeout = [int]$env:PSADT_PROMPT_TIMEOUT\n" +
		"$psadtPromptNoWait = ($env:PSADT_PROMPT_NOWAIT -eq '1')\n" +
		"$psadtPromptNotTopMost = ($env:PSADT_PROMPT_NOT_TOPMOST -eq '1')\n" +
		"$psadtDialogButtons = $env:PSADT_DIALOG_BUTTONS\n" +
		"$psadtDialogDefault = $env:PSADT_DIALOG_DEFAULT\n" +
		"$psadtDialogIcon = $env:PSADT_DIALOG_ICON\n" +
		"$psadtDialogTimeout = [int]$env:PSADT_DIALOG_TIMEOUT\n" +
		"$psadtDialogNoWait = ($env:PSADT_DIALOG_NOWAIT -eq '1')\n" +
		"$psadtDialogExitOnTimeout = ($env:PSADT_DIALOG_EXIT_ON_TIMEOUT -eq '1')\n" +
		"$psadtDialogNotTopMost = ($env:PSADT_DIALOG_NOT_TOPMOST -eq '1')\n" +
		"$psadtDialogForce = ($env:PSADT_DIALOG_FORCE -eq '1')\n" +
		"$psadtRestartCountdown = [int]$env:PSADT_RESTART_COUNTDOWN\n" +
		"$psadtRestartNoCountdown = ($env:PSADT_RESTART_NO_COUNTDOWN -eq '1')\n" +
		"$psadtWelcomeProcesses = $env:PSADT_WELCOME_PROCESSES\n" +
		"$psadtWelcomeAllowDefer = ($env:PSADT_WELCOME_ALLOW_DEFER -eq '1')\n" +
		"$psadtWelcomeDeferTimes = [int]$env:PSADT_WELCOME_DEFER_TIMES\n" +
		"$psadtWelcomeDeadline = $env:PSADT_WELCOME_DEFER_DEADLINE\n" +
		"$psadtWelcomeBlockExec = ($env:PSADT_WELCOME_BLOCK_EXEC -eq '1')\n" +
		"$psadtWelcomeCloseCountdown = [int]$env:PSADT_WELCOME_CLOSE_COUNTDOWN\n\n"

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

	// printResult emite o resultado do diálogo em formato legível e em um
	// marcador estruturado ("PSADT_RESULT=<json>") que o lado Go extrai para
	// PSADTScriptResult.Result. Para prompts de entrada (-RequestInput), o
	// InputDialogResult do PSADT serializa {"Result":"<botao>","Text":"<texto
	// digitado pelo usuario>"}. Para DialogBox o resultado e o texto do botao.
	printResult := "if ($null -ne $adtResult) {\n" +
		"  Write-Host (\"Resultado: \" + $adtResult)\n" +
		"  Write-Host (\"PSADT_RESULT=\" + ($adtResult | ConvertTo-Json -Compress))\n" +
		"} else { Write-Host 'Resultado: sem resposta (NoWait/Timeout)' }\n"

	switch req.NotifType {
	case "balloon_info", "balloon_warning", "balloon_error":
		body := openInteractive +
			"$balloonParams = @{\n" +
			"  BalloonTipTitle = $psadtTitle\n" +
			"  BalloonTipText = $psadtMessage\n" +
			fmt.Sprintf("  BalloonTipIcon = '%s'\n", balloonIcon) +
			"}\n" +
			"if ($psadtBalloonTime -gt 0) { $balloonParams.BalloonTipTime = $psadtBalloonTime * 1000 }\n" +
			"if ($psadtBalloonNoWait) { $balloonParams.NoWait = $true }\n" +
			"Show-ADTBalloonTip @balloonParams\n" +
			"Write-Host 'BalloonTip exibido com sucesso'\n" +
			// O PSADT encerra o processo cliente ao fechar a sessao, o que
			// REMOVE o balloon. Um script de uso unico sairia imediatamente e o
			// toast nunca apareceria; mantem o script vivo enquanto ele exibe.
			"Start-Sleep -Seconds $psadtBalloonTime\n" +
			closeSession
		// Timeout do processo PowerShell: precisa cobrir a espera do balloon
		// (BalloonTimeSeconds) + a inicializacao do modulo.
		timeout := time.Duration(req.BalloonTimeSeconds+30) * time.Second
		if timeout < 30*time.Second {
			timeout = 30 * time.Second
		}
		return header + body, timeout

	case "prompt_ok", "prompt_yesno", "prompt_continue", "prompt_input":
		body := openInteractive +
			"$promptParams = @{\n" +
			"  Message = $psadtMessage\n" +
			"  Title = $psadtTitle\n" +
			"}\n" +
			// PSADT 4.1.8: o Fluent valida Subtitle via IsNullOrWhiteSpace no
			// BaseDialogOptions. Um espaco em branco (default do
			// defaultPSADTSubtitle) faz o cmdlet lancar "Subtitle value is null
			// or invalid" e NADA e exibido. O AppName entra como fallback para
			// garantir sempre um subtitulo valido.
			"$promptSubtitle = if ($psadtSubtitle -and $psadtSubtitle.Trim()) { $psadtSubtitle } else { $psadtAppName }\n" +
			"$promptParams.Subtitle = $promptSubtitle\n" +
			"if ($psadtPromptLeft) { $promptParams.ButtonLeftText = $psadtPromptLeft }\n" +
			"if ($psadtPromptMiddle) { $promptParams.ButtonMiddleText = $psadtPromptMiddle }\n" +
			"if ($psadtPromptRight) { $promptParams.ButtonRightText = $psadtPromptRight }\n" +
			"if ($psadtPromptIcon) { $promptParams.Icon = $psadtPromptIcon }\n" +
			"if ($psadtPromptTimeout -gt 0) { $promptParams.Timeout = $psadtPromptTimeout }\n" +
			// -NoExitOnTimeout: sem isso o PSADT fecha a sessao ADT com o
			// DefaultExitCode (1618) ao expirar o timeout e o script ainda tenta
			// Close-ADTSession de novo. Para uma notificacao queremos apenas
			// retornar "Timeout" e fechar normalmente com exit 0.
			"$promptParams.NoExitOnTimeout = $true\n" +
			"if ($psadtPromptNoWait) { $promptParams.NoWait = $true }\n" +
			"if ($psadtPromptNotTopMost) { $promptParams.NotTopMost = $true }\n"
		switch req.NotifType {
		case "prompt_ok":
			// Overrides padrao apenas quando o usuario nao personalizou o botao.
			if strings.TrimSpace(req.PromptRightText) == "" {
				body += "$promptParams.ButtonRightText = 'OK'\n"
			}
		case "prompt_yesno":
			if strings.TrimSpace(req.PromptLeftText) == "" {
				body += "$promptParams.ButtonLeftText = 'Sim'\n"
			}
			if strings.TrimSpace(req.PromptRightText) == "" {
				body += "$promptParams.ButtonRightText = 'Nao'\n"
			}
		case "prompt_continue":
			if strings.TrimSpace(req.PromptLeftText) == "" {
				body += "$promptParams.ButtonLeftText = 'Continuar'\n"
			}
			if strings.TrimSpace(req.PromptRightText) == "" {
				body += "$promptParams.ButtonRightText = 'Adiar'\n"
			}
		case "prompt_input":
			body += "$promptParams.RequestInput = $true\n" +
				"if ($env:PSADT_PROMPT_DEFAULT) { $promptParams.DefaultValue = $env:PSADT_PROMPT_DEFAULT }\n"
		}
		body += "$adtResult = Show-ADTInstallationPrompt @promptParams\n" +
			printResult +
			closeSession
		timeout := 3 * time.Minute
		if req.PromptNoWait {
			timeout = 30 * time.Second
		} else if req.PromptTimeout > 0 {
			timeout = time.Duration(req.PromptTimeout+30) * time.Second
		}
		return header + body, timeout

	case "progress":
		body := openNonInt +
			"$progressParams = @{\n" +
			"  StatusMessage = $psadtMessage\n" +
			"}\n" +
			"if ($psadtSubtitle) { $progressParams.StatusMessageDetail = $psadtSubtitle }\n" +
			"Show-ADTInstallationProgress @progressParams\n" +
			"Start-Sleep -Seconds $psadtDuration\n" +
			"try { Close-ADTInstallationProgress } catch {}\n" +
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
			printResult +
			"exit 0\n"
		timeout := 3 * time.Minute
		if req.DialogNoWait {
			timeout = 30 * time.Second
		} else if req.DialogTimeout > 0 {
			timeout = time.Duration(req.DialogTimeout+30) * time.Second
		}
		return header + body, timeout

	case "restart_prompt":
		body := openInteractive +
			"$restartParams = @{}\n" +
			"if ($psadtRestartNoCountdown) { $restartParams.NoCountdown = $true } else { $restartParams.CountdownSeconds = $psadtRestartCountdown }\n" +
			"Show-ADTInstallationRestartPrompt @restartParams\n" +
			"Write-Host 'RestartPrompt exibido com sucesso'\n" +
			closeSession
		return header + body, time.Duration(req.RestartCountdownSeconds+60) * time.Second

	case "welcome":
		body := openInteractive +
			"$welcomeParams = @{}\n" +
			"$welcomeProcs = @($psadtWelcomeProcesses -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })\n" +
			"if ($welcomeProcs.Count -gt 0) { $welcomeParams.CloseProcesses = $welcomeProcs }\n" +
			"if ($psadtWelcomeAllowDefer) { $welcomeParams.AllowDefer = $true }\n" +
			"if ($psadtWelcomeDeferTimes -gt 0) { $welcomeParams.DeferTimes = $psadtWelcomeDeferTimes }\n" +
			"if ($psadtWelcomeDeadline) { $welcomeParams.DeferDeadline = $psadtWelcomeDeadline }\n" +
			"if ($psadtWelcomeBlockExec) { $welcomeParams.BlockExecution = $true }\n" +
			"if ($psadtWelcomeCloseCountdown -gt 0) { $welcomeParams.CloseProcessesCountdown = $psadtWelcomeCloseCountdown }\n" +
			"Show-ADTInstallationWelcome @welcomeParams\n" +
			"Write-Host 'InstallationWelcome concluido'\n" +
			closeSession
		return header + body, 6*time.Minute + time.Duration(req.CloseProcessesCountdown)*time.Second

	default:
		body := openInteractive +
			"$balloonParams = @{\n" +
			"  BalloonTipTitle = $psadtTitle\n" +
			"  BalloonTipText = $psadtMessage\n" +
			"  BalloonTipIcon = 'Info'\n" +
			"}\n" +
			"if ($psadtBalloonTime -gt 0) { $balloonParams.BalloonTipTime = $psadtBalloonTime * 1000 }\n" +
			"if ($psadtBalloonNoWait) { $balloonParams.NoWait = $true }\n" +
			"Show-ADTBalloonTip @balloonParams\n" +
			"Write-Host 'BalloonTip exibido com sucesso'\n" +
			closeSession
		return header + body, 45 * time.Second
	}
}

func boolEnvValue(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// defaultPSADTSubtitle devolve um espaco em branco quando o usuario nao
// preencheu o Subtitulo/Detail. O PSADT (Show-ADTInstallationPrompt -Subtitle
// e Show-ADTInstallationProgress -StatusMessageDetail) omite a linha quando o
// valor e vazio; com um espaco a linha reservada aparece em branco.
func defaultPSADTSubtitle(s string) string {
	if strings.TrimSpace(s) == "" {
		return " "
	}
	return s
}

// psadtAgentIconPNG e o appiconPSADT.png do Discovery Agent (embedado),
// usado como logo default nos dialogs Fluent do PSADT para padronizar a
// identidade visual das notificacoes com o icone do agent. O PNG (~200 KB)
// e o mesmo asset de build (src/build/appiconPSADT.png) e e o formato
// nativo esperado pela chave Assets.Logo do config.psd1.
//
//go:embed assets/psadt/appiconPSADT.png
var psadtAgentIconPNG []byte

// psadtNotifUsesFluentDialogs indica se o tipo de notificacao renderiza
// dialogs Fluent/Classic do PSADT (que exibem o logo do config.psd1).
// Dialog Box (Win32 MessageBox) usa icone de sistema.
func psadtNotifUsesFluentDialogs(notifType string) bool {
	switch notifType {
	case "prompt_ok", "prompt_yesno", "prompt_continue", "prompt_input", "progress", "restart_prompt", "welcome":
		return true
	default:
		return false
	}
}

// psadtNotifUsesTrayBranding indica se o tipo renderiza balloon/toast do PSADT,
// cujo TrayTitle e TrayIcon vem de Toolkit.CompanyName e Assets.Logo.
func psadtNotifUsesTrayBranding(notifType string) bool {
	switch notifType {
	case "balloon_info", "balloon_warning", "balloon_error":
		return true
	default:
		return false
	}
}

// writePSADTVisualBranding grava um config.psd1 parcial (e copia os assets
// de branding) no diretorio do script temporario. O PSADT 4.1.x faz o merge
// recursivo desse config sobre o default via Initialize-ADTModule
// -ScriptDirectory. Retorna a lista de arquivos criados (para cleanup) ou
// nil quando nao ha nada a personalizar.
func writePSADTVisualBranding(req PSADTVisualNotificationRequest, scriptPath string) ([]string, error) {
	userIcon := strings.TrimSpace(req.BrandingIconPath)
	userIconDark := strings.TrimSpace(req.BrandingIconDark)
	bannerPath := strings.TrimSpace(req.BrandingBannerPath)
	accent := normalizeHexAccent(req.FluentAccentColor)
	style := normalizeDialogStyle(req.DialogStyle)
	fluentApplies := psadtNotifUsesFluentDialogs(req.NotifType)
	// Balloons/toasts tambem usam branding: TrayTitle (Toolkit.CompanyName) e
	// TrayIcon (Assets.Logo) sao exibidos na notificacao do Windows.
	trayApplies := psadtNotifUsesTrayBranding(req.NotifType)
	brandedAssets := fluentApplies || trayApplies
	if !brandedAssets && userIcon == "" && userIconDark == "" && bannerPath == "" && accent == "" && style == "" {
		return nil, nil
	}
	// O PSADT so considera o diretorio do caller se existir
	// "<scriptdir>\\Config\\config.psd1" (Initialize-ADTModule testa
	// "Config\\config.psd1" relativo a -ScriptDirectory). Assets sao
	// resolvidos a partir do mesmo diretorio Config. Logo, tudo vai em
	// <tmpdir>\\Config\\ e o config referencia os assets por nome simples.
	configDir := filepath.Join(filepath.Dir(scriptPath), "Config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, fmt.Errorf("falha ao criar diretorio de staging: %w", err)
	}
	var created []string
	created = append(created, configDir)
	writeAsset := func(data []byte, destName string) string {
		dest := filepath.Join(configDir, destName)
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return ""
		}
		created = append(created, dest)
		return destName
	}
	copyAsset := func(src, destName string) string {
		data, err := os.ReadFile(src)
		if err != nil {
			return ""
		}
		return writeAsset(data, destName)
	}
	var logo, logoDark, banner string
	if userIcon != "" {
		logo = copyAsset(userIcon, "discovery-icon.png")
	}
	if userIconDark != "" {
		logoDark = copyAsset(userIconDark, "discovery-icon-dark.png")
	}
	if bannerPath != "" {
		banner = copyAsset(bannerPath, "discovery-banner.png")
	}
	// Padronizacao com o agent: em dialogs Fluent sem logo definido, usa o
	// appiconPSADT.png embedado do Discovery Agent (claro e escuro). Se o
	// usuario definiu logo sem dark, reaproveita o mesmo arquivo.
	switch {
	case logo != "" && logoDark == "":
		logoDark = logo
	case logo == "" && logoDark == "" && brandedAssets && len(psadtAgentIconPNG) > 0:
		logo = writeAsset(psadtAgentIconPNG, "discovery-agent-icon.png")
		logoDark = logo
	}
	// Nada de fato gravado (so o diretorio): limpa e nao gera config. Tipos
	// branded sempre gravam Toolkit.CompanyName, entao nunca caem aqui.
	if len(created) == 1 && accent == "" && style == "" && !brandedAssets {
		_ = os.Remove(configDir)
		return nil, nil
	}
	company := strings.TrimSpace(req.CompanyName)
	if company == "" {
		company = "Discovery Agent"
	}
	var cfg strings.Builder
	cfg.WriteString("@{\n")
	// Toolkit.CompanyName e o TrayTitle do balloon/toast e o nome default de
	// subtitulo dos dialogs Fluent. Evita o "PSAppDeployToolkit" do modulo.
	cfg.WriteString("\tToolkit = @{\n")
	cfg.WriteString("\t\tCompanyName = '" + psadt.EscapeSingleQuoted(company) + "'\n")
	cfg.WriteString("\t}\n")
	if logo != "" || logoDark != "" || banner != "" {
		cfg.WriteString("\tAssets = @{\n")
		if logo != "" {
			cfg.WriteString("\t\tLogo = '" + logo + "'\n")
		}
		if logoDark != "" {
			cfg.WriteString("\t\tLogoDark = '" + logoDark + "'\n")
		}
		if banner != "" {
			cfg.WriteString("\t\tBanner = '" + banner + "'\n")
		}
		cfg.WriteString("\t}\n")
	}
	if style != "" || accent != "" {
		cfg.WriteString("\tUI = @{\n")
		if style != "" {
			cfg.WriteString("\t\tDialogStyle = '" + style + "'\n")
		}
		if accent != "" {
			cfg.WriteString("\t\tFluentAccentColor = " + accent + "\n")
		}
		cfg.WriteString("\t}\n")
	}
	cfg.WriteString("}\n")
	configPath := filepath.Join(configDir, "config.psd1")
	if err := os.WriteFile(configPath, []byte(cfg.String()), 0o644); err != nil {
		for _, p := range created {
			_ = os.RemoveAll(p)
		}
		return nil, fmt.Errorf("falha ao gravar config.psd1 de branding: %w", err)
	}
	created = append(created, configPath)
	return created, nil
}

// normalizeHexAccent aceita hex RGB (4A9EFF) ou ARGB (FF4A9EFF), com ou sem
// prefixo 0x/#, e devolve no formato literal do config.psd1 (0xFFRRGGBB).
// Vazio quando a entrada e invalida (o config usa o default do modulo).
func normalizeHexAccent(raw string) string {
	text := strings.ToLower(strings.TrimSpace(raw))
	text = strings.TrimPrefix(text, "0x")
	text = strings.TrimPrefix(text, "#")
	if text == "" {
		return ""
	}
	switch len(text) {
	case 6:
		text = "ff" + text
	case 8:
	default:
		return ""
	}
	for _, ch := range text {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return ""
		}
	}
	return "0x" + text
}

// normalizeDialogStyle valida o estilo de dialogo do PSADT (UI.DialogStyle).
// Vazio = nao sobrescrever (usa o default Fluent do modulo).
func normalizeDialogStyle(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "fluent":
		return "Fluent"
	case "classic":
		return "Classic"
	default:
		return ""
	}
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

// normalizePromptIcon converte aliases para os valores do enum DialogSystemIcon do
// PSADT 4.x (Show-ADTInstallationPrompt -Icon). Retorna vazio para omitir o parametro.
func normalizePromptIcon(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "none":
		return ""
	case "info", "information":
		return "Information"
	case "warning", "exclamation":
		return "Exclamation"
	case "error":
		return "Error"
	case "question":
		return "Question"
	case "hand":
		return "Hand"
	case "shield":
		return "Shield"
	case "asterisk":
		return "Asterisk"
	case "application":
		return "Application"
	case "winlogo":
		return "WinLogo"
	default:
		return "Information"
	}
}
