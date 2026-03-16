package printer

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"discovery/internal/processutil"
)

const printerModulePreamble = `$ErrorActionPreference = 'Stop'
Import-Module PrintManagement -ErrorAction Stop`

// Manager encapsulates PowerShell-based printer management operations.
type Manager struct {
	defaultTimeout time.Duration
}

// NewManager creates a printer manager with a default command timeout.
func NewManager(timeout time.Duration) *Manager {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Manager{defaultTimeout: timeout}
}

func (m *Manager) ListPrinters(ctx context.Context) (json.RawMessage, error) {
	script := printerModulePreamble + `
$items = @(Get-Printer | Select-Object Name, DriverName, PortName, Shared, ShareName, Published, ComputerName, Type, PrinterStatus, JobCount, Default)
$items | ConvertTo-Json -Depth 4 -Compress`
	return m.runJSON(ctx, script)
}

func (m *Manager) InstallPrinter(ctx context.Context, name, driverName, portName, portAddress string) (json.RawMessage, error) {
	script, err := buildInstallScript(name, driverName, portName, portAddress)
	if err != nil {
		return nil, err
	}
	return m.runJSON(ctx, script)
}

func (m *Manager) InstallSharedPrinter(ctx context.Context, connectionPath string, setDefault bool) (json.RawMessage, error) {
	script, err := buildSharedInstallScript(connectionPath, setDefault)
	if err != nil {
		return nil, err
	}
	return m.runJSON(ctx, script)
}

func (m *Manager) RemovePrinter(ctx context.Context, name string) (json.RawMessage, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("nome da impressora nao pode ser vazio")
	}

	script := printerModulePreamble + `
$printerName = ` + psLiteral(name) + `
$printer = Get-Printer -Name $printerName -ErrorAction SilentlyContinue
if (-not $printer) {
  throw "impressora nao encontrada: $printerName"
}
Remove-Printer -Name $printerName -ErrorAction Stop
[PSCustomObject]@{
  removed = $true
  name = $printerName
} | ConvertTo-Json -Depth 3 -Compress`
	return m.runJSON(ctx, script)
}

func (m *Manager) GetPrinterConfig(ctx context.Context, name string) (json.RawMessage, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("nome da impressora nao pode ser vazio")
	}

	script := printerModulePreamble + `
$printerName = ` + psLiteral(name) + `
$config = Get-PrintConfiguration -PrinterName $printerName -ErrorAction Stop |
  Select-Object PrinterName, Color, DuplexingMode, Collate, NUp, PaperSize, InputBin
$config | ConvertTo-Json -Depth 4 -Compress`
	return m.runJSON(ctx, script)
}

func (m *Manager) ListPrintJobs(ctx context.Context, printerName string) (json.RawMessage, error) {
	printerName = strings.TrimSpace(printerName)
	if printerName == "" {
		return nil, fmt.Errorf("printerName nao pode ser vazio")
	}

	script := printerModulePreamble + `
$printerName = ` + psLiteral(printerName) + `
$jobs = @(Get-PrintJob -PrinterName $printerName -ErrorAction SilentlyContinue |
  Select-Object ID, PrinterName, DocumentName, JobStatus, SubmittedTime, PagesPrinted, TotalPages, Size, ComputerName, UserName)
$jobs | ConvertTo-Json -Depth 4 -Compress`
	return m.runJSON(ctx, script)
}

func (m *Manager) RemovePrintJob(ctx context.Context, printerName string, jobID int) (json.RawMessage, error) {
	printerName = strings.TrimSpace(printerName)
	if printerName == "" {
		return nil, fmt.Errorf("printerName nao pode ser vazio")
	}
	if jobID <= 0 {
		return nil, fmt.Errorf("jobId deve ser maior que zero")
	}

	script := printerModulePreamble + `
$printerName = ` + psLiteral(printerName) + `
$jobId = ` + fmt.Sprintf("%d", jobID) + `
$job = Get-PrintJob -PrinterName $printerName -ID $jobId -ErrorAction SilentlyContinue
if (-not $job) {
  throw "job nao encontrado para a impressora informada"
}
Remove-PrintJob -PrinterName $printerName -ID $jobId -ErrorAction Stop
[PSCustomObject]@{
  removed = $true
  printerName = $printerName
  jobId = $jobId
} | ConvertTo-Json -Depth 3 -Compress`
	return m.runJSON(ctx, script)
}

