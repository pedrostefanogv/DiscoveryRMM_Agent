package export

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-pdf/fpdf"

	"discovery/app/core/models"
)

const maxPDFSoftwareItems = 200

const (
	pdfFontFamily       = "DiscoveryUTF8"
	pdfFontPathEnvVar   = "DISCOVERY_PDF_TTF"
	pdfFontConfigEnvVar = "DISCOVERY_PDF_FONT_DIR"
	// pdfFallbackFamily é a família "core" do PDF (Helvetica, aceita pelo
	// alias "Arial"). Usada quando nenhuma TTF UTF-8 está disponível: o
	// relatório perde acentuação, mas NUNCA falha.
	pdfFallbackFamily = "Arial"
)

// defaultPDFFontNames são as TTF procuradas no diretório de fontes
// configurado (DISCOVERY_PDF_FONT_DIR) quando o caminho exato não foi dado.
var defaultPDFFontNames = []string{
	"arial.ttf", "segoeui.ttf", "calibri.ttf",
	"DejaVuSans.ttf", "LiberationSans-Regular.ttf",
}

// knownBoldSiblings mapeia a TTF regular para a variante negrito equivalente.
var knownBoldSiblings = map[string]string{
	"arial.ttf":                  "arialbd.ttf",
	"segoeui.ttf":                "segoeuib.ttf",
	"calibri.ttf":                "calibrib.ttf",
	"DejaVuSans.ttf":             "DejaVuSans-Bold.ttf",
	"LiberationSans-Regular.ttf": "LiberationSans-Bold.ttf",
}

