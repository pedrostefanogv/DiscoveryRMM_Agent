package export

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/go-pdf/fpdf"

	"discovery/internal/models"
)

const maxPDFSoftwareItems = 200

const (
	pdfFontFamily       = "DiscoveryUTF8"
	pdfFontPathEnvVar   = "DISCOVERY_PDF_TTF"
	pdfFontConfigEnvVar = "DISCOVERY_PDF_FONT_DIR"
)

// WritePDF writes the inventory report as a PDF file to outPath.
// If redact is true, sensitive fields (serials, MACs, hostname) are masked.
func WritePDF(r models.InventoryReport, outPath string, redact bool) error {
	hw := r.Hardware
	if redact {
		hw = RedactHardware(hw)
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	tryEnableUTF8Font(pdf)
	pdf.SetMargins(12, 12, 12)
	pdf.AddPage()
	setPDFFont(pdf, "B", 14)
	pdf.CellFormat(0, 8, "Inventario Discovery", "", 1, "L", false, 0, "")
	setPDFFont(pdf, "", 10)
	pdf.CellFormat(0, 6, "Coletado em: "+safePDF(r.CollectedAt), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, "Fonte: "+safePDF(r.Source), "", 1, "L", false, 0, "")

	addSection(pdf, "Hardware", []string{
		"Hostname: " + safePDF(hw.Hostname),
		"Fabricante: " + safePDF(hw.Manufacturer),
		"Modelo: " + safePDF(hw.Model),
		"CPU: " + safePDF(hw.CPU),
		"Cores fisicos: " + strconv.Itoa(hw.Cores),
		"Cores logicos: " + strconv.Itoa(hw.LogicalCores),
		"Memoria (GB): " + strconv.FormatFloat(hw.MemoryGB, 'f', 2, 64),
		"Placa-mae fabricante: " + safePDF(hw.MotherboardManufacturer),
		"Placa-mae modelo: " + safePDF(hw.MotherboardModel),
		"Placa-mae serial: " + safePDF(hw.MotherboardSerial),
		"BIOS vendor: " + safePDF(hw.BIOSVendor),
		"BIOS versao: " + safePDF(hw.BIOSVersion),
		"BIOS data: " + safePDF(hw.BIOSReleaseDate),
		"BIOS serial: " + safePDF(hw.BIOSSerial),
		"Quantidade de pentes: " + strconv.Itoa(hw.MemoryModulesCount),
	})

	addSection(pdf, "Sistema Operacional", []string{
		"Nome: " + safePDF(r.OS.Name),
		"Versao: " + safePDF(r.OS.Version),
		"Build: " + safePDF(r.OS.Build),
		"Arquitetura: " + safePDF(r.OS.Architecture),
	})

	if len(r.LoggedInUsers) > 0 {
		setPDFFont(pdf, "B", 11)
		pdf.CellFormat(0, 7, "Usuarios Logados", "", 1, "L", false, 0, "")
		setPDFFont(pdf, "", 9)
		for _, u := range r.LoggedInUsers {
			sid := u.SID
			if redact {
				sid = Redacted
			}
			line := fmt.Sprintf("- %s | Tipo: %s | SID: %s",
				safePDF(u.User), safePDF(u.Type), safePDF(sid))
			pdf.MultiCell(0, 5, line, "", "L", false)
		}
		pdf.Ln(1)
	}

	addSection(pdf, "Resumo", []string{
		"Volumes: " + strconv.Itoa(len(r.Volumes)),
		"Interfaces de rede: " + strconv.Itoa(len(r.Networks)),
		"Modulos de memoria: " + strconv.Itoa(len(r.MemoryModules)),
		"Monitores: " + strconv.Itoa(len(r.Monitors)),
		"GPUs: " + strconv.Itoa(len(r.GPUs)),
		"Startup items: " + strconv.Itoa(len(r.StartupItems)),
		"Autoexec: " + strconv.Itoa(len(r.Autoexec)),
		"Softwares: " + strconv.Itoa(len(r.Software)),
	})

	setPDFFont(pdf, "B", 11)
	pdf.CellFormat(0, 7, fmt.Sprintf("Softwares (primeiros %d)", maxPDFSoftwareItems), "", 1, "L", false, 0, "")
	setPDFFont(pdf, "", 9)
	limit := len(r.Software)
	if limit > maxPDFSoftwareItems {
		limit = maxPDFSoftwareItems
	}
	software := append([]models.SoftwareItem(nil), r.Software...)
	sort.Slice(software, func(i, j int) bool {
		return strings.ToLower(software[i].Name) < strings.ToLower(software[j].Name)
	})
	for i := 0; i < limit; i++ {
		serial := software[i].Serial
		if redact {
			serial = Redacted
		}
		line := fmt.Sprintf("- %s | %s | %s | %s | %s",
			safePDF(software[i].Name),
			safePDF(software[i].Version),
			safePDF(software[i].Publisher),
			safePDF(software[i].InstallID),
			safePDF(serial),
		)
		pdf.MultiCell(0, 5, line, "", "L", false)
	}

	if err := pdf.OutputFileAndClose(outPath); err != nil {
		return fmt.Errorf("falha ao gerar pdf: %w", err)
	}
	return nil
}

func addSection(pdf *fpdf.Fpdf, title string, lines []string) {
	setPDFFont(pdf, "B", 11)
	pdf.CellFormat(0, 7, title, "", 1, "L", false, 0, "")
	setPDFFont(pdf, "", 9)
	for _, line := range lines {
		pdf.MultiCell(0, 5, line, "", "L", false)
	}
	pdf.Ln(1)
}

func setPDFFont(pdf *fpdf.Fpdf, style string, size float64) {
	pdf.SetFont(pdfFontFamily, style, size)
	if pdf.Err() {
		_ = pdf.Error()
		pdf.SetFont("Arial", style, size)
	}
}

func tryEnableUTF8Font(pdf *fpdf.Fpdf) {
	fontPath := strings.TrimSpace(os.Getenv(pdfFontPathEnvVar))
	if fontPath == "" {
		fontDir := strings.TrimSpace(os.Getenv(pdfFontConfigEnvVar))
		if fontDir != "" {
			pdf.SetFontLocation(fontDir)
		}
		return
	}
	pdf.AddUTF8Font(pdfFontFamily, "", fontPath)
	pdf.AddUTF8Font(pdfFontFamily, "B", fontPath)
}

// safePDF cleans a string for safe use in PDF cells.
func safePDF(s string) string {
	v := strings.TrimSpace(s)
	if v == "" {
		return "-"
	}
	v = strings.ReplaceAll(v, "\n", " ")
	v = strings.ReplaceAll(v, "\r", " ")
	return v
}