func (m *Manager) GetSpoolerStatus(ctx context.Context) (json.RawMessage, error) {
	script := `$ErrorActionPreference = 'Stop'
Get-Service -Name Spooler | Select-Object Name, DisplayName, Status, StartType | ConvertTo-Json -Depth 3 -Compress`
	return m.runJSON(ctx, script)
}

func (m *Manager) RestartSpooler(ctx context.Context) (json.RawMessage, error) {
	script := `$ErrorActionPreference = 'Stop'
Restart-Service -Name Spooler -Force -ErrorAction Stop
Get-Service -Name Spooler | Select-Object Name, DisplayName, Status, StartType | ConvertTo-Json -Depth 3 -Compress`
	return m.runJSON(ctx, script)
}

func (m *Manager) ClearQueue(ctx context.Context, printerName string) (json.RawMessage, error) {
	printerName = strings.TrimSpace(printerName)
	if printerName == "" {
		return nil, fmt.Errorf("printerName nao pode ser vazio")
	}

	script := printerModulePreamble + `
$printerName = ` + psLiteral(printerName) + `
$jobs = @(Get-PrintJob -PrinterName $printerName -ErrorAction SilentlyContinue)
$count = $jobs.Count
if ($count -gt 0) {
  $jobs | Remove-PrintJob -ErrorAction Stop
}
[PSCustomObject]@{
  cleared = $true
  printerName = $printerName
  removedJobs = $count
} | ConvertTo-Json -Depth 3 -Compress`
	return m.runJSON(ctx, script)
}

func (m *Manager) ListDrivers(ctx context.Context) (json.RawMessage, error) {
	script := printerModulePreamble + `
$drivers = @(Get-PrinterDriver | Select-Object Name, Manufacturer, MajorVersion, DriverVersion, InfPath)
$drivers | ConvertTo-Json -Depth 4 -Compress`
	return m.runJSON(ctx, script)
}

func buildInstallScript(name, driverName, portName, portAddress string) (string, error) {
	name = strings.TrimSpace(name)
	driverName = strings.TrimSpace(driverName)
	portName = strings.TrimSpace(portName)
	portAddress = strings.TrimSpace(portAddress)

	if name == "" {
		return "", fmt.Errorf("nome da impressora nao pode ser vazio")
	}
	if driverName == "" {
		return "", fmt.Errorf("driverName nao pode ser vazio")
	}
	if portName == "" {
		return "", fmt.Errorf("portName nao pode ser vazio")
	}

	var builder strings.Builder
	builder.WriteString(printerModulePreamble)
	builder.WriteString("\n$printerName = ")
	builder.WriteString(psLiteral(name))
	builder.WriteString("\n$driverName = ")
	builder.WriteString(psLiteral(driverName))
	builder.WriteString("\n$portName = ")
	builder.WriteString(psLiteral(portName))
	builder.WriteString("\n$portAddress = ")
	builder.WriteString(psLiteral(portAddress))
	builder.WriteString(`
$existingPrinter = Get-Printer -Name $printerName -ErrorAction SilentlyContinue
if ($existingPrinter) {
  throw "impressora ja instalada: $printerName"
}

$existingPort = Get-PrinterPort -Name $portName -ErrorAction SilentlyContinue
if (-not $existingPort) {
  if ([string]::IsNullOrWhiteSpace($portAddress)) {
    throw "porta nao encontrada e portAddress nao informado"
  }
  Add-PrinterPort -Name $portName -PrinterHostAddress $portAddress -ErrorAction Stop | Out-Null
}

$existingDriver = Get-PrinterDriver -Name $driverName -ErrorAction SilentlyContinue
if (-not $existingDriver) {
  Add-PrinterDriver -Name $driverName -ErrorAction Stop | Out-Null
}

Add-Printer -Name $printerName -DriverName $driverName -PortName $portName -ErrorAction Stop | Out-Null
Get-Printer -Name $printerName |
  Select-Object Name, DriverName, PortName, Shared, ShareName, PrinterStatus, JobCount, Default |
  ConvertTo-Json -Depth 4 -Compress`)

	return builder.String(), nil
}

