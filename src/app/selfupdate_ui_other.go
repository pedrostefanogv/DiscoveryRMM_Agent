//go:build !windows

package app

import (
	"context"
	"errors"
)

// selfUpdateInstallWithPSADT é um stub para plataformas não-Windows.
// O callback OnSelfUpdateInstall é atribuído mas nunca chamado porque
// o selfupdate.Updater só roda download+install no Windows.
func (a *App) selfUpdateInstallWithPSADT(ctx context.Context, exePath, targetVersion string) error {
	return errors.New("selfupdate PSADT UI nao suportado neste SO")
}
