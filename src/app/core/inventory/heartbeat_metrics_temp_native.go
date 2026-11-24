//go:build windows

package inventory

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/agentconn"
	"discovery/app/core/inventory/native"
)

// ─── PDH (Performance Data Helper) CPU Thermal ────────────────────────
//
// Coleta a temperatura das zonas térmicas via pdh.dll usando wildcard:
//   \Thermal Zone Information(*)\High Precision Temperature
//   \Thermal Zone Information(*)\Temperature
//
// O Windows não expõe instância `_Total` para Thermal Zone Information; as
// instâncias reais são caminhos ACPI (ex.: `\_TZ.TZ10`, `\_TZ.THRM`). Por isso
// usamos PdhGetFormattedCounterArrayW para ler todas as instâncias e escolher
// a leitura válida mais quente.

const (
	pdhErrMoreData      = 0x800007D2 // PDH_MORE_DATA (distinto de pdhMoreData=0x800007D1 do diskio)
	pdhCstatusValidData = 0x00000000
	pdhCstatusNewData   = 0x00000001

	pdhThermalHighPrecCounterPath = `\Thermal Zone Information(*)\High Precision Temperature`
	pdhThermalCounterPath         = `\Thermal Zone Information(*)\Temperature`

	// Quando o contador não existe (ex.: serviço iniciado antes do ACPI
	// publicar as thermal zones), tenta de novo depois deste intervalo.
	thermalPdhInitRetryDelay = 5 * time.Minute
)

var procPdhGetFormattedCounterArrayW = modpdh.NewProc("PdhGetFormattedCounterArrayW")

// pdhFmtCounterValueItemW espelha PDH_FMT_COUNTERVALUE_ITEM_W (24 bytes em x64).
type pdhFmtCounterValueItemW struct {
	Name     *uint16
	FmtValue pdhFmtCounterValue
}

var (
	thermalPdhMu          sync.Mutex
	thermalPdhQuery       uintptr // HQUERY
	thermalPdhCtrHighPrec uintptr // HCOUNTER (High Precision Temperature)
	thermalPdhCtrTemp     uintptr // HCOUNTER (Temperature)
	thermalPdhReady       bool
	thermalPdhInitErr     error
	thermalPdhRetryAt     time.Time
)

func resetThermalPdhLocked(err error) {
	if thermalPdhQuery != 0 {
		procPdhCloseQuery.Call(thermalPdhQuery)
	}
	thermalPdhQuery = 0
	thermalPdhCtrHighPrec = 0
	thermalPdhCtrTemp = 0
	thermalPdhReady = false
	thermalPdhInitErr = err
	thermalPdhRetryAt = time.Now().Add(thermalPdhInitRetryDelay)
}

func addEnglishCounter(hQuery uintptr, path string) uintptr {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var hCtr uintptr
	ret, _, _ := procPdhAddEnglishCounterW.Call(
		hQuery,
		uintptr(unsafe.Pointer(pathPtr)),
		0,
		uintptr(unsafe.Pointer(&hCtr)),
	)
	if ret != 0 {
		return 0
	}
	return hCtr
}

// ensureThermalPdhInit abre a query PDH e adiciona os contadores wildcard de temperatura.
// Thread-safe (mutex). Retorna true se ao menos um contador foi inicializado.
func ensureThermalPdhInit() bool {
	thermalPdhMu.Lock()
	defer thermalPdhMu.Unlock()

	if thermalPdhReady {
		return true
	}

	if thermalPdhInitErr != nil && time.Now().Before(thermalPdhRetryAt) {
		return false
	}

	var hQuery uintptr
	ret, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&hQuery)))
	if ret != 0 {
		thermalPdhInitErr = fmt.Errorf("pdh(thermal): PdhOpenQueryW failed: 0x%X", ret)
		thermalPdhRetryAt = time.Now().Add(thermalPdhInitRetryDelay)
		return false
	}

	hHighPrec := addEnglishCounter(hQuery, pdhThermalHighPrecCounterPath)
	hTemp := addEnglishCounter(hQuery, pdhThermalCounterPath)

	if hHighPrec == 0 && hTemp == 0 {
		procPdhCloseQuery.Call(hQuery)
		thermalPdhInitErr = fmt.Errorf("pdh(thermal): no thermal zone wildcard counters available")
		thermalPdhRetryAt = time.Now().Add(thermalPdhInitRetryDelay)
		return false
	}

	thermalPdhQuery = hQuery
	thermalPdhCtrHighPrec = hHighPrec
	thermalPdhCtrTemp = hTemp
	thermalPdhReady = true
	thermalPdhInitErr = nil
	return true
}

