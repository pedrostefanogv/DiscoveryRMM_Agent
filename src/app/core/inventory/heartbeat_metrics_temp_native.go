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
// O valor retornado está em décimos de Kelvin (ex.: 3400 = 340.0 K = 66.85 °C).
// A primeira chamada após init serve como baseline; chamadas subsequentes
// retornam o valor atual.
//
// Nem todo hardware expõe thermal zones no PDH (VMs, desktops sem sensores,
// etc.). Nesses casos, o coletor retorna -1 silenciosamente.

const (
	pdhThermalCounterPath = `\Thermal Zone Information(_Total)\Temperature`
)

var (
	thermalPdhMu      sync.Mutex
	thermalPdhQuery   uintptr // HQUERY
	thermalPdhCtrTemp uintptr // HCOUNTER
	thermalPdhReady   bool
	thermalPdhSeeded  bool
	thermalPdhInitErr error
)

// ensureThermalPdhInit abre a query PDH e adiciona o contador de temperatura.
// Thread-safe (mutex). Retorna true se inicializado com sucesso.
func ensureThermalPdhInit() bool {
	thermalPdhMu.Lock()
	defer thermalPdhMu.Unlock()

	if thermalPdhReady {
		return true
	}
	if thermalPdhInitErr != nil {
		return false
	}

	// PdhOpenQueryW(NULL, 0, &hQuery)
	var hQuery uintptr
	ret, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&hQuery)))
	if ret != 0 {
		thermalPdhInitErr = fmt.Errorf("pdh(thermal): PdhOpenQueryW failed: 0x%X", ret)
		return false
	}

	pathPtr, err := syscall.UTF16PtrFromString(pdhThermalCounterPath)
	if err != nil {
		procPdhCloseQuery.Call(hQuery)
		thermalPdhInitErr = fmt.Errorf("pdh(thermal): UTF16 encoding failed: %w", err)
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
		return false
	}

	thermalPdhQuery = hQuery
	thermalPdhCtrTemp = hCtr
	thermalPdhReady = true
	return true
}

