//go:build windows

package inventory

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"discovery/app/core/agentconn"
	"discovery/app/core/processutil"
)

// ─── PDH (Performance Data Helper) CPU Thermal ────────────────────────
//
// Coleta a temperatura do processador via pdh.dll usando o contador:
//   \Thermal Zone Information(_Total)\Temperature
//
// O contador é um valor absoluto (raw), não uma taxa: não precisa de duas
// amostras para o delta como os contadores de disco. O valor vem em graus
// Kelvin — alguns drivers expõem décimos de Kelvin (ex.: 3180 = 318.0 K).
// convertThermalRawToCelsius aceita os dois formatos.
//
// Nem todo hardware expõe thermal zones no PDH (VMs, desktops sem sensores,
// etc.). Nesses casos, o coletor retorna -1 silenciosamente.

const (
	pdhThermalCounterPath = `\Thermal Zone Information(_Total)\Temperature`

	// Quando o contador não existe (ex.: serviço iniciado antes do ACPI
	// publicar as thermal zones), tenta de novo depois deste intervalo em vez
	// de desistir para sempre.
	thermalPdhInitRetryDelay = 5 * time.Minute
)

var (
	thermalPdhMu      sync.Mutex
	thermalPdhQuery   uintptr // HQUERY
	thermalPdhCtrTemp uintptr // HCOUNTER
	thermalPdhReady   bool
	thermalPdhInitErr error
	thermalPdhRetryAt time.Time
)

// ensureThermalPdhInit abre a query PDH e adiciona o contador de temperatura.
// Thread-safe (mutex). Retorna true se inicializado com sucesso.
func ensureThermalPdhInit() bool {
	thermalPdhMu.Lock()
	defer thermalPdhMu.Unlock()

	if thermalPdhReady {
		return true
	}

	// Falha anterior: respeita o backoff antes de tentar de novo. Ao contrário
	// do comportamento antigo, uma falha não desabilita o coletor para sempre.
	if thermalPdhInitErr != nil && time.Now().Before(thermalPdhRetryAt) {
		return false
	}

	// PdhOpenQueryW(NULL, 0, &hQuery)
	var hQuery uintptr
	ret, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&hQuery)))
	if ret != 0 {
		thermalPdhInitErr = fmt.Errorf("pdh(thermal): PdhOpenQueryW failed: 0x%X", ret)
		thermalPdhRetryAt = time.Now().Add(thermalPdhInitRetryDelay)
		return false
	}

	pathPtr, err := syscall.UTF16PtrFromString(pdhThermalCounterPath)
	if err != nil {
		procPdhCloseQuery.Call(hQuery)
		thermalPdhInitErr = fmt.Errorf("pdh(thermal): UTF16 encoding failed: %w", err)
		thermalPdhRetryAt = time.Now().Add(thermalPdhInitRetryDelay)
		return false
	}

	var hCtr uintptr
	ret, _, _ = procPdhAddEnglishCounterW.Call(
		hQuery,
		uintptr(unsafe.Pointer(pathPtr)),
		0,
		uintptr(unsafe.Pointer(&hCtr)),
	)
	if ret != 0 {
		procPdhCloseQuery.Call(hQuery)
		thermalPdhInitErr = fmt.Errorf("pdh(thermal): PdhAddEnglishCounterW failed (counter not available): 0x%X", ret)
		thermalPdhRetryAt = time.Now().Add(thermalPdhInitRetryDelay)
		return false
	}

	thermalPdhQuery = hQuery
	thermalPdhCtrTemp = hCtr
	thermalPdhReady = true
	thermalPdhInitErr = nil
	return true
}

// getPdhCounterRawDouble lê o valor cru (double) de um contador PDH.
//
// Existe separado de getPdhCounterDouble porque aquele helper é específico de
// percentuais de disco: ele zera valores negativos e LIMITA qualquer valor a
// 100. Aplicado à temperatura (Kelvin, na casa dos milhares) isso corrompia a
// leitura e fazia o coletor sempre cair no fallback. Aqui não há clamp.
func getPdhCounterRawDouble(hCounter uintptr) (float64, bool) {
	if hCounter == 0 {
		return -1, false
	}

	var dwType uint32
	var value pdhFmtCounterValue

	ret, _, _ := procPdhGetFormattedCounterValue.Call(
		hCounter,
		uintptr(pdhFmtDouble),
		uintptr(unsafe.Pointer(&dwType)),
		uintptr(unsafe.Pointer(&value)),
	)
	if ret != 0 || value.CStatus != pdhCstatusOk {
		return -1, false
	}

	result := value.DoubleValue
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return -1, false
	}
	return result, true
}

