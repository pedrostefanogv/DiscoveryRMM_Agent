//go:build !windows

package selfupdate

// extractFileVersion retorna "" em plataformas não-Windows
// (a extração de versão de PE é específica do Windows)
func extractFileVersion(_ string) string {
	return ""
}