// readPdhCounterArray lê todas as instâncias de um contador PDH wildcard via
// PdhGetFormattedCounterArrayW, aceitando VALID_DATA e NEW_DATA.
func readPdhCounterArray(hCounter uintptr) []native.ThermalReading {
	if hCounter == 0 {
		return nil
	}

	var bufSize uint32
	var itemCount uint32

	ret, _, _ := procPdhGetFormattedCounterArrayW.Call(
		hCounter,
		uintptr(pdhFmtDouble),
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&itemCount)),
		0,
	)
	if ret != pdhErrMoreData || bufSize == 0 || itemCount == 0 {
		return nil
	}

	words := make([]uint64, (bufSize+7)/8)
	bufSize = uint32(len(words) * 8)
	ret, _, _ = procPdhGetFormattedCounterArrayW.Call(
		hCounter,
		uintptr(pdhFmtDouble),
		uintptr(unsafe.Pointer(&bufSize)),
		uintptr(unsafe.Pointer(&itemCount)),
		uintptr(unsafe.Pointer(&words[0])),
	)
	if ret == pdhErrMoreData && bufSize > 0 {
		words = make([]uint64, (bufSize+7)/8)
		bufSize = uint32(len(words) * 8)
		ret, _, _ = procPdhGetFormattedCounterArrayW.Call(
			hCounter,
			uintptr(pdhFmtDouble),
			uintptr(unsafe.Pointer(&bufSize)),
			uintptr(unsafe.Pointer(&itemCount)),
			uintptr(unsafe.Pointer(&words[0])),
		)
	}
	if ret != 0 || itemCount == 0 {
		return nil
	}

	itemSize := unsafe.Sizeof(pdhFmtCounterValueItemW{})
	if uintptr(itemCount)*itemSize > uintptr(len(words)*8) {
		return nil
	}

	items := unsafe.Slice((*pdhFmtCounterValueItemW)(unsafe.Pointer(&words[0])), itemCount)
	readings := make([]native.ThermalReading, 0, len(items))
	for _, item := range items {
		cstatus := item.FmtValue.CStatus
		if cstatus != pdhCstatusValidData && cstatus != pdhCstatusNewData {
			continue
		}
		val := item.FmtValue.DoubleValue
		if math.IsNaN(val) || math.IsInf(val, 0) || val <= 0 {
			continue
		}
		var name string
		if item.Name != nil {
			name = windows.UTF16PtrToString(item.Name)
		}
		readings = append(readings, native.ThermalReading{
			Source:   "pdh",
			Name:     name,
			RawValue: val,
			Unit:     native.ThermalUnitAutoKelvin,
		})
	}
	return readings
}

// collectCPUTemperatureNative retorna a temperatura do processador em °C via PDH.
// Retorna (celsius, ok).
func collectCPUTemperatureNative() (float64, bool) {
	if !ensureThermalPdhInit() {
		return -1, false
	}

	thermalPdhMu.Lock()
	defer thermalPdhMu.Unlock()

	ret, _, _ := procPdhCollectQueryData.Call(thermalPdhQuery)
	if ret != 0 {
		resetThermalPdhLocked(fmt.Errorf("pdh(thermal): PdhCollectQueryData failed: 0x%X", ret))
		return -1, false
	}

	if readings := readPdhCounterArray(thermalPdhCtrHighPrec); len(readings) > 0 {
		if c, ok := selectBestThermalReading(readings); ok {
			return c, true
		}
	}

	if readings := readPdhCounterArray(thermalPdhCtrTemp); len(readings) > 0 {
		if c, ok := selectBestThermalReading(readings); ok {
			return c, true
		}
	}

	return -1, false
}

// ─── Conversão e seleção de leituras térmicas ────────────────────────

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

// normalizeCelsius valida e arredonda uma temperatura já em graus Celsius.
func normalizeCelsius(c float64) (float64, bool) {
	if math.IsNaN(c) || math.IsInf(c, 0) || c < 0 || c > 125 {
		return -1, false
	}
	return roundTo1Decimal(c), true
}

// convertProbeRawToCelsius converte leituras do Win32_TemperatureProbe.
// Alguns drivers/BIOS reportam em décimos de °C (ex.: 450 = 45.0 °C) e outros
// em °C inteiros (ex.: 45).
func convertProbeRawToCelsius(raw float64) (float64, bool) {
	if math.IsNaN(raw) || math.IsInf(raw, 0) || raw <= 0 {
		return -1, false
	}
	celsius := raw
	if raw > 150 && raw <= 1250 {
		celsius = raw / 10.0
	}
	return normalizeCelsius(celsius)
}

