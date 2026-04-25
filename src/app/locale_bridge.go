package app

func (a *App) GetPreferredLocale() string {
	return detectPreferredLocale()
}
