//go:build windows

package screen

import (
	"log"
	"sync"
	"syscall"
	"unsafe"
)

// ── DPI awareness (PLANO_ACESSO_REMOTO: mouse/captura corretos com DPI
// scaling 100/125/150%) ──
//
// O binário do agente embute manifest PerMonitorV2 (build/windows/
// wails.exe.manifest), mas o discovery-service.exe usa manifest mínimo SEM
// declaração de DPI e qualquer spawn/binário sem manifest roda DPI-UNAWARE.
// Nesse estado o Windows VIRTUALIZA GetSystemMetrics/EnumDisplayMonitors/
// GetCursorPos/SetCursorPos (ex.: tela 1920x1080 com escala 125% passa a
// reportar 1536x864 lógico) enquanto os frames capturados (DXGI/GDI) continuam
// em pixels FÍSICOS. Essa mistura de espaços desloca o cursor publicado ao
// viewer, borra a captura GDI e quebra a normalização do input do acesso
// remoto — exatamente o sintoma "não funciona com escala 125%".
//
// EnsurePerMonitorDPIAwareness força awareness per-monitor no processo atual.
// Idempotente (uma única tentativa por processo, via sync.Once). Ordem:
//  1. já é per-monitor (manifest/shcore) → nada a fazer;
//  2. SetProcessDpiAwarenessContext(PER_MONITOR_AWARE_V2) — Win10 1703+;
//  3. SetProcessDpiAwareness(PER_MONITOR_DPI_AWARE) — Win8.1+ (shcore);
//  4. SetProcessDPIAware() — Vista+ (system aware; coordenadas físicas do
//     monitor primário — melhor que DPI-unaware).
//
// Retorna true quando o processo garante coordenadas FÍSICAS (não
// virtualizadas). Chamar ANTES de criar janelas/DCs ou enumerar monitores.
var (
	dpiOnce        sync.Once
	dpiPhysical    bool
	dpiAwareSource string
)

// Procs Win32 para DPI awareness.
var (
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procShcoreSetProcessDpiAwareness  = syscall.NewLazyDLL("shcore.dll").NewProc("SetProcessDpiAwareness")
	procShcoreGetProcessDpiAwareness  = syscall.NewLazyDLL("shcore.dll").NewProc("GetProcessDpiAwareness")
)

const (
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = DPI_AWARENESS_CONTEXT(-4).
	dpiAwarenessContextPerMonitorV2 = ^uintptr(3) // -4 em complemento de 2
	// PROCESS_PER_MONITOR_DPI_AWARE (shcore).
	processPerMonitorDpiAware = 2
)

// EnsurePerMonitorDPIAwareness garante coordenadas físicas no processo.
// Ver documentação do bloco acima. Seguro chamar múltiplas vezes.
func EnsurePerMonitorDPIAwareness() bool {
	dpiOnce.Do(func() {
		// 1. Já é per-monitor? (manifest embutido ou set anterior)
		if shcorePerMonitorActive() {
			dpiPhysical = true
			dpiAwareSource = "já ativa (manifest/set anterior)"
			return
		}
		// 2. PMv2 (Win10 1703+) — cobre mixed-DPI multi-monitor.
		if procSetProcessDpiAwarenessContext.Find() == nil {
			ret, _, _ := procSetProcessDpiAwarenessContext.Call(dpiAwarenessContextPerMonitorV2)
			if ret != 0 {
				dpiPhysical = true
				dpiAwareSource = "SetProcessDpiAwarenessContext(PMv2)"
				return
			}
		}
		// 3. Per-monitor v1 (Win8.1+).
		if procShcoreSetProcessDpiAwareness.Find() == nil {
			ret, _, _ := procShcoreSetProcessDpiAwareness.Call(processPerMonitorDpiAware)
			if ret == 0 {
				dpiPhysical = true
				dpiAwareSource = "SetProcessDpiAwareness(PER_MONITOR)"
				return
			}
		}
		// 4. Último recurso: system aware (coordenadas físicas do monitor
		// primário; insuficiente para mixed-DPI, mas melhor que unaware).
		if procSetProcessDPIAware.Find() == nil {
			if ret, _, _ := procSetProcessDPIAware.Call(); ret != 0 {
				dpiPhysical = true
				dpiAwareSource = "SetProcessDPIAware (system)"
				return
			}
		}
		log.Printf("[screen] AVISO: não foi possível garantir DPI awareness — coordenadas podem ser virtualizadas (DPI scaling quebra input/captura)")
	})
	return dpiPhysical
}

// DpiAwarenessSource retorna como a awareness foi garantida (diagnóstico).
func DpiAwarenessSource() string { return dpiAwareSource }

// shcorePerMonitorActive verifica se o processo já é PER_MONITOR_DPI_AWARE.
func shcorePerMonitorActive() bool {
	if procShcoreGetProcessDpiAwareness.Find() != nil {
		return false
	}
	var level uint32
	ret, _, _ := procShcoreGetProcessDpiAwareness.Call(0, uintptr(unsafe.Pointer(&level)))
	return ret == 0 && level >= processPerMonitorDpiAware
}
