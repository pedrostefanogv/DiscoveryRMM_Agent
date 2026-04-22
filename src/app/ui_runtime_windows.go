//go:build windows

package app

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const gwOwner = 4

var (
	modUser32                    = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows              = modUser32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = modUser32.NewProc("GetWindowThreadProcessId")
	procIsHungAppWindow          = modUser32.NewProc("IsHungAppWindow")
	procIsWindowVisible          = modUser32.NewProc("IsWindowVisible")
	procGetWindow                = modUser32.NewProc("GetWindow")
	procGetWindowTextLengthW     = modUser32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW           = modUser32.NewProc("GetWindowTextW")
)

func probeUIRuntimeNative() uiRuntimeNativeProbe {
	probe := uiRuntimeNativeProbe{Supported: true}
	if procEnumWindows.Find() != nil ||
		procGetWindowThreadProcessId.Find() != nil ||
		procIsHungAppWindow.Find() != nil ||
		procIsWindowVisible.Find() != nil ||
		procGetWindow.Find() != nil ||
		procGetWindowTextLengthW.Find() != nil ||
		procGetWindowTextW.Find() != nil {
		return uiRuntimeNativeProbe{}
	}

	targetPID := uint32(os.Getpid())
	type candidate struct {
		hwnd    uintptr
		visible bool
		title   string
		score   int
	}
	best := candidate{}

	callback := windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var windowPID uint32
		procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&windowPID)))
		if windowPID != targetPID {
			return 1
		}

		owner, _, _ := procGetWindow.Call(hwnd, uintptr(gwOwner))
		if owner != 0 {
			return 1
		}

		visible, _, _ := procIsWindowVisible.Call(hwnd)
		title := getWindowText(hwnd)
		score := 0
		if visible != 0 {
			score += 10
		}
		if title != "" {
			score += 5
		}
		if strings.Contains(strings.ToLower(title), "discovery") {
			score += 20
		}
		if score <= best.score {
			return 1
		}

		best = candidate{
			hwnd:    hwnd,
			visible: visible != 0,
			title:   title,
			score:   score,
		}
		return 1
	})

	procEnumWindows.Call(callback, 0)
	if best.hwnd == 0 {
		return probe
	}

	hung, _, _ := procIsHungAppWindow.Call(best.hwnd)
	probe.WindowFound = true
	probe.Visible = best.visible
	probe.Title = best.title
	probe.Hung = hung != 0
	return probe
}

func getWindowText(hwnd uintptr) string {
	length, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if length == 0 {
		return ""
	}
	buf := make([]uint16, length+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return strings.TrimSpace(windows.UTF16ToString(buf))
}
