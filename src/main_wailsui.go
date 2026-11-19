//go:build wailsui

package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	wailsnotifications "github.com/wailsapp/wails/v3/pkg/services/notifications"

	appkg "discovery/app"
)

func buildWailsServices(app *appkg.App) []application.Service {
	return []application.Service{
		application.NewService(app),
		application.NewService(app.RemoteDebugService()),
		application.NewService(app.RemoteSessionService()),
	}
}

func wireWailsApp(app *appkg.App, appInstance *application.App, notifService *wailsnotifications.NotificationService) {
	app.SetApplicationWails(appInstance)
	app.SetNativeNotificationService(notifService)
}

func wireWailsWindowFactory(app *appkg.App, factory func() application.Window) {
	app.SetWindowFactoryWails(factory)
}
