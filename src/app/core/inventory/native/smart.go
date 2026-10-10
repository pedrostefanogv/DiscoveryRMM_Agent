package native

// SmartHealth holds basic disk health/SMART data collected from Windows.
// All fields are optional — when the OS/driver does not expose a value it is
// left zero/empty and the caller decides how to render it.
//
// O tipo é cross-platform de propósito: é usado pela interface Collector e pelo
// Provider, mas a coleta é Windows-only (smart_windows.go). Antes ele vivia no
// arquivo com //go:build windows e o pacote não compilava fora do Windows
// (undefined: SmartHealth).
type SmartHealth struct {
	// HealthStatus from Get-PhysicalDisk: "Healthy" | "Warning" | "Unhealthy" | "".
	HealthStatus string `json:"healthStatus"`
	// TemperatureC in Celsius (0 when unknown).
	TemperatureC int `json:"temperatureC"`
	// PowerOnHours total hours the disk has been powered on.
	PowerOnHours int `json:"powerOnHours"`
	// Wear is the SSD wear percentage (0-100, 0 when unknown).
	Wear int `json:"wear"`
	// ReadErrorsTotal cumulative read errors.
	ReadErrorsTotal int `json:"readErrorsTotal"`
	// WriteErrorsTotal cumulative write errors.
	WriteErrorsTotal int `json:"writeErrorsTotal"`
}
