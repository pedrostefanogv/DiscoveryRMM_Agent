package app

import (
	"discovery/app/updates"
	"discovery/internal/models"
)

func parseUpgradeOutput(raw string) []models.UpgradeItem {
	return updates.ParseUpgradeOutput(raw)
}

func parseInstalledOutput(raw string) []string {
	return updates.ParseInstalledOutput(raw)
}
