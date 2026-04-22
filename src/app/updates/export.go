package updates

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

// InventoryGetter resolves inventory for export.
type InventoryGetter func() (models.InventoryReport, error)

// ExportOptions wires the export service.
type ExportOptions struct {
	BeginActivity ActivityFunc
	Inventory     InventoryGetter
	GetRedact     func() bool
	SetRedact     func(bool)
	Now           func() time.Time
}

// Exporter handles inventory exports.
type Exporter struct {
	beginActivity ActivityFunc
	inventory     InventoryGetter
	getRedact     func() bool
	setRedact     func(bool)
	now           func() time.Time
}

// NewExporter builds an exporter.
func NewExporter(opts ExportOptions) *Exporter {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Exporter{
		beginActivity: opts.BeginActivity,
		inventory:     opts.Inventory,
		getRedact:     opts.GetRedact,
		setRedact:     opts.SetRedact,
		now:           now,
	}
}

// SetRedaction toggles data redaction in exports.
func (e *Exporter) SetRedaction(redact bool) {
	if e.setRedact != nil {
		e.setRedact(redact)
	}
}

// ExportInventoryMarkdown exports inventory data in Markdown format.
func (e *Exporter) ExportInventoryMarkdown() (string, error) {
	done := e.beginActivity("exportacao markdown")
	if done != nil {
		defer done()
	}
	report, err := e.inventory()
	if err != nil {
		return "", err
	}

	redact := false
	if e.getRedact != nil {
		redact = e.getRedact()
	}
	content := export.BuildMarkdown(report, redact)
	stamp := e.now().Format("20060102-150405")
	fileName := "inventory-" + stamp + ".md"

	path, err := writeWithFallback(fileName, func(outPath string) error {
		return os.WriteFile(outPath, []byte(content), 0o644)
	})
	if err != nil {
		return "", err
	}

	return path, nil
}

// ExportInventoryPDF exports inventory data in PDF format.
func (e *Exporter) ExportInventoryPDF() (string, error) {
	done := e.beginActivity("exportacao pdf")
	if done != nil {
		defer done()
	}
	report, err := e.inventory()
	if err != nil {
		return "", err
	}

	redact := false
	if e.getRedact != nil {
		redact = e.getRedact()
	}
	stamp := e.now().Format("20060102-150405")
	fileName := "inventory-" + stamp + ".pdf"

	path, err := writeWithFallback(fileName, func(outPath string) error {
		return export.WritePDF(report, outPath, redact)
	})
	if err != nil {
		return "", err
	}

	return path, nil
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
