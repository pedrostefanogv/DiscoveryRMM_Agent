//go:build !windows

package app

import (
	"fmt"
	"net/http"
)

// debugHTTPServer is a minimal stub for non-Windows platforms
// so the App struct field compiles cross-platform.
type debugHTTPServer struct{}

// SetDebugFrontendAssets is a no-op on non-Windows platforms.
func SetDebugFrontendAssets(fs http.FileSystem) {}

// StartDebugHTTPServer is a no-op on non-Windows platforms.
func (a *App) StartDebugHTTPServer() error {
	return fmt.Errorf("debug-http não suportado nesta plataforma")
}

// StopDebugHTTPServer is a no-op on non-Windows platforms.
func (a *App) StopDebugHTTPServer() {}

// GetDebugHTTPPort returns 0 on non-Windows platforms.
func (a *App) GetDebugHTTPPort() int { return 0 }
