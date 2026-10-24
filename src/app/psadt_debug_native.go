package app

import (
	_ "embed"
	"strings"
	"time"

	"discovery/app/agentconfig"
)

// ── Console de debug das NOTIFICAÇÕES NATIVAS ──
//
// Substitui o antigo console de debug do PSADT: os bindings preservam o
// propósito (testar notificações/preflight/scripts) mas tudo roda no backend
// nativo (core/notifyui) — nenhum uso de PSAppDeployToolkit.

// psadtAgentIconPNG é o appiconPSADT.png do Discovery Agent (embedado), ícone
// GLOBAL do agente — um único asset para todos os clientes/agentes.

//go:embed assets/psadt/appiconPSADT.png
var psadtAgentIconPNG []byte

// NotificationDebugState é o estado exibido no painel "Estado Atual".
type NotificationDebugState struct {
	RuntimeDebugMode     bool                                        `json:"runtimeDebugMode"`
	Backend              string                                      `json:"backend"`
	Configuration        agentconfig.AgentConfiguration              `json:"configuration"`
	NotificationBranding agentconfig.AgentNotificationBrandingConfig `json:"notificationBranding"`
	NotificationPolicies []agentconfig.AgentNotificationPolicy       `json:"notificationPolicies"`
}

// GetNotificationDebugState devolve o estado do backend de notificações.
func (a *App) GetNotificationDebugState() NotificationDebugState {
	a.Logs.Append("[notify-debug] GetNotificationDebugState chamado")
	cfg := a.GetAgentConfiguration()
	return NotificationDebugState{
		RuntimeDebugMode:     a.RuntimeFlags.DebugMode,
		Backend:              agentconfig.NormalizeNotificationBackend(cfg.Notifications.Backend),
		Configuration:        cfg,
		NotificationBranding: cfg.NotificationBranding, NotificationPolicies: cfg.NotificationPolicies,
	}
}

// NotificationDebugEventRequest é o payload do teste da pipeline de eventos.
type NotificationDebugEventRequest struct {
	Title      string `json:"title"`
	Message    string `json:"message"`
	Mode       string `json:"mode"`
	Severity   string `json:"severity"`
	Layout     string `json:"layout"`
	Accent     string `json:"accent"`
	RequireAck bool   `json:"requireAck"`
}

// EmitNotificationDebugEvent emite um evento pela pipeline de notificações.
func (a *App) EmitNotificationDebugEvent(req NotificationDebugEventRequest) error {
	return a.psadtSvc.EmitDebugNotification(req.Title, req.Message, req.Mode, req.Severity, req.Layout, req.Accent, req.RequireAck)
}

// NativeToolResult representa o resultado de uma ação do console de debug.
type NativeToolResult struct {
	Success  bool   `json:"success"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
	// Result carrega a resposta estruturada do diálogo quando aplicável
	// (texto do botão clicado).
	Result        string `json:"result"`
	Error         string `json:"error"`
	ExecutedAtUTC string `json:"executedAtUtc"`
	DurationMS    int64  `json:"durationMs"`
}

func (a *App) GetPowerShellScriptTemplate() string {
	return a.psadtSvc.GetScriptTemplate()
}

func (a *App) ExecuteCustomPowerShellScript(scriptContent string) NativeToolResult {
	result := a.psadtSvc.ExecuteCustomScript(scriptContent)
	return NativeToolResult{
		Success:       result.Success,
		ExitCode:      result.ExitCode,
		Output:        result.Output,
		Error:         result.Error,
		ExecutedAtUTC: result.ExecutedAtUTC,
		DurationMS:    result.DurationMS,
	}
}

// ── Preflight (nativo) ──

// NativePreflightResult agrupa as pré-checas nativas (psadt_preflight_windows.go).
type NativePreflightResult struct {
	OSName           string `json:"osName"`
	OSVersion        string `json:"osVersion"`
	Architecture     string `json:"architecture"`
	IsAdmin          bool   `json:"isAdmin"`
	RebootPending    bool   `json:"rebootPending"`
	NetworkAvailable bool   `json:"networkAvailable"`
	Success          bool   `json:"success"`
	Error            string `json:"error"`
	CheckedAtUTC     string `json:"checkedAtUtc"`
}

// NativeVisualNotificationRequest define os parâmetros do teste visual do
// console de debug — somente campos que o backend nativo consome.
type NativeVisualNotificationRequest struct {
	NotifType       string `json:"notifType"`
	Title           string `json:"title"`
	Message         string `json:"message"`
	Subtitle        string `json:"subtitle"`
	AppName         string `json:"appName"`
	DurationSeconds int    `json:"durationSeconds"`

	// Prompt (cartão nativo)
	PromptLeftText  string `json:"promptLeftText"`
	PromptRightText string `json:"promptRightText"`
	PromptTimeout   int    `json:"promptTimeout"`

	// Countdown / Welcome / Restart (cartão com contador)
	ButtonText              string `json:"buttonText"`
	ButtonCloseText         string `json:"buttonCloseText"`
	CountdownLabel          string `json:"countdownLabel"`
	CountdownNoDefer        bool   `json:"countdownNoDefer"`
	RestartCountdownSeconds int    `json:"restartCountdownSeconds"`
	AllowDefer              bool   `json:"allowDefer"`
	CloseProcessesCountdown int    `json:"closeProcessesCountdown"`

	// Dialog Box (MessageBoxW nativa)
	DialogButtons string `json:"dialogButtons"`
}

// ExecuteNativeVisualNotification executa a notificação visual pelo backend
// NATIVO (cartão winui / toast do Windows Runtime / MessageBox).
func (a *App) ExecuteNativeVisualNotification(req NativeVisualNotificationRequest) NativeToolResult {
	result := NativeToolResult{ExecutedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	start := time.Now()
	defer func() { result.DurationMS = time.Since(start).Milliseconds() }()

	if strings.TrimSpace(req.Title) == "" {
		req.Title = "Discovery Agent"
	}
	if strings.TrimSpace(req.Message) == "" {
		req.Message = "Teste de notificacao"
	}
	if strings.TrimSpace(req.AppName) == "" {
		req.AppName = "Discovery Agent"
	}

	return executeNativeVisualNotification(req, &result)
}