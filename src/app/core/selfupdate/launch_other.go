//go:build !windows

package selfupdate

import "errors"

func (u *Updater) launchInstaller(_ string) error {
	return errors.New("selfupdate launchInstaller nao suportado neste SO")
}
