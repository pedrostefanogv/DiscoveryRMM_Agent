package app

import (
	"discovery/app/core/models"
	"discovery/app/updates"
)

func parseUpgradeOutput(raw string) []models.UpgradeItem {
	return updates.ParseUpgradeOutput(raw)
}

func parseInstalledOutput(raw string) []string {
	return updates.ParseInstalledOutput(raw)
}
