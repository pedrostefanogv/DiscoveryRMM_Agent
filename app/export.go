package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/samber/lo"

	"discovery/internal/export"
	"discovery/internal/models"
)

func (a *App) SetExportRedaction(redact bool) {
	a.exportCfg.set(redact)
}

func (a *App) getRedact() bool {
	return a.exportCfg.get()
}

func (a *App) ExportInventoryMarkdown() (string, error) {
	done := a.beginActivity("exportacao markdown")
	defer done()
	report, err := a.getInventoryForExport()
	if err != nil {
		return "", err
	}

	content := export.BuildMarkdown(report, a.getRedact())
	stamp := time.Now().Format("20060102-150405")
	fileName := "inventory-" + stamp + ".md"

	path, err := writeWithFallback(fileName, func(outPath string) error {
		return os.WriteFile(outPath, []byte(content), 0o644)
	})
	if err != nil {
		return "", err
	}

	return path, nil
}

func (a *App) ExportInventoryPDF() (string, error) {
	done := a.beginActivity("exportacao pdf")
	defer done()
	report, err := a.getInventoryForExport()
	if err != nil {
		return "", err
	}

	stamp := time.Now().Format("20060102-150405")
	fileName := "inventory-" + stamp + ".pdf"

	path, err := writeWithFallback(fileName, func(outPath string) error {
		return export.WritePDF(report, outPath, a.getRedact())
	})
	if err != nil {
		return "", err
	}

	return path, nil
}

func (a *App) getInventoryForExport() (models.InventoryReport, error) {
	if cached, ok := a.invCache.get(); ok {
		return cached, nil
	}

	report, err := a.invSvc.GetInventory(a.ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}
	a.invCache.set(report)
	return report, nil
}

func writeWithFallback(fileName string, writer func(outPath string) error) (string, error) {
	candidates := exportDirCandidates()
	errs := make([]string, 0, len(candidates))

	for _, dir := range candidates {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		outPath := filepath.Join(dir, fileName)
		if err := writer(outPath); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		return outPath, nil
	}

	if len(errs) == 0 {
		return "", fmt.Errorf("nenhuma pasta de exportacao disponivel")
	}
	return "", fmt.Errorf("falha ao exportar; tentativas: %s", strings.Join(errs, " | "))
}

func exportDirCandidates() []string {
	paths := make([]string, 0, 5)

	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "DiscoveryExports"))
	}

	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Discovery", "Exports"))
		}
	}

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, "Documents", "DiscoveryExports"))
		paths = append(paths, filepath.Join(home, "DiscoveryExports"))
	}

	paths = append(paths, filepath.Join(".", "DiscoveryExports"))
	return lo.Uniq(paths)
}
