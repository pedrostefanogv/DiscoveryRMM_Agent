//go:build windows

package main

import "golang.org/x/sys/windows"

const (
	vkShift   = 0x10
	vkControl = 0x11
)

// detectStartupDebugMode checks instantaneously whether Shift or Ctrl is pressed.
// Must NOT sleep — this is called synchronously in the Wails startup callback.
func detectStartupDebugMode() bool {
	return isStartupKeyDown(vkShift) || isStartupKeyDown(vkControl)
}

func isStartupKeyDown(vk int) bool {
	user32 := windows.NewLazySystemDLL("user32.dll")
	procGetAsyncKeyState := user32.NewProc("GetAsyncKeyState")
	r, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return r&0x8000 != 0
}
