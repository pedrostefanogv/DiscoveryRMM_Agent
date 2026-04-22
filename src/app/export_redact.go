package app

func (a *App) getRedact() bool {
	return a.exportCfg.get()
}
