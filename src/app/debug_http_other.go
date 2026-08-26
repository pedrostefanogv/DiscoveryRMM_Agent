//go:build !windows

package app

import (
	"fmt"
	"net/http"
)

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

// IsDebugHTTPBoundToAllInterfaces returns false on non-Windows platforms.
func (a *App) IsDebugHTTPBoundToAllInterfaces() bool { return false }

// SetDebugHTTPBindAllInterfaces is a no-op on non-Windows platforms.
func (a *App) SetDebugHTTPBindAllInterfaces(enabled bool) error {
	return fmt.Errorf("debug-http não suportado nesta plataforma")
}

// PublishChatEvent is a no-op stub for non-Windows platforms.
func (a *App) PublishChatEvent(eventType, data string) {}

// EnsureChatSSEServer is a no-op stub for non-Windows platforms.
func (a *App) EnsureChatSSEServer() error { return nil }

// StopChatSSEServer is a no-op stub for non-Windows platforms.
func (a *App) StopChatSSEServer() {}

// GetChatSSEPort returns 0 on non-Windows platforms.
func (a *App) GetChatSSEPort() int { return 0 }
