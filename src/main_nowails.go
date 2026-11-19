//go:build !wailsui

package main

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
	wailsnotifications "github.com/wailsapp/wails/v3/pkg/services/notifications"

	appkg "discovery/app"
)

func buildWailsServices(app *appkg.App) []application.Service {
	return []application.Service{
		application.NewService(app),
	}
}

func wireWailsApp(_ *appkg.App, _ *application.App, _ *wailsnotifications.NotificationService) {
	log.Fatal("[startup] binário UI compilado sem -tags wailsui; use 'cd src && wails3 build'")
}

func wireWailsWindowFactory(_ *appkg.App, _ func() application.Window) {}
