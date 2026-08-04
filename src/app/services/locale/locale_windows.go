//go:build windows

package locale

import (
	"strings"
	"syscall"
	"unsafe"
)

const windowsLocaleNameMaxLength = 85

var (
	kernel32DLL                  = syscall.NewLazyDLL("kernel32.dll")
	procGetUserDefaultLocaleName = kernel32DLL.NewProc("GetUserDefaultLocaleName")
)

// DetectPreferredLocale detecta o locale preferido do usuário.
func DetectPreferredLocale() string {
	if locale := detectLocaleFromWindowsAPI(); locale != "" {
		return locale
	}
	if locale := DetectLocaleFromEnv(); locale != "" {
		return locale
	}
	return DefaultAppLocale
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
	return NormalizeSupportedLocale(utf16ToString(buffer))
}

func utf16ToString(buf []uint16) string {
	var sb strings.Builder
	for _, r := range buf {
		if r == 0 {
			break
		}
		sb.WriteRune(rune(r))
	}
	return sb.String()
}