// collectCPUTemperatureNative retorna a temperatura do processador em °C via PDH.
// Retorna (celsius, ok).
func collectCPUTemperatureNative() (float64, bool) {
	if !ensureThermalPdhInit() {
		return -1, false
	}

	thermalPdhMu.Lock()
	defer thermalPdhMu.Unlock()

	// Coleta a amostra. O contador de temperatura é absoluto, então uma única
	// amostra já é suficiente (diferente dos contadores de % do disco).
	ret, _, _ := procPdhCollectQueryData.Call(thermalPdhQuery)
	if ret != 0 && ret != pdhMoreData {
		return -1, false
	}

	raw, ok := getPdhCounterRawDouble(thermalPdhCtrTemp)
	if !ok {
		return -1, false
	}

	return convertThermalRawToCelsius(raw)
}

// convertThermalRawToCelsius converte a leitura bruta de um sensor térmico
// ACPI/PDH para Celsius.
//
// O WMI MSAcpi_ThermalZoneTemperature documenta décimos de Kelvin
// (ex.: 3180 = 318.0 K = 44.85 °C), formato que o PDH normalmente replica.
// Alguns drivers, porém, expõem graus Kelvin inteiros (ex.: 318). Aceitamos os
// dois formatos: leituras acima de 1000 só fazem sentido como décimos de Kelvin.
// Entradas ambíguas ou fora da faixa plausível (-20 a 125 °C) são rejeitadas.
func convertThermalRawToCelsius(raw float64) (float64, bool) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw <= 0 {
		return -1, false
	}

	kelvin := raw
	if raw > 1000 {
		kelvin = raw / 10.0
	}

	celsius := kelvin - 273.15
	if celsius < -20 || celsius > 125 {
		return -1, false
	}

	return roundTo1Decimal(celsius), true
}

// roundTo1Decimal arredonda para 1 casa decimal.
func roundTo1Decimal(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return -1
	}
	return math.Round(v*10) / 10
}

// collectHeartbeatCPUTemperatureWindowsNative coleta a temperatura da CPU
// e popula o campo CpuTemperatureCelsius nos metrics.
//
// Estratégia de coleta:
//  1. PDH nativo (\Thermal Zone Information(_Total)\Temperature) — zero subprocesso
//  2. Fallback WMI/CIM (MSAcpi_ThermalZoneTemperature + perf class) — cache 60s
//  3. Se ambos falharem, mantém -1 (não disponível)
func collectHeartbeatCPUTemperatureWindowsNative(ctx context.Context, metrics *agentconn.AgentHeartbeatMetrics) {
	if metrics == nil {
		return
	}

	// Tentativa 1: PDH nativo (disponível na maioria dos notebooks/laptops)
	if celsius, ok := collectCPUTemperatureNative(); ok && celsius >= 0 {
		metrics.CpuTemperatureCelsius = celsius
		return
	}

	// Tentativa 2: Fallback WMI/CIM com cache de 60s
	if celsius, ok := collectCPUTemperatureWMI(ctx); ok && celsius >= 0 {
		metrics.CpuTemperatureCelsius = celsius
	}
}

// ─── Fallback WMI/CIM: MSAcpi_ThermalZoneTemperature ─────────────────
//
// Em desktops com placas-mãe que não expõem thermal zones via PDH
// (ex.: Gigabyte AORUS, ASUS ROG, MSI), usamos as classes WMI:
//   - MSAcpi_ThermalZoneTemperature (root/wmi)
//   - Win32_PerfFormattedData_Counters_ThermalZoneInformation (root/cimv2)
//
// Ambos os valores são passados crus para convertThermalRawToCelsius, que
// normaliza Kelvin x décimos de Kelvin.
//
// Cache de 60 segundos para evitar subprocesso em todo heartbeat (15s). Em
// caso de falha há um backoff — não desabilita o fallback para sempre, pois o
// sensor pode voltar a responder após um resume/hot-plug.

const (
	cpuTempWMICacheTTL       = 60 * time.Second
	cpuTempWMIFailureBackoff = 5 * time.Minute
	cpuTempWMIQueryTimeout   = 8 * time.Second
)

var (
	cpuTempWMIMu      sync.Mutex
	cpuTempWMILastAt  time.Time
	cpuTempWMILastVal float64 = -1
	cpuTempWMIRetryAt time.Time
)