func convertReadingToCelsius(r native.ThermalReading) (float64, bool) {
	var c float64
	var ok bool
	switch r.Unit {
	case native.ThermalUnitAutoKelvin:
		c, ok = convertThermalRawToCelsius(r.RawValue)
	case native.ThermalUnitCelsius:
		c, ok = normalizeCelsius(r.RawValue)
	case native.ThermalUnitProbeCelsius:
		c, ok = convertProbeRawToCelsius(r.RawValue)
	default:
		return -1, false
	}
	if !ok || c < 0 {
		return -1, false
	}
	return c, true
}

// selectBestThermalReading converte todas as leituras para Celsius primeiro e
// escolhe a mais quente dentre as válidas, evitando que unidades mistas ou
// sensores inválidos mascarem leituras válidas.
func selectBestThermalReading(readings []native.ThermalReading) (float64, bool) {
	best := -1.0
	found := false
	for _, r := range readings {
		c, ok := convertReadingToCelsius(r)
		if !ok {
			continue
		}
		if !found || c > best {
			best = c
			found = true
		}
	}
	if !found {
		return -1, false
	}
	return best, true
}

// selectBestHardwareMonitorCPUTemp seleciona a melhor leitura de temperatura da CPU
// dentre os sensores do LibreHardwareMonitor / OpenHardwareMonitor.
func selectBestHardwareMonitorCPUTemp(readings []native.ThermalReading) (float64, bool) {
	excludedTokens := []string{
		"gpu", "nvme", "hdd", "ssd", "drive", "disk",
		"vrm", "mosfet", "pch", "chipset", "ambient", "system",
		"motherboard", "battery", "memory", "dimm",
		"distance to", "tjmax", "limit", "threshold", "critical", "graphics",
	}

	bestTier := 0
	bestTemp := -1.0

	for _, r := range readings {
		c, ok := normalizeCelsius(r.RawValue)
		if !ok || c <= 0 {
			continue
		}

		idLower := strings.ToLower(r.Identifier)
		nameLower := strings.ToLower(r.Name)
		combined := idLower + " " + nameLower

		excluded := false
		for _, tok := range excludedTokens {
			if strings.Contains(combined, tok) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}

		isCPU := strings.Contains(idLower, "/intelcpu/") ||
			strings.Contains(idLower, "/amdcpu/") ||
			strings.Contains(idLower, "/cpu/") ||
			strings.Contains(nameLower, "cpu") ||
			strings.Contains(nameLower, "tctl") ||
			strings.Contains(nameLower, "tdie") ||
			strings.Contains(nameLower, "core package") ||
			strings.Contains(nameLower, "core max") ||
			strings.Contains(nameLower, "core average")
		if !isCPU {
			continue
		}

		tier := 1
		switch {
		case strings.Contains(nameLower, "package") ||
			strings.Contains(nameLower, "tctl/tdie") ||
			strings.Contains(nameLower, "tdie") ||
			strings.Contains(nameLower, "tctl") ||
			strings.Contains(nameLower, "core max"):
			tier = 3
		case strings.Contains(nameLower, "core average") ||
			strings.Contains(nameLower, "cpu core") ||
			strings.TrimSpace(nameLower) == "cpu":
			tier = 2
		}

		if tier > bestTier || (tier == bestTier && c > bestTemp) {
			bestTier = tier
			bestTemp = c
		}
	}

	if bestTier == 0 {
		return -1, false
	}
	return bestTemp, true
}

// ─── Orquestração da coleta de temperatura no heartbeat ──────────────

const (
	cpuTempCacheTTL       = 30 * time.Second
	cpuTempFailureBackoff = 5 * time.Minute
	cpuTempCOMTimeout     = 3 * time.Second
)

var (
	hwmTempMu       sync.Mutex
	hwmTempLastAt   time.Time
	hwmTempLastVal  float64 = -1
	hwmTempRetryAt  time.Time
	hwmTempInFlight atomic.Bool

	cpuTempWMIMu       sync.Mutex
	cpuTempWMILastAt   time.Time
	cpuTempWMILastVal  float64 = -1
	cpuTempWMIRetryAt  time.Time
	cpuTempWMIInFlight atomic.Bool
)

