//go:build windows

package native

import (
	"runtime"
	"sync"
	"time"
)

// ThermalUnit identifica a unidade de medida de uma leitura térmica bruta.
type ThermalUnit int

const (
	// ThermalUnitAutoKelvin indica Kelvin ou décimos de Kelvin (ACPI / PDH).
	ThermalUnitAutoKelvin ThermalUnit = iota
	// ThermalUnitCelsius indica graus Celsius diretos (LibreHardwareMonitor / OpenHardwareMonitor).
	ThermalUnitCelsius
	// ThermalUnitProbeCelsius indica Win32_TemperatureProbe (décimos de °C ou °C inteiros).
	ThermalUnitProbeCelsius
)

// ThermalReading representa uma leitura bruta de temperatura de um sensor Windows.
type ThermalReading struct {
	Source     string
	Name       string
	Identifier string
	RawValue   float64
	Unit       ThermalUnit
}

const hwMonitorNamespaceRetryDelay = 5 * time.Minute

var (
	hwMonitorMu      sync.Mutex
	hwMonitorRetryAt = map[string]time.Time{}
)

// wmiQueryLocked executa wmiQuery fixando a goroutine à thread OS atual para
// garantir que CoInitializeEx e CoUninitialize sejam pareados na mesma thread.
func wmiQueryLocked(namespace, query string) ([]map[string]any, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return wmiQuery(namespace, query)
}

// QueryHardwareMonitorCPUTemps consulta os namespaces WMI do LibreHardwareMonitor
// e do OpenHardwareMonitor por sensores de temperatura. Namespaces ausentes entram
// em backoff de 5 minutos para evitar overhead nos heartbeats seguintes.
func QueryHardwareMonitorCPUTemps() ([]ThermalReading, bool) {
	namespaces := []struct {
		ns     string
		source string
	}{
		{ns: `root\LibreHardwareMonitor`, source: "librehwm"},
		{ns: `root\OpenHardwareMonitor`, source: "openhwm"},
	}

	const query = "SELECT Name, Value, Identifier, SensorType FROM Sensor WHERE SensorType = 'Temperature'"

	for _, item := range namespaces {
		hwMonitorMu.Lock()
		retryAt := hwMonitorRetryAt[item.ns]
		hwMonitorMu.Unlock()

		if !retryAt.IsZero() && time.Now().Before(retryAt) {
			continue
		}

		rows, err := wmiQueryLocked(item.ns, query)
		if err != nil {
			hwMonitorMu.Lock()
			hwMonitorRetryAt[item.ns] = time.Now().Add(hwMonitorNamespaceRetryDelay)
			hwMonitorMu.Unlock()
			continue
		}

		hwMonitorMu.Lock()
		delete(hwMonitorRetryAt, item.ns)
		hwMonitorMu.Unlock()

		if len(rows) == 0 {
			continue
		}

		readings := make([]ThermalReading, 0, len(rows))
		for _, row := range rows {
			readings = append(readings, ThermalReading{
				Source:     item.source,
				Name:       wmiString(row, "Name"),
				Identifier: wmiString(row, "Identifier"),
				RawValue:   wmiFloat(row, "Value"),
				Unit:       ThermalUnitCelsius,
			})
		}
		if len(readings) > 0 {
			return readings, true
		}
	}

	return nil, false
}

// QueryACPIThermalZonesWMI consulta classes WMI nativas de temperatura ACPI,
// contadores de zona térmica e Win32_TemperatureProbe via COM, sem subprocesso.
func QueryACPIThermalZonesWMI() []ThermalReading {
	var readings []ThermalReading

	if rows, err := wmiQueryLocked(`root\wmi`, "SELECT InstanceName, CurrentTemperature FROM MSAcpi_ThermalZoneTemperature"); err == nil {
		for _, row := range rows {
			val := wmiFloat(row, "CurrentTemperature")
			if val <= 0 {
				continue
			}
			readings = append(readings, ThermalReading{
				Source:   "wmi_acpi",
				Name:     wmiString(row, "InstanceName"),
				RawValue: val,
				Unit:     ThermalUnitAutoKelvin,
			})
		}
	}

	if rows, err := wmiQueryLocked(`root\cimv2`, "SELECT Name, HighPrecisionTemperature, Temperature FROM Win32_PerfFormattedData_Counters_ThermalZoneInformation"); err == nil {
		for _, row := range rows {
			name := wmiString(row, "Name")
			highPrec := wmiFloat(row, "HighPrecisionTemperature")
			if highPrec > 0 {
				readings = append(readings, ThermalReading{
					Source:   "wmi_perf",
					Name:     name,
					RawValue: highPrec,
					Unit:     ThermalUnitAutoKelvin,
				})
				if highPrec >= 2530 && highPrec <= 3982 {
					continue
				}
			}
			temp := wmiFloat(row, "Temperature")
			if temp > 0 {
				readings = append(readings, ThermalReading{
					Source:   "wmi_perf",
					Name:     name,
					RawValue: temp,
					Unit:     ThermalUnitAutoKelvin,
				})
			}
		}
	}

	if rows, err := wmiQueryLocked(`root\cimv2`, "SELECT Name, CurrentReading FROM Win32_TemperatureProbe"); err == nil {
		for _, row := range rows {
			val := wmiFloat(row, "CurrentReading")
			if val <= 0 {
				continue
			}
			readings = append(readings, ThermalReading{
				Source:   "wmi_probe",
				Name:     wmiString(row, "Name"),
				RawValue: val,
				Unit:     ThermalUnitProbeCelsius,
			})
		}
	}

	return readings
}