// WritePDF writes the inventory report as a PDF file to outPath.
// If redact is true, sensitive fields (serials, MACs, hostname) are masked.
func WritePDF(r models.InventoryReport, outPath string, redact bool) error {
	hw := r.Hardware
	if redact {
		hw = RedactHardware(hw)
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	family := resolvePDFFamily(pdf)
	pdf.SetMargins(12, 12, 12)
	pdf.AddPage()
	setPDFFont(pdf, family, "B", 14)
	pdf.CellFormat(0, 8, "Inventario Discovery", "", 1, "L", false, 0, "")
	setPDFFont(pdf, family, "", 10)
	pdf.CellFormat(0, 6, "Coletado em: "+safePDF(r.CollectedAt), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, "Fonte: "+safePDF(r.Source), "", 1, "L", false, 0, "")

	addSection(pdf, family, "Hardware", []string{
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

	addSection(pdf, family, "Sistema Operacional", []string{
		"Nome: " + safePDF(r.OS.Name),
		"Versao: " + safePDF(r.OS.Version),
		"Build: " + safePDF(r.OS.Build),
		"Arquitetura: " + safePDF(r.OS.Architecture),
	})

	if len(r.LoggedInUsers) > 0 {
		setPDFFont(pdf, family, "B", 11)
		pdf.CellFormat(0, 7, "Usuarios Logados", "", 1, "L", false, 0, "")
		setPDFFont(pdf, family, "", 9)
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

	addSection(pdf, family, "Resumo", []string{
		"Volumes: " + strconv.Itoa(len(r.Volumes)),
		"Interfaces de rede: " + strconv.Itoa(len(r.Networks)),
		"Modulos de memoria: " + strconv.Itoa(len(r.MemoryModules)),
		"Monitores: " + strconv.Itoa(len(r.Monitors)),
		"GPUs: " + strconv.Itoa(len(r.GPUs)),
		"Startup items: " + strconv.Itoa(len(r.StartupItems)),
		"Autoexec: " + strconv.Itoa(len(r.Autoexec)),
		"Softwares: " + strconv.Itoa(len(r.Software)),
	})

	setPDFFont(pdf, family, "B", 11)
	pdf.CellFormat(0, 7, fmt.Sprintf("Softwares (primeiros %d)", maxPDFSoftwareItems), "", 1, "L", false, 0, "")
	setPDFFont(pdf, family, "", 9)
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

func addSection(pdf *fpdf.Fpdf, family, title string, lines []string) {
	setPDFFont(pdf, family, "B", 11)
	pdf.CellFormat(0, 7, title, "", 1, "L", false, 0, "")
	setPDFFont(pdf, family, "", 9)
	for _, line := range lines {
		pdf.MultiCell(0, 5, line, "", "L", false)
	}
	pdf.Ln(1)
}

// setPDFFont aplica a fonte e, se ela não existir, cai para a família core.
//
// BUG (corrigido): o fallback usava pdf.Error(), que em go-pdf/fpdf é apenas um
// GETTER — não limpa o erro interno. Depois do primeiro SetFont("DiscoveryUTF8")
// sem a TTF registrada, f.err ficava preso; SetFont passava a ignorar TODAS as
// chamadas seguintes (early-return em f.err != nil) e OutputFileAndClose devolvia
// "undefined font: discoveryutf8 B". Era o erro relatado pelo usuário: a
// exportação em Markdown funcionava e a em PDF falhava sempre, em qualquer pasta.
func setPDFFont(pdf *fpdf.Fpdf, family, style string, size float64) {
	pdf.SetFont(family, style, size)
	if pdf.Err() {
		pdf.ClearError()
		pdf.SetFont(pdfFallbackFamily, style, size)
	}
}

// resolvePDFFamily registra uma TTF UTF-8 quando disponível e devolve a família
// que deve ser usada em todas as chamadas SetFont.
//
// Ordem de busca: DISCOVERY_PDF_TTF (arquivo explícito) → DISCOVERY_PDF_FONT_DIR
// (diretório de fontes) → fontes do Windows. Sem nenhuma disponível, devolve a
// família core (Arial/Helvetica) — o PDF sai sem acentos, mas sai.
func resolvePDFFamily(pdf *fpdf.Fpdf) string {
	regular := existingFile(strings.TrimSpace(os.Getenv(pdfFontPathEnvVar)))

	if dir := strings.TrimSpace(os.Getenv(pdfFontConfigEnvVar)); dir != "" {
		pdf.SetFontLocation(dir)
		if regular == "" {
			for _, name := range defaultPDFFontNames {
				if p := existingFile(filepath.Join(dir, name)); p != "" {
					regular = p
					break
				}
			}
		}
	}

	if regular == "" {
		for _, candidate := range defaultPDFFontCandidates() {
			if p := existingFile(candidate); p != "" {
				regular = p
				break
			}
		}
	}

	if regular == "" {
		return pdfFallbackFamily
	}

	bold := boldSibling(regular)
	if bold == "" {
		bold = regular
	}

	pdf.AddUTF8Font(pdfFontFamily, "", regular)
	pdf.AddUTF8Font(pdfFontFamily, "B", bold)
	if pdf.Err() {
		// TTF inválida/ilegível: prefere um PDF sem acentos a falhar.
		pdf.ClearError()
		return pdfFallbackFamily
	}
	return pdfFontFamily
}

// defaultPDFFontCandidates devolve TTF com cobertura Unicode incluídas no
// Windows, para o relatório manter acentuação sem configuração extra.
func defaultPDFFontCandidates() []string {
	windir := strings.TrimSpace(os.Getenv("WINDIR"))
	if windir == "" {
		return nil
	}
	fonts := filepath.Join(windir, "Fonts")
	out := make([]string, 0, len(defaultPDFFontNames))
	for _, name := range defaultPDFFontNames {
		out = append(out, filepath.Join(fonts, name))
	}
	return out
}

// boldSibling devolve a variante negrito para uma TTF regular conhecida.
func boldSibling(regular string) string {
	name := filepath.Base(regular)
	boldName, ok := knownBoldSiblings[strings.ToLower(name)]
	if !ok {
		return ""
	}
	return existingFile(filepath.Join(filepath.Dir(regular), boldName))
}

// existingFile devolve o caminho quando ele existe e é arquivo regular.
func existingFile(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return ""
	}
	return path
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
