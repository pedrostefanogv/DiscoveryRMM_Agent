//go:build windows

package winui

import (
	"unsafe"
)

// ── Monitor ativo ──
//
// No Windows a API SPI_GETWORKAREA (usada por WorkArea) devolve sempre o
// monitor PRIMÁRIO. O prompt de consentimento precisa aparecer no monitor em
// que o usuário está trabalhando: em multi-monitor, um cartão topmost no
// primário pode passar despercebido e o pedido expira como negado.

var (
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procMonitorFromWindow   = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
)

const (
	// monitorDefaultToNearest é MONITOR_DEFAULTTONEAREST.
	monitorDefaultToNearest = 2
)

// monitorInfo espelha MONITORINFO.
type monitorInfo struct {
	Size    uint32
	Monitor nativeRect
	Work    nativeRect
	Flags   uint32
}

// ActiveWorkArea devolve a área útil do monitor da janela em primeiro plano
// (sem a barra de tarefas desse monitor). Sem janela em primeiro plano ou em
// caso de falha, cai no monitor primário.
func ActiveWorkArea() Rect {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return WorkArea()
	}
	monitor, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if monitor == 0 {
		return WorkArea()
	}
	info := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if ok, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return WorkArea()
	}
	return Rect{
		X: int(info.Work.Left),
		Y: int(info.Work.Top),
		W: int(info.Work.Right - info.Work.Left),
		H: int(info.Work.Bottom - info.Work.Top),
	}
}
