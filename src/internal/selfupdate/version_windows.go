//go:build windows

package selfupdate

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// extractFileVersion lê a versão do arquivo PE (Windows executable).
// Retorna a versão no formato "major.minor.patch" ou "" se não conseguir extrair.
func extractFileVersion(filePath string) string {
	var zeroHandle windows.Handle
	size, err := windows.GetFileVersionInfoSize(filePath, &zeroHandle)
	if err != nil || size == 0 {
		return ""
	}

	buf := make([]byte, size)
	err = windows.GetFileVersionInfo(filePath, 0, size, unsafe.Pointer(&buf[0]))
	if err != nil {
		return ""
	}

	// VerQueryValue: busca \StringFileInfo\<lang>\<key>
	// Tentamos lang comum 040904b0 (US English, Unicode); fallback 000004b0 (neutro).
	keys := []string{"FileVersion", "ProductVersion"}

	for _, key := range keys {
		subBlock := "\\StringFileInfo\\040904b0\\" + key

		var valPtr unsafe.Pointer
		var valLen uint32
		err = windows.VerQueryValue(unsafe.Pointer(&buf[0]), subBlock, unsafe.Pointer(&valPtr), &valLen)
		if err != nil {
			// Tentar com codepage neutra (000004b0)
			subBlockNeutral := "\\StringFileInfo\\000004b0\\" + key
			err = windows.VerQueryValue(unsafe.Pointer(&buf[0]), subBlockNeutral, unsafe.Pointer(&valPtr), &valLen)
			if err != nil {
				continue
			}
		}

		if valPtr != nil && valLen > 0 {
			version := windows.UTF16PtrToString((*uint16)(valPtr))
			if parsed := parseVersionString(version); parsed != "" {
				return parsed
			}
		}
	}

	return ""
}

func parseVersionString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// Extrair apenas números e pontos (evitar sufixos como "beta", etc)
	var version strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '.' {
			version.WriteRune(r)
		} else {
			break
		}
	}

	result := strings.TrimSpace(version.String())
	result = strings.Trim(result, ".")

	if result == "" {
		return ""
	}

	// Garantir que tem 3 componentes (major.minor.patch)
	parts := strings.Split(result, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	if len(parts) > 3 {
		parts = parts[:3]
	}

	return strings.Join(parts, ".")
}
