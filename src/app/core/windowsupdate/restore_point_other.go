//go:build !windows

package windowsupdate

import "errors"

// errRestorePointUnsupported indica plataforma sem System Restore.
var errRestorePointUnsupported = errors.New("restore point nao suportado nesta plataforma")

// createRestorePoint e um no-op fora do Windows.
func createRestorePoint(string) error {
	return errRestorePointUnsupported
}
