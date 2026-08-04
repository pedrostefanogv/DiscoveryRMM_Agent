//go:build windows

package processutil

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	userenvDLL                  = windows.NewLazySystemDLL("userenv.dll")
	procCreateEnvironmentBlock  = userenvDLL.NewProc("CreateEnvironmentBlock")
	procDestroyEnvironmentBlock = userenvDLL.NewProc("DestroyEnvironmentBlock")
)

// BuildUserEnvironment calls CreateEnvironmentBlock (userenv.dll) to build the
// complete set of environment variables for the given user token.
//
// The returned slice has the form ["KEY=VALUE", ...] and is suitable for
// assignment to cmd.Env. This ensures child processes spawned via
// CreateProcessAsUser see correct user-specific variables such as
// %LOCALAPPDATA%, %APPDATA%, %USERPROFILE%, %TEMP%, etc., rather than
// inheriting the service's SYSTEM-scoped environment.
//
// On failure, an error is returned; callers should fall back to the inherited
// environment (service env) rather than propagating the error.
func BuildUserEnvironment(tok syscall.Token) ([]string, error) {
	// Use unsafe.Pointer (not uintptr) so that the block pointer returned by
	// the OS API can be passed back to parseEnvBlock without triggering
	// go vet's "possible misuse of unsafe.Pointer" rule (uintptr→unsafe.Pointer).
	var block unsafe.Pointer
	r, _, err := procCreateEnvironmentBlock.Call(
		uintptr(unsafe.Pointer(&block)),
		uintptr(tok),
		0, // bInherit = FALSE — do not merge the service's environment
	)
	if r == 0 {
		return nil, fmt.Errorf("CreateEnvironmentBlock: %w", err)
	}
	defer procDestroyEnvironmentBlock.Call(uintptr(block)) //nolint:errcheck

	return parseEnvBlock((*uint16)(block)), nil
}

// parseEnvBlock converts a Windows multi-string environment block (a contiguous
// sequence of null-terminated UTF-16 strings terminated by an extra null word)
// into a []string of "KEY=VALUE" entries.
func parseEnvBlock(block *uint16) []string {
	if block == nil {
		return nil
	}
	// Map block as a large read-only slice; we break out on the double-null
	// well before exhausting the artificial cap.
	all := (*[1 << 20]uint16)(unsafe.Pointer(block))[:]
	var entries []string
	for i := 0; i < len(all); {
		j := i
		for j < len(all) && all[j] != 0 {
			j++
		}
		if j == i {
			break // double-null terminator = end of block
		}
		entries = append(entries, string(utf16.Decode(all[i:j])))
		i = j + 1 // skip past null terminator
	}
	return entries
}
