//go:build windows

package screen

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

// Monitor representa um monitor conectado.
type Monitor struct {
	Index     int    `json:"index"`     // índice na ordem de enumeração (usado como monitorIndex do capturer)
	X         int    `json:"x"`         // posição no desktop virtual
	Y         int    `json:"y"`         // posição no desktop virtual
	Width     int    `json:"width"`     // largura
	Height    int    `json:"height"`    // altura
	Name      string `json:"name"`      // device name (ex: \\.\DISPLAY1)
	IsPrimary bool   `json:"isPrimary"` // se é o monitor primário
}

// Retangulo (reutiliza `rect` de capturer_dxgi.go) — usado por GetMonitorInfoW.
type monitorInfo struct {
	cbSize    uint32
	rcMonitor rect
	rcWork    rect
	dwFlags   uint32
}

const (
	MONITORINFOF_PRIMARY = 0x00000001
)

var (
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
)

// enumMonitorsCallback é criado UMA VEZ por processo (M27: syscall.NewCallback
// registra um callback PERMANENTE no runtime, limite ~2000/processo — era
// chamado a cada re-detecção de geometria do GDI (a cada 5s), esgotando os
// slots em ~2-3h de sessão contínua e crashando o processo).
var enumMonitorsCallback = syscall.NewCallback(func(hMonitor, _ uintptr, _, _ uintptr) uintptr {
	mi := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	ret, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))
	if ret == 0 {
		return 1 // continua enumeração
	}

	m := Monitor{
		Width:  int(mi.rcMonitor.Right - mi.rcMonitor.Left),
		Height: int(mi.rcMonitor.Bottom - mi.rcMonitor.Top),
		X:      int(mi.rcMonitor.Left),
		Y:      int(mi.rcMonitor.Top),
	}
	if mi.dwFlags&MONITORINFOF_PRIMARY != 0 {
		m.IsPrimary = true
	}
	monitorsAccumMu.Lock()
	monitorsAccum = append(monitorsAccum, m)
	monitorsAccumMu.Unlock()
	return 1 // TRUE — continua
})

// monitorsAccum é o acumulador do callback único (protegido por mutex: o
// callback pode ser chamado de goroutines distintas em momentos diferentes).
var monitorsAccumMu sync.Mutex
var monitorsAccum []Monitor

// GetMonitors retorna a lista de monitores conectados via EnumDisplayMonitors.
// Ordena com o primário primeiro (índice 0 = primário, compatível com o capturer).
func GetMonitors() ([]Monitor, error) {
	// Zera o acumulador do callback único antes da enumeração.
	monitorsAccumMu.Lock()
	monitorsAccum = monitorsAccum[:0]
	monitorsAccumMu.Unlock()

	ret, _, _ := procEnumDisplayMonitors.Call(0, 0, enumMonitorsCallback, 0)
	if ret == 0 {
		return nil, fmt.Errorf("EnumDisplayMonitors falhou")
	}
	monitorsAccumMu.Lock()
	if len(monitorsAccum) == 0 {
		monitorsAccumMu.Unlock()
		return nil, fmt.Errorf("nao foi possivel detectar monitores")
	}
	monitors := append([]Monitor(nil), monitorsAccum...)
	monitorsAccumMu.Unlock()

	// Move o primário para o índice 0 (capturer usa índice 0 = primário por padrão).
	sorted := make([]Monitor, 0, len(monitors))
	for _, m := range monitors {
		if m.IsPrimary {
			sorted = append([]Monitor{m}, sorted...)
		} else {
			sorted = append(sorted, m)
		}
	}
	// Reatribui índices na ordem final e preenche nomes de forma estável.
	for i := range sorted {
		sorted[i].Index = i
		if sorted[i].Name == "" {
			sorted[i].Name = fmt.Sprintf("Monitor %dx%d", sorted[i].Width, sorted[i].Height)
		}
	}

	return sorted, nil
}
