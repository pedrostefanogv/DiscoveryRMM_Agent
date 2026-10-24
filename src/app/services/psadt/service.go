// Package psadt mantém APENAS os utilitários legados do console de debug que
// sobreviveram à substituição do PSADT por mecanismo nativo:
//   - EmitDebugNotification: dispatch pela pipeline nativa de notificações;
//   - GetScriptTemplate: template genérico de script PowerShell;
//   - ExecuteCustomScript: runner genérico de PowerShell (console de debug).
package psadt

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// ScriptResult representa o resultado da execução de um script.
type ScriptResult struct {
	Success       bool   `json:"success"`
	ExitCode      int    `json:"exitCode"`
	Output        string `json:"output"`
	Error         string `json:"error"`
	ExecutedAtUTC string `json:"executedAtUtc"`
	DurationMS    int64  `json:"durationMs"`
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

// Deps são as dependências injetadas.
type Deps struct {
	// Logf appends a log line.
	Logf func(string)
	// DispatchNotification dispatches a notification.
	DispatchNotification func(NotificationRequest) NotificationResponse
}

// Service encapsula os utilitários legados.
type Service struct {
	logf                 func(string)
	dispatchNotification func(NotificationRequest) NotificationResponse
}

// New cria o Service.
func New(deps Deps) *Service {
	return &Service{
		logf:                 deps.Logf,
		dispatchNotification: deps.DispatchNotification,
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

// EmitDebugNotification emite uma notificação pela pipeline nativa.
func (s *Service) EmitDebugNotification(title, message, mode, severity, layout, accent string, requireAck bool) error {
	if strings.TrimSpace(title) == "" {
		title = "Teste PSADT"
	}
	if strings.TrimSpace(message) == "" {
		message = "Notificação de teste"
	}
	if strings.TrimSpace(mode) == "" {
		mode = "notify_only"
	}
	if strings.TrimSpace(severity) == "" {
		severity = "medium"
	}
	if strings.TrimSpace(layout) == "" {
		layout = "toast"
	}
	notificationID := fmt.Sprintf("psadt-debug-%d", time.Now().UnixNano())
	resp := s.dispatchNotification(NotificationRequest{
		NotificationID: notificationID,
		Title:          title,
		Message:        message,
		Mode:           mode,
		Severity:       severity,
		EventType:      "psadt_debug_runtime",
		Layout:         layout,
		TimeoutSeconds: 45,
		Metadata: map[string]any{
			"source":     "psadt-debug",
			"accent":     accent,
			"requireAck": requireAck,
		},
	})
	if !resp.Accepted {
		return fmt.Errorf("notificação rejeitada: %s", strings.TrimSpace(resp.Message))
	}
	s.logf("[notification] evento PSADT emitido via dispatch id=" + notificationID)
	return nil
}

// GetScriptTemplate retorna um template genérico de script PowerShell.
func (s *Service) GetScriptTemplate() string {
	return `# Script de exemplo (executa via PowerShell -Command)\n` +
			`Write-Host "Discovery Agent - script de teste"\n` +
			`$dest = Join-Path $env:ProgramFiles "DiscoveryTest"\n` +
			`New-Item -ItemType Directory -Path $dest -Force | Out-Null\n` +
			`Write-Host "Criado: $dest"\n` +
			`exit 0\n`
}

// ExecuteCustomScript executa um script PowerShell genérico (console de debug).
func (s *Service) ExecuteCustomScript(scriptContent string) ScriptResult {
	result := ScriptResult{ExecutedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	if runtime.GOOS != "windows" {
		result.Success = false
		result.Error = "suportado apenas em Windows"
		result.ExitCode = 1
		s.logf("[psadt] custom script ignorado: não é Windows")
		return result
	}
	if strings.TrimSpace(scriptContent) == "" {
		result.Success = false
		result.Error = "Script vazio"
		result.ExitCode = 1
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

// hideWindow esconde a janela do processo (no-op em não-Windows;
// hide_windows.go sobrescreve via init).
var hideWindow = func(cmd *exec.Cmd) {}