func buildSharedInstallScript(connectionPath string, setDefault bool) (string, error) {
	connectionPath = strings.TrimSpace(connectionPath)
	if !isUNCConnectionPath(connectionPath) {
		return "", fmt.Errorf("connectionPath deve estar no formato \\\\servidor\\impressora")
	}

	shareName := shareNameFromUNC(connectionPath)
	setDefaultPS := "$false"
	if setDefault {
		setDefaultPS = "$true"
	}

	var builder strings.Builder
	builder.WriteString(printerModulePreamble)
	builder.WriteString("\n$connectionPath = ")
	builder.WriteString(psLiteral(connectionPath))
	builder.WriteString("\n$shareName = ")
	builder.WriteString(psLiteral(shareName))
	builder.WriteString("\n$setDefault = ")
	builder.WriteString(setDefaultPS)
	builder.WriteString(`
$existingPrinter = Get-Printer | Where-Object {
  $_.Name -eq $connectionPath -or $_.PortName -eq $connectionPath -or $_.ComputerName -eq $connectionPath -or $_.Name -like "*$shareName*"
} | Select-Object -First 1

if (-not $existingPrinter) {
  Add-Printer -ConnectionName $connectionPath -ErrorAction Stop | Out-Null
}

$printer = Get-Printer | Where-Object {
  $_.Name -eq $connectionPath -or $_.PortName -eq $connectionPath -or $_.ComputerName -eq $connectionPath -or $_.Name -like "*$shareName*"
} | Select-Object -First 1

if (-not $printer) {
  throw "impressora compartilhada instalada, mas nao foi possivel localizar o objeto final para $connectionPath"
}

if ($setDefault) {
  $printer | Set-Printer -IsDefault $true -ErrorAction Stop
  $printer = Get-Printer -Name $printer.Name -ErrorAction SilentlyContinue
}

$printer |
  Select-Object Name, DriverName, PortName, Shared, ShareName, ComputerName, Type, PrinterStatus, JobCount, Default |
  ConvertTo-Json -Depth 4 -Compress`)

	return builder.String(), nil
}

func isUNCConnectionPath(value string) bool {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, `\\`) {
		return false
	}
	if strings.Contains(trimmed, "/") {
		return false
	}

	parts := strings.Split(strings.TrimPrefix(trimmed, `\\`), `\`)
	if len(parts) != 2 {
		return false
	}
	return strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != ""
}

func shareNameFromUNC(connectionPath string) string {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(connectionPath), `\\`), `\`)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func (m *Manager) runJSON(ctx context.Context, script string) (json.RawMessage, error) {
	output, err := m.run(ctx, script)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		trimmed = "null"
	}
	return json.RawMessage(trimmed), nil
}

func (m *Manager) run(ctx context.Context, script string) ([]byte, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("gerenciamento de impressoras suportado apenas no Windows")
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if runCtx == nil {
		runCtx = context.Background()
	}
	if _, hasDeadline := runCtx.Deadline(); !hasDeadline && m.defaultTimeout > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, m.defaultTimeout)
	} else {
		cancel = func() {}
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, "powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("erro ao executar powershell de impressoras: %w | saida: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func psLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
