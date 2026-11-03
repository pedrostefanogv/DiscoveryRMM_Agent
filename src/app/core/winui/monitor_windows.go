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
	procMonitorFromPoint    = user32.NewProc("MonitorFromPoint")
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

// point espelha POINT do Win32: dois LONG (32 bits), diferente de nativeRect
// (quatro campos) — usar o tipo errado como out-param devolve lixo.
type point struct {
	X int32
	Y int32
}

// WorkAreaForPoint devolve a área útil (sem a barra de tarefas) do monitor que
// contém o ponto informado, em coordenadas FÍSICAS de tela.
//
// Existe porque WorkArea() só conhece o monitor primário: em multi-monitor um
// cartão ancorado no cursor/bandeja nasceria no monitor errado.
func WorkAreaForPoint(x, y int) Rect {
	if procMonitorFromPoint.Find() != nil {
		return WorkArea()
	}
	pt := point{X: int32(x), Y: int32(y)}
	monitor, _, _ := procMonitorFromPoint.Call(uintptr(unsafe.Pointer(&pt)), monitorDefaultToNearest)
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
