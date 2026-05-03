//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// EnsureWorldAccess grants Everyone full control (read/write/execute) on the
// given directory with inheritance, so new files and subdirectories also get
// the permission. On Windows this is essential when the directory lives under
// %WINDIR%\Temp — a path whose default ACL only allows SYSTEM and Administrators.
//
// If the directory does not exist it is created first (with MkdirAll).
//
// On non-Windows platforms this is a no-op.
func EnsureWorldAccess(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("criar diretorio: %w", err)
	}

	// Use icacls to grant Everyone:(OI)(CI)F (Object Inherit + Container Inherit + Full).
	// The /T flag is omitted on purpose: we only set the ACL on the directory root;
	// inheritance handles the children automatically.
	cmd := exec.Command("icacls.exe", path, "/grant", "Everyone:(OI)(CI)F", "/Q")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls falhou: %w — saida: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
