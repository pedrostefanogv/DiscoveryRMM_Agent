//go:build windows

package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"discovery/app/core/models"
)

// collectPackageManagers scans known package manager directories for
// chocolatey, npm and python packages. This is a best-effort native
// replacement for the osquery chocolatey_packages/npm_packages/python_packages
// tables.
func collectPackageManagers(ctx context.Context) []models.SoftwareItem {
	var items []models.SoftwareItem

	// Chocolatey: C:\ProgramData\chocolatey\lib\<pkg>\<pkg>.nuspec
	items = append(items, scanChocolatey()...)

	// npm global: %APPDATA%\npm\node_modules and %ProgramFiles%\nodejs\node_modules
	items = append(items, scanNPM()...)

	// Python: site-packages directories.
	items = append(items, scanPython()...)

	return items
}

func scanChocolatey() []models.SoftwareItem {
	var items []models.SoftwareItem
	libDir := `C:\ProgramData\chocolatey\lib`
	entries, err := os.ReadDir(libDir)
	if err != nil {
		return items
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		version := ""
		// Look for <name>.nuspec to extract version.
		nuspec := filepath.Join(libDir, name, name+".nuspec")
		if data, err := os.ReadFile(nuspec); err == nil {
			version = extractNuspecVersion(string(data))
		}
		items = append(items, models.SoftwareItem{
			Name:      name,
			Version:   version,
			Source:    "chocolatey",
			InstallID: filepath.Join(libDir, name),
		})
	}
	return items
}

func extractNuspecVersion(content string) string {
	// Look for <version>X.Y.Z</version>
	idx := strings.Index(content, "<version>")
	if idx < 0 {
		return ""
	}
	rest := content[idx+len("<version>"):]
	end := strings.Index(rest, "</version>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func scanNPM() []models.SoftwareItem {
	var items []models.SoftwareItem
	dirs := []string{
		filepath.Join(os.Getenv("APPDATA"), "npm", "node_modules"),
		filepath.Join(os.Getenv("ProgramFiles"), "nodejs", "node_modules"),
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := strings.TrimPrefix(e.Name(), "@")
			version := readPackageJSONVersion(filepath.Join(dir, e.Name(), "package.json"))
			items = append(items, models.SoftwareItem{
				Name:      name,
				Version:   version,
				Source:    "npm",
				InstallID: filepath.Join(dir, e.Name()),
			})
		}
	}
	return items
}

func readPackageJSONVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Minimal parse: look for "version": "X.Y.Z"
	idx := strings.Index(string(data), `"version"`)
	if idx < 0 {
		return ""
	}
	rest := string(data)[idx+len(`"version"`):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return ""
	}
	rest = rest[colon+1:]
	rest = strings.TrimSpace(rest)
	rest = strings.Trim(rest, `"`)
	end := strings.IndexAny(rest, `",`)
	if end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

func scanPython() []models.SoftwareItem {
	var items []models.SoftwareItem
	// Common site-packages locations.
	dirs := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Python"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Python"),
	}
	for _, base := range dirs {
		versions, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, v := range versions {
			if !v.IsDir() {
				continue
			}
			sp := filepath.Join(base, v.Name(), "Lib", "site-packages")
			entries, err := os.ReadDir(sp)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				// Skip dist-info/egg-info metadata dirs.
				if strings.HasSuffix(name, ".dist-info") || strings.HasSuffix(name, ".egg-info") {
					continue
				}
				version := readDistInfoVersion(filepath.Join(sp, name))
				items = append(items, models.SoftwareItem{
					Name:      name,
					Version:   version,
					Source:    "python",
					InstallID: filepath.Join(sp, name),
				})
			}
		}
	}
	return items
}

func readDistInfoVersion(dir string) string {
	// Look for *.dist-info/METADATA or *.egg-info/PKG-INFO with "Version:".
	matches, _ := filepath.Glob(filepath.Join(dir, "*.dist-info", "METADATA"))
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(dir, "*.egg-info", "PKG-INFO"))
	}
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "Version:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
			}
		}
	}
	return ""
}
