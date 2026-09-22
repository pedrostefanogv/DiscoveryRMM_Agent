//go:build windows

package native

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"discovery/app/core/processutil"
)

// ScheduledTaskEdit contém os campos editáveis de uma tarefa agendada.
type ScheduledTaskEdit struct {
	TriggerType  string // daily | weekly | once | logon | boot
	Time         string // "HH:mm" (daily/weekly) ou "yyyy-MM-dd HH:mm" (once)
	DaysOfWeek   []int  // 0=Dom..6=Sáb (apenas weekly)
	DaysInterval int    // intervalo em dias (apenas daily > 1)
	// Ação (opcional): quando ActionPath != "", também substitui o que a
	// tarefa executa.
	ActionPath string
	ActionArgs string
}

// psQuote escapa uma string para uso dentro de aspas simples no PowerShell.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(strings.TrimSpace(s), "'", "''") + "'"
}

func runPowerShellCommand(ctx context.Context, script string, timeout time.Duration) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func taskPathNameArgs(taskPath, taskName string) (string, string) {
	p := strings.TrimSpace(taskPath)
	if p == "" {
		p = "\\"
	}
	return p, strings.TrimSpace(taskName)
}

// SetScheduledTaskState habilita/desabilita uma tarefa do Task Scheduler.
func SetScheduledTaskState(ctx context.Context, enable bool, taskPath, taskName string) error {
	p, n := taskPathNameArgs(taskPath, taskName)
	if n == "" {
		return fmt.Errorf("nome da tarefa é obrigatório")
	}
	cmdlet := "Disable-ScheduledTask"
	if enable {
		cmdlet = "Enable-ScheduledTask"
	}
	script := fmt.Sprintf("$ErrorActionPreference = 'Stop'\ntry { %s -TaskPath %s -TaskName %s | Out-Null; 'OK' } catch { Write-Error $_; exit 1 }",
		cmdlet, psQuote(p), psQuote(n))
	if _, err := runPowerShellCommand(ctx, script, 30*time.Second); err != nil {
		return fmt.Errorf("falha ao %s tarefa %s%s: %w", stateVerb(enable), p, n, err)
	}
	return nil
}

func stateVerb(enable bool) string {
	if enable {
		return "habilitar"
	}
	return "desabilitar"
}

// RunScheduledTask dispara uma tarefa agendada imediatamente.
func RunScheduledTask(ctx context.Context, taskPath, taskName string) error {
	p, n := taskPathNameArgs(taskPath, taskName)
	if n == "" {
		return fmt.Errorf("nome da tarefa é obrigatório")
	}
	script := fmt.Sprintf("$ErrorActionPreference = 'Stop'\ntry { Start-ScheduledTask -TaskPath %s -TaskName %s; 'OK' } catch { Write-Error $_; exit 1 }", psQuote(p), psQuote(n))
	if _, err := runPowerShellCommand(ctx, script, 30*time.Second); err != nil {
		return fmt.Errorf("falha ao executar tarefa %s%s: %w", p, n, err)
	}
	return nil
}

// DeleteScheduledTask remove uma tarefa agendada (irreversível).
func DeleteScheduledTask(ctx context.Context, taskPath, taskName string) error {
	p, n := taskPathNameArgs(taskPath, taskName)
	if n == "" {
		return fmt.Errorf("nome da tarefa é obrigatório")
	}
	script := fmt.Sprintf("$ErrorActionPreference = 'Stop'\ntry { Unregister-ScheduledTask -TaskPath %s -TaskName %s -Confirm:$false; 'OK' } catch { Write-Error $_; exit 1 }", psQuote(p), psQuote(n))
	if _, err := runPowerShellCommand(ctx, script, 30*time.Second); err != nil {
		return fmt.Errorf("falha ao excluir tarefa %s%s: %w", p, n, err)
	}
	return nil
}

// EditScheduledTask substitui o gatilho (e opcionalmente a ação) da tarefa.
// Os gatilhos suportados cobrem os casos de uso de inicialização/agenda mais
// comuns: daily, weekly (com dias), once, logon e boot.
func EditScheduledTask(ctx context.Context, taskPath, taskName string, edit ScheduledTaskEdit) error {
	p, n := taskPathNameArgs(taskPath, taskName)
	if n == "" {
		return fmt.Errorf("nome da tarefa é obrigatório")
	}

	trigger := strings.ToLower(strings.TrimSpace(edit.TriggerType))
	var triggerPS string
	switch trigger {
	case "daily":
		interval := ""
		if edit.DaysInterval > 1 {
			interval = fmt.Sprintf(" -DaysInterval %d", edit.DaysInterval)
		}
		triggerPS = fmt.Sprintf("$t = New-ScheduledTaskTrigger -Daily%s -At %s", interval, psQuote(normalizeTimeArg(edit.Time)))
	case "weekly":
		days, err := weeklyDaysArg(edit.DaysOfWeek)
		if err != nil {
			return err
		}
		triggerPS = fmt.Sprintf("$t = New-ScheduledTaskTrigger -Weekly -At %s -DaysOfWeek %s", psQuote(normalizeTimeArg(edit.Time)), days)
	case "once":
		startAt := strings.TrimSpace(edit.Time)
		if startAt == "" {
			return fmt.Errorf("data/hora é obrigatória para gatilho 'once'")
		}
		triggerPS = fmt.Sprintf("$t = New-ScheduledTaskTrigger -Once -At %s", psQuote(startAt))
	case "logon":
		triggerPS = "$t = New-ScheduledTaskTrigger -AtLogOn"
	case "boot":
		triggerPS = "$t = New-ScheduledTaskTrigger -AtStartup"
	default:
		return fmt.Errorf("tipo de gatilho não suportado para edição: %q", trigger)
	}

	var sb strings.Builder
	sb.WriteString("$ErrorActionPreference = 'Stop'\ntry {\n")
	sb.WriteString(triggerPS + "\n")
	sb.WriteString("Set-ScheduledTask -TaskPath " + psQuote(p) + " -TaskName " + psQuote(n) + " -Trigger $t | Out-Null\n")
	if actionPath := strings.TrimSpace(edit.ActionPath); actionPath != "" {
		sb.WriteString("$a = New-ScheduledTaskAction -Execute " + psQuote(actionPath))
		if args := strings.TrimSpace(edit.ActionArgs); args != "" {
			sb.WriteString(" -Argument " + psQuote(args))
		}
		sb.WriteString("\nSet-ScheduledTask -TaskPath " + psQuote(p) + " -TaskName " + psQuote(n) + " -Action $a | Out-Null\n")
	}
	sb.WriteString("'OK'\n} catch { Write-Error $_; exit 1 }")

	if _, err := runPowerShellCommand(ctx, sb.String(), 60*time.Second); err != nil {
		return fmt.Errorf("falha ao editar tarefa %s%s: %w", p, n, err)
	}
	return nil
}

// weeklyDaysArg converte índices de dia (0=Dom..6=Sáb) para nomes aceitos
// pelo parâmetro -DaysOfWeek do PowerShell.
func weeklyDaysArg(days []int) (string, error) {
	names := []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	parts := make([]string, 0, len(days))
	for _, d := range days {
		if d < 0 || d > 6 {
			return "", fmt.Errorf("dia da semana inválido: %d", d)
		}
		parts = append(parts, names[d])
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("selecione pelo menos um dia da semana para gatilho semanal")
	}
	return strings.Join(parts, ","), nil
}

// normalizeTimeArg valida/normaliza o horário ("HH:mm" — formato aceito por
// New-ScheduledTaskTrigger).
func normalizeTimeArg(t string) string {
	return strings.TrimSpace(t)
}
