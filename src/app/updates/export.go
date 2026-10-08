package updates

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/samber/lo"

	"discovery/app/core/export"
	"discovery/app/core/models"
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
	paths := make([]string, 0, 8)

	// 1) Pastas VISÍVEIS PARA O USUÁRIO primeiro: Desktop e, em seguida,
	// Documentos. É onde a pessoa realmente procura o relatório — antes o
	// destino era LocalAppData\Discovery\Exports, invisível no dia a dia, e o
	// arquivo só aparecia depois de navegar por AppData. Mantemos o
	// subdiretório DiscoveryExports para não misturar o relatório com os
	// arquivos pessoais do usuário.
	for _, base := range userVisibleExportBaseDirs() {
		paths = append(paths, filepath.Join(base, "DiscoveryExports"))
	}

	// 2) Pasta histórica do agente (LocalAppData\Discovery\Exports): continua
	// como fallback — era o destino anterior e é sempre gravável pelo usuário
	// (a exportação pedida pela IA não deve exigir privilégio de administrador;
	// antes a pasta do executável vinha primeiro e, sem elevação, todas as
	// tentativas falhavam).
	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Discovery", "Exports"))
		}
	}

	// 3) Últimos recursos: pasta ao lado do executável (instalação elevada /
	// perfil não interativo), home e diretório corrente.
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "DiscoveryExports"))
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, "DiscoveryExports"))
	}
	paths = append(paths, filepath.Join(".", "DiscoveryExports"))
	return lo.Uniq(paths)
}

// userVisibleExportBaseDirs devolve as pastas pessoais onde o usuário espera
// achar o relatório exportado — Desktop antes de Documentos.
//
// Pastas que JÁ EXISTEM vêm primeiro: com o "Known Folder Move" do OneDrive,
// Desktop/Documentos podem estar redirecionados para %OneDrive% e a pasta
// antiga em %USERPROFILE% pode nem existir — criar uma pasta vazia ali daria um
// destino ruim (o arquivo "sumiria" da área de trabalho real do usuário).
func userVisibleExportBaseDirs() []string {
	var existing, missing []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			existing = append(existing, dir)
			return
		}
		missing = append(missing, dir)
	}

	home, _ := os.UserHomeDir()
	oneDrive := strings.TrimSpace(os.Getenv("OneDrive"))

	if strings.TrimSpace(home) != "" {
		add(filepath.Join(home, "Desktop"))
	}
	if oneDrive != "" {
		add(filepath.Join(oneDrive, "Desktop"))
	}
	if strings.TrimSpace(home) != "" {
		add(filepath.Join(home, "Documents"))
	}
	if oneDrive != "" {
		add(filepath.Join(oneDrive, "Documents"))
	}

	return append(existing, missing...)
}
