//go:build windows

package app

import (
	"syscall"
	"unsafe"
)

const windowsLocaleNameMaxLength = 85

var (
	kernel32DLL                  = syscall.NewLazyDLL("kernel32.dll")
	procGetUserDefaultLocaleName = kernel32DLL.NewProc("GetUserDefaultLocaleName")
)

func detectPreferredLocale() string {
	if locale := detectLocaleFromWindowsAPI(); locale != "" {
		return locale
	}
	if locale := detectLocaleFromEnv(); locale != "" {
		return locale
	}
	return defaultAppLocale
}

func detectLocaleFromWindowsAPI() string {
	buffer := make([]uint16, windowsLocaleNameMaxLength)
	ret, _, _ := procGetUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if ret == 0 {
		return ""
	}
	return normalizeSupportedLocale(syscall.UTF16ToString(buffer))
}
