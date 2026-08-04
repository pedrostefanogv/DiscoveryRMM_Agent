package app

import "discovery/app/services/locale"

func (a *App) GetPreferredLocale() string {
	return locale.DetectPreferredLocale()
}
