package app

import (
	"strings"
	"time"

	"discovery/internal/models"
)

const (
	packageActionInstall   = "install"
	packageActionUninstall = "uninstall"
	packageActionUpgrade   = "upgrade"
)

// GetPendingUpdates runs `winget upgrade` and parses the output into structured items.
func (a *App) GetPendingUpdates() ([]models.UpgradeItem, error) {
	done := a.beginActivity("checagem de atualizacoes")
	defer done()
	raw, err := a.appsSvc.ListUpgradable(a.ctx)
	a.logs.append("[winget upgrade] " + time.Now().Format("15:04:05"))
	a.logs.append(raw)
	if err != nil {
		return nil, err
	}
	items := parseUpgradeOutput(raw)
	return items, nil
}

// parseUpgradeOutput parses the tabular output of `winget upgrade`.
func parseUpgradeOutput(raw string) []models.UpgradeItem {
	// winget emits progress spinners using bare \r (no \n) to overwrite the same
	// terminal line. This means the spinner content and the actual table header end
	// up in the same \n-delimited segment. Simulate terminal CR-overwrite: for each
	// \n-terminated line keep only the last \r-delimited non-empty segment.
	rawLines := strings.Split(raw, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		parts := strings.Split(l, "\r")
		last := ""
		for j := len(parts) - 1; j >= 0; j-- {
			if strings.TrimSpace(parts[j]) != "" {
				last = parts[j]
				break
			}
		}
		lines = append(lines, last)
	}

	var items []models.UpgradeItem
	headerIdx := -1

	// Find the header line (contains "Name" and "Id" and "Version")
	for i, line := range lines {
		lower := strings.ToLower(line)
		if (strings.Contains(lower, "name") || strings.Contains(lower, "nome")) &&
			(strings.Contains(lower, "id")) &&
			(strings.Contains(lower, "version") || strings.Contains(lower, "vers")) {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 || headerIdx+1 >= len(lines) {
		return items
	}

	// Find the separator line (dashes)
	dataStart := headerIdx + 1
	if dataStart < len(lines) && strings.Count(lines[dataStart], "-") > 10 {
		dataStart++
	}

	// Parse column positions from header
	header := lines[headerIdx]
	idCol := findColumnStart(header, "Id")
	if idCol < 0 {
		idCol = findColumnStart(header, "ID")
	}
	verCol := findColumnStart(header, "Version")
	if verCol < 0 {
		verCol = findColumnStart(header, "Vers")
	}
	availCol := findColumnStart(header, "Available")
	if availCol < 0 {
		availCol = findColumnStart(header, "Dispon")
	}
	srcCol := findColumnStart(header, "Source")
	if srcCol < 0 {
		srcCol = findColumnStart(header, "Fonte")
	}

	for _, line := range lines[dataStart:] {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Skip summary lines like "X upgrades available"
		lower := strings.ToLower(line)
		if strings.Contains(lower, "upgrade") || strings.Contains(lower, "atualiza") {
			continue
		}

		item := models.UpgradeItem{}
		if idCol > 0 {
			item.Name = strings.TrimSpace(safeSubstring(line, 0, idCol))
		}
		if idCol >= 0 && verCol > idCol {
			item.ID = strings.TrimSpace(safeSubstring(line, idCol, verCol))
		}
		if verCol >= 0 && availCol > verCol {
			item.CurrentVersion = strings.TrimSpace(safeSubstring(line, verCol, availCol))
		}
		if availCol >= 0 {
			if srcCol > availCol {
				item.AvailableVersion = strings.TrimSpace(safeSubstring(line, availCol, srcCol))
			} else {
				item.AvailableVersion = strings.TrimSpace(safeSubstring(line, availCol, len(line)))
			}
		}
		if srcCol >= 0 {
			item.Source = strings.TrimSpace(safeSubstring(line, srcCol, len(line)))
		}

		if item.ID != "" {
			items = append(items, item)
		}
	}
	return items
}

func findColumnStart(header, keyword string) int {
	idx := strings.Index(header, keyword)
	if idx < 0 {
		idx = strings.Index(strings.ToLower(header), strings.ToLower(keyword))
	}
	return idx
}

func safeSubstring(s string, start, end int) string {
	runes := []rune(s)
	if start >= len(runes) {
		return ""
	}
	if end > len(runes) {
		end = len(runes)
	}
	if start < 0 {
		start = 0
	}
	return string(runes[start:end])
}

// GetPackageActions returns a contextual action map keyed by package id.
// Values: install, uninstall, upgrade.
func (a *App) GetPackageActions() (map[string]string, error) {
	done := a.beginActivity("contexto de pacotes")
	defer done()

	actions := map[string]string{}

	installedRaw, err := a.appsSvc.ListInstalled(a.ctx)
	if err != nil {
		return actions, err
	}
	a.logs.append("[winget list] " + time.Now().Format("15:04:05"))
	a.logs.append(installedRaw)

	for _, id := range parseInstalledOutput(installedRaw) {
		actions[strings.ToLower(id)] = packageActionUninstall
	}

	updatesRaw, updatesErr := a.appsSvc.ListUpgradable(a.ctx)
	a.logs.append("[winget upgrade] " + time.Now().Format("15:04:05"))
	a.logs.append(updatesRaw)
	if updatesErr == nil {
		for _, u := range parseUpgradeOutput(updatesRaw) {
			if strings.TrimSpace(u.ID) == "" {
				continue
			}
			actions[strings.ToLower(u.ID)] = packageActionUpgrade
		}
	}

	return actions, nil
}

func parseInstalledOutput(raw string) []string {
	rawLines := strings.Split(raw, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		parts := strings.Split(l, "\r")
		last := ""
		for j := len(parts) - 1; j >= 0; j-- {
			if strings.TrimSpace(parts[j]) != "" {
				last = parts[j]
				break
			}
		}
		lines = append(lines, last)
	}

	headerIdx := -1
	for i, line := range lines {
		lower := strings.ToLower(line)
		if (strings.Contains(lower, "name") || strings.Contains(lower, "nome")) &&
			(strings.Contains(lower, "id")) &&
			(strings.Contains(lower, "version") || strings.Contains(lower, "vers")) {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 || headerIdx+1 >= len(lines) {
		return nil
	}

	dataStart := headerIdx + 1
	if dataStart < len(lines) && strings.Count(lines[dataStart], "-") > 10 {
		dataStart++
	}

	header := lines[headerIdx]
	idCol := findColumnStart(header, "Id")
	if idCol < 0 {
		idCol = findColumnStart(header, "ID")
	}
	verCol := findColumnStart(header, "Version")
	if verCol < 0 {
		verCol = findColumnStart(header, "Vers")
	}
	if idCol < 0 || verCol <= idCol {
		return nil
	}

	ids := make([]string, 0, len(lines)-dataStart)
	for _, line := range lines[dataStart:] {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		id := strings.TrimSpace(safeSubstring(line, idCol, verCol))
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}

	return ids
}