// collectCPUTemperatureNative retorna a temperatura do processador em °C via PDH.
// Retorna (celsius, ok).
//
// A primeira chamada semeia baseline e retorna os valores como medição instantânea
// (ao contrário do disk I/O que precisa de delta entre amostras, a temperatura
// é um valor absoluto — o PDH retorna o snapshot atual).
func collectCPUTemperatureNative() (float64, bool) {
	if !ensureThermalPdhInit() {
		return -1, false
	}

	thermalPdhMu.Lock()
	defer thermalPdhMu.Unlock()

	// Coleta a amostra
	ret, _, _ := procPdhCollectQueryData.Call(thermalPdhQuery)
	if ret != 0 && ret != pdhMoreData {
		return -1, false
	}

	thermalPdhSeeded = true

	// Lê o valor do contador (Kelvin * 10)
	kelvinDecimos := getPdhCounterDouble(thermalPdhCtrTemp)
	if kelvinDecimos < 0 {
		return -1, false
	}

	// Converte Kelvin*10 → Celsius
	celsius := (kelvinDecimos / 10.0) - 273.15

	// Validação de sanidade: temperatura de CPU plausível (-20 a 125 °C)
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
//  2. Fallback WMI/CIM (MSAcpi_ThermalZoneTemperature via root/wmi) — com cache 60s
//  3. Se ambos falharem, mantém -1 (não disponível)
func collectHeartbeatCPUTemperatureWindowsNative(metrics *agentconn.AgentHeartbeatMetrics) {
	if metrics == nil {
		return
	}

	// Tentativa 1: PDH nativo (disponível na maioria dos notebooks/laptops)
	celsius, ok := collectCPUTemperatureNative()
	if ok && celsius >= 0 {
		metrics.CpuTemperatureCelsius = normalizeHeartbeatPercent(celsius)
		return
	}

	// Tentativa 2: Fallback WMI/CIM com cache de 60s
	celsius, ok = collectCPUTemperatureWMI()
	if ok && celsius >= 0 {
		metrics.CpuTemperatureCelsius = normalizeHeartbeatPercent(celsius)
	}
}

// ─── Fallback WMI/CIM: MSAcpi_ThermalZoneTemperature ─────────────────
//
// Em desktops com placas-mãe que não expõem thermal zones via PDH
// (ex.: Gigabyte AORUS, ASUS ROG, MSI), usamos a classe WMI
// MSAcpi_ThermalZoneTemperature no namespace root/wmi.
//
// O valor retornado está em décimos de Kelvin (CurrentTemperature / 10 - 273.15 = °C),
// mesmo formato do PDH nativo.
//
// Cache de 60 segundos para evitar subprocesso em todo heartbeat (15s).

var (
	cpuTempWMIMu      sync.Mutex
	cpuTempWMILastAt  time.Time
	cpuTempWMILastVal float64 = -1
	cpuTempWMIFailed  bool    // evita retry incessante quando o WMI não existe
)

// collectCPUTemperatureWMI retorna a temperatura da CPU em °C via WMI/CIM.
// Usa cache de 60s para evitar subprocesso a cada heartbeat (15s).
// Retorna (celsius, ok).
func collectCPUTemperatureWMI() (float64, bool) {
	cpuTempWMIMu.Lock()

	// Cache hit: valor recente (60s)
	if time.Since(cpuTempWMILastAt) < 60*time.Second && cpuTempWMILastVal >= 0 {
		v := cpuTempWMILastVal
		cpuTempWMIMu.Unlock()
		return v, true
	}

	// Se já falhou antes, não tenta de novo (WMI não disponível nesta máquina)
	if cpuTempWMIFailed {
		cpuTempWMIMu.Unlock()
		return -1, false
	}
	cpuTempWMIMu.Unlock()

	// Consulta WMI/CIM
	celsius, err := queryThermalZoneWMI()
	if err != nil {
		return -1, false
	}

	cpuTempWMIMu.Lock()
	cpuTempWMILastAt = time.Now()
	cpuTempWMILastVal = celsius
	cpuTempWMIMu.Unlock()

	return celsius, true
}

// queryThermalZoneWMI consulta a temperatura da CPU via WMI/CIM.
//
// Usa Get-CimInstance contra a classe MSAcpi_ThermalZoneTemperature
// no namespace root/wmi. Essa classe expõe a temperatura dos sensores
// térmicos ACPI, presente na maioria dos desktops modernos.
//
// O valor CurrentTemperature está em décimos de Kelvin.
// Retorna (celsius, nil) em caso de sucesso.
func queryThermalZoneWMI() (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Script PowerShell que consulta a temperatura via CIM.
	// MSAcpi_ThermalZoneTemperature retorna CurrentTemperature em décimos de Kelvin.
	// Selecionamos o maior valor (hottest zone) como temperatura do processador.
	script := `$ErrorActionPreference = 'Stop'
$temp = Get-CimInstance -Namespace root/wmi -ClassName MSAcpi_ThermalZoneTemperature -ErrorAction SilentlyContinue |
    Sort-Object CurrentTemperature -Descending |
    Select-Object -First 1 -ExpandProperty CurrentTemperature -ErrorAction SilentlyContinue
if ($null -eq $temp -or $temp -le 0) { 'N/A' } else { $temp.ToString([System.Globalization.CultureInfo]::InvariantCulture) }`

	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// WMI não disponível ou falhou — marca como failed para evitar retry
		cpuTempWMIMu.Lock()
		cpuTempWMIFailed = true
		cpuTempWMIMu.Unlock()
		return -1, fmt.Errorf("wmi(cpu_temp): exec failed: %w", err)
	}

	raw := strings.TrimSpace(string(output))
	if raw == "" || raw == "N/A" {
		cpuTempWMIMu.Lock()
		cpuTempWMIFailed = true
		cpuTempWMIMu.Unlock()
		return -1, fmt.Errorf("wmi(cpu_temp): no thermal zone data available")
	}

	kelvinDecimos, err := strconv.ParseFloat(raw, 64)
	if err != nil || kelvinDecimos <= 0 {
		cpuTempWMIMu.Lock()
		cpuTempWMIFailed = true
		cpuTempWMIMu.Unlock()
		return -1, fmt.Errorf("wmi(cpu_temp): invalid value %q: %w", raw, err)
	}

	// Converte Kelvin*10 → Celsius
	celsius := (kelvinDecimos / 10.0) - 273.15

	// Validação de sanidade: temperatura de CPU plausível (-20 a 125 °C)
	if celsius < -20 || celsius > 125 {
		return -1, fmt.Errorf("wmi(cpu_temp): value out of range: %.1f°C", celsius)
	}

	return roundTo1Decimal(celsius), nil
}