// runWithContextSingleFlight executa fn em goroutine respeitando o contexto do
// caller (limitado a timeout) e evitando acúmulo de goroutines se uma chamada
// COM anterior ainda estiver bloqueada.
func runWithContextSingleFlight[T any](ctx context.Context, inFlight *atomic.Bool, timeout time.Duration, fn func() T) (T, bool) {
	var zero T
	if !inFlight.CompareAndSwap(false, true) {
		return zero, false
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ch := make(chan T, 1)
	go func() {
		defer inFlight.Store(false)
		ch <- fn()
	}()

	select {
	case res := <-ch:
		return res, true
	case <-runCtx.Done():
		return zero, false
	}
}

func collectCPUTemperatureHardwareMonitor(ctx context.Context) (float64, bool) {
	if ctx != nil && ctx.Err() != nil {
		return -1, false
	}

	hwmTempMu.Lock()
	if hwmTempLastVal >= 0 && time.Since(hwmTempLastAt) < cpuTempCacheTTL {
		v := hwmTempLastVal
		hwmTempMu.Unlock()
		return v, true
	}
	if time.Now().Before(hwmTempRetryAt) {
		hwmTempMu.Unlock()
		return -1, false
	}
	hwmTempMu.Unlock()

	type hwmResult struct {
		readings []native.ThermalReading
		ok       bool
	}
	res, completed := runWithContextSingleFlight(ctx, &hwmTempInFlight, cpuTempCOMTimeout, func() hwmResult {
		r, ok := native.QueryHardwareMonitorCPUTemps()
		return hwmResult{readings: r, ok: ok}
	})
	if !completed {
		return -1, false
	}
	if !res.ok {
		hwmTempMu.Lock()
		hwmTempRetryAt = time.Now().Add(cpuTempFailureBackoff)
		hwmTempMu.Unlock()
		return -1, false
	}

	celsius, ok := selectBestHardwareMonitorCPUTemp(res.readings)
	if !ok {
		hwmTempMu.Lock()
		hwmTempRetryAt = time.Now().Add(cpuTempFailureBackoff)
		hwmTempMu.Unlock()
		return -1, false
	}

	hwmTempMu.Lock()
	hwmTempLastAt = time.Now()
	hwmTempLastVal = celsius
	hwmTempRetryAt = time.Time{}
	hwmTempMu.Unlock()

	return celsius, true
}

// collectCPUTemperatureWMI retorna a temperatura da CPU em °C via WMI/COM nativo
// com cache de 30s e backoff de 5m em caso de ausência de sensores.
func collectCPUTemperatureWMI(ctx context.Context) (float64, bool) {
	if ctx != nil && ctx.Err() != nil {
		return -1, false
	}

	cpuTempWMIMu.Lock()
	if cpuTempWMILastVal >= 0 && time.Since(cpuTempWMILastAt) < cpuTempCacheTTL {
		v := cpuTempWMILastVal
		cpuTempWMIMu.Unlock()
		return v, true
	}
	if time.Now().Before(cpuTempWMIRetryAt) {
		cpuTempWMIMu.Unlock()
		return -1, false
	}
	cpuTempWMIMu.Unlock()

	readings, completed := runWithContextSingleFlight(ctx, &cpuTempWMIInFlight, cpuTempCOMTimeout, func() []native.ThermalReading {
		return native.QueryACPIThermalZonesWMI()
	})
	if !completed {
		return -1, false
	}

	celsius, ok := selectBestThermalReading(readings)
	if !ok {
		cpuTempWMIMu.Lock()
		cpuTempWMIRetryAt = time.Now().Add(cpuTempFailureBackoff)
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

// collectHeartbeatCPUTemperatureWindowsNative coleta a temperatura da CPU
// e popula o campo CpuTemperatureCelsius nos metrics.
//
// Estratégia de coleta (zero subprocessos):
//  1. LibreHardwareMonitor / OpenHardwareMonitor via COM/WMI (quando presente)
//  2. PDH nativo wildcard (\Thermal Zone Information(*)\High Precision Temperature | Temperature)
//  3. Fallback COM/WMI (MSAcpi_ThermalZoneTemperature + perf class + Win32_TemperatureProbe)
//  4. Se todos falharem, mantém -1 (não disponível)
func collectHeartbeatCPUTemperatureWindowsNative(ctx context.Context, metrics *agentconn.AgentHeartbeatMetrics) {
	if metrics == nil {
		return
	}

	if celsius, ok := collectCPUTemperatureHardwareMonitor(ctx); ok && celsius >= 0 {
		metrics.CpuTemperatureCelsius = celsius
		return
	}

	if celsius, ok := collectCPUTemperatureNative(); ok && celsius >= 0 {
		metrics.CpuTemperatureCelsius = celsius
		return
	}

	if celsius, ok := collectCPUTemperatureWMI(ctx); ok && celsius >= 0 {
		metrics.CpuTemperatureCelsius = celsius
	}
}
