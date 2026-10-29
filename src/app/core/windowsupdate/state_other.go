//go:build !windows

package windowsupdate

// InvalidateInstalledKBsCache e no-op fora do Windows: nao existe CBS/registro
// local para cachear (a sonda nem roda nessas plataformas).
func InvalidateInstalledKBsCache() {}