// collectCPUTemperatureWMI retorna a temperatura da CPU em °C via WMI/CIM.
// Usa cache de 60s para evitar subprocesso a cada heartbeat (15s).
// Retorna (celsius, ok).
func collectCPUTemperatureWMI(ctx context.Context) (float64, bool) {
	cpuTempWMIMu.Lock()
	// Cache hit: valor recente (60s)
	if cpuTempWMILastVal >= 0 && time.Since(cpuTempWMILastAt) < cpuTempWMICacheTTL {
		v := cpuTempWMILastVal
		cpuTempWMIMu.Unlock()
		return v, true
	}
	// Backoff de falha: evita disparar PowerShell a cada heartbeat quando o
	// sensor não existe, sem impedir uma nova tentativa mais tarde.
	if time.Now().Before(cpuTempWMIRetryAt) {
		cpuTempWMIMu.Unlock()
		return -1, false
	}
	cpuTempWMIMu.Unlock()

	celsius, err := queryThermalZoneWMI(ctx)
	if err != nil {
		cpuTempWMIMu.Lock()
		cpuTempWMIRetryAt = time.Now().Add(cpuTempWMIFailureBackoff)
		cpuTempWMIMu.Unlock()
		return -1, false
	}

	cpuTempWMIMu.Lock()
	cpuTempWMILastAt = time.Now()
	cpuTempWMILastVal = celsius
	cpuTempWMIRetryAt = time.Time{}
	cpuTempWMIMu.Unlock()

	return celsius, true
}

// queryThermalZoneWMI consulta a temperatura da CPU via WMI/CIM.
//
// Consulta duas classes e escolhe a leitura mais quente (a zona mais próxima
// do processador costuma ser a mais quente). O valor é devolvido cru; a
// conversão de Kelvin fica em convertThermalRawToCelsius.
func queryThermalZoneWMI(parent context.Context) (float64, error) {
	// Respeita o orçamento do heartbeat (5s): se o contexto do caller já
	// estiver mais curto, ele prevalece — evita segurar o shutdown.
	ctx, cancel := context.WithTimeout(parent, cpuTempWMIQueryTimeout)
	defer cancel()

	script := `$ErrorActionPreference = 'SilentlyContinue'
$candidates = New-Object System.Collections.Generic.List[double]

# Classe ACPI clássica (root/wmi): CurrentTemperature em décimos de Kelvin.
$acpi = Get-CimInstance -Namespace root/wmi -ClassName MSAcpi_ThermalZoneTemperature -ErrorAction SilentlyContinue
foreach ($z in $acpi) {
    if ($null -ne $z -and $z.CurrentTemperature -gt 0) {
        $candidates.Add([double]$z.CurrentTemperature)
    }
}

# Classe de performance (root/cimv2), equivalente ao contador PDH.
$perf = Get-CimInstance -ClassName Win32_PerfFormattedData_Counters_ThermalZoneInformation -ErrorAction SilentlyContinue
foreach ($p in $perf) {
    if ($null -eq $p) { continue }
    if ($p.HighPrecisionTemperature -gt 0) {
        $candidates.Add([double]$p.HighPrecisionTemperature)
    } elseif ($p.Temperature -gt 0) {
        $candidates.Add([double]$p.Temperature)
    }
}

if ($candidates.Count -eq 0) {
    'N/A'
} else {
    ($candidates | Sort-Object -Descending | Select-Object -First 1).ToString([System.Globalization.CultureInfo]::InvariantCulture)
}`

	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return -1, fmt.Errorf("wmi(cpu_temp): exec failed: %w", err)
	}

	raw := strings.TrimSpace(string(output))
	if raw == "" || raw == "N/A" {
		return -1, fmt.Errorf("wmi(cpu_temp): no thermal zone data available")
	}

	// A saída pode conter múltiplas linhas (avisos); usamos a última não vazia.
	line := raw
	if idx := strings.LastIndexAny(raw, "\r\n"); idx >= 0 {
		line = strings.TrimSpace(raw[idx+1:])
	}
	if line == "" || line == "N/A" {
		return -1, fmt.Errorf("wmi(cpu_temp): no thermal zone data available")
	}

	value, err := strconv.ParseFloat(line, 64)
	if err != nil {
		return -1, fmt.Errorf("wmi(cpu_temp): invalid value %q: %w", raw, err)
	}

	celsius, ok := convertThermalRawToCelsius(value)
	if !ok {
		return -1, fmt.Errorf("wmi(cpu_temp): value out of range: raw=%v", value)
	}

	return celsius, nil
}
