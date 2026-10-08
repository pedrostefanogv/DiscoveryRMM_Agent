package export

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-pdf/fpdf"

	"discovery/app/core/models"
)

// Regressão do erro relatado pelo usuário: "falha ao gerar pdf: undefined font:
// discoveryutf8 B". Sem nenhuma TTF disponível (DISCOVERY_PDF_TTF vazio e sem
// fontes do Windows), o fallback antigo usava pdf.Error() — getter que NÃO limpa
// o erro — então f.err ficava preso e OutputFileAndClose devolvia o erro de fonte.
// O export em Markdown funcionava e o PDF falhava SEMPRE, em qualquer pasta.
func TestWritePDFWithoutAnyFontStillSucceeds(t *testing.T) {
	t.Setenv(pdfFontPathEnvVar, "")
	t.Setenv(pdfFontConfigEnvVar, "")
	t.Setenv("WINDIR", "") // sem fontes do sistema: exercita o fallback core puro

	out := filepath.Join(t.TempDir(), "inventario.pdf")
	report := models.InventoryReport{
		CollectedAt: "2026-10-07T20:48:03Z",
		Source:      "teste",
		Hardware:    models.HardwareInfo{Hostname: "PC-ACENTUAÇÃO", CPU: "CPU", Cores: 4, LogicalCores: 8, MemoryGB: 16},
		Software: []models.SoftwareItem{
			{Name: "Ação", Version: "1.0", Publisher: "Fabricante"},
		},
	}

	if err := WritePDF(report, out, false); err != nil {
		t.Fatalf("WritePDF falhou sem nenhuma TTF (deveria cair para a fonte core): %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("pdf não foi gravado: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		t.Fatalf("arquivo gerado não é um PDF válido (prefixo=%q)", data[:minInt(8, len(data))])
	}
	if len(data) < 500 {
		t.Fatalf("pdf suspeito de truncamento: %d bytes", len(data))
	}
}

// Caminho de fonte inválido não pode derrubar a exportação: a resolução de fonte
// precisa ignorar o arquivo inexistente e usar o fallback core.
func TestWritePDFIgnoresBogusFontPath(t *testing.T) {
	t.Setenv(pdfFontPathEnvVar, filepath.Join(t.TempDir(), "nao-existe.ttf"))
	t.Setenv(pdfFontConfigEnvVar, "")

	out := filepath.Join(t.TempDir(), "inventario.pdf")
	if err := WritePDF(models.InventoryReport{Source: "teste"}, out, false); err != nil {
		t.Fatalf("WritePDF falhou com caminho de fonte inexistente: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("pdf não foi gravado: %v", err)
	}
}

// setPDFFont deve LIMPAR o erro de fonte inexistente antes de aplicar o fallback;
// se voltar a usar Error() (getter), o erro preso reaparece em OutputFileAndClose.
func TestSetPDFFontClearsErrorAfterFallback(t *testing.T) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	setPDFFont(pdf, "FamiliaQueNaoExiste", "B", 12)
	if pdf.Err() {
		t.Fatalf("erro de fonte continuou preso após o fallback: %v", pdf.Error())
	}
}

// Com as fontes do Windows disponíveis, o relatório deve registrar a TTF UTF-8
// automaticamente — sem depender de configurar DISCOVERY_PDF_TTF no instalador.
func TestWritePDFUsesSystemUTF8FontWhenAvailable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fontes do Windows só existem no Windows")
	}
	t.Setenv(pdfFontPathEnvVar, "")
	t.Setenv(pdfFontConfigEnvVar, "")
	windir := os.Getenv("WINDIR")
	if existingFile(filepath.Join(windir, "Fonts", "arial.ttf")) == "" {
		t.Skip("arial.ttf não encontrada nesta máquina")
	}

	out := filepath.Join(t.TempDir(), "inventario.pdf")
	if err := WritePDF(models.InventoryReport{Source: "teste"}, out, false); err != nil {
		t.Fatalf("WritePDF falhou com fonte do sistema disponível: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("pdf não foi gravado: %v", err)
	}
}

// resolvePDFFamily deve escolher a TTF UTF-8 quando ela existe e cair para a
// família core quando não há nenhuma fonte — devolvendo sempre uma família sem
// deixar erro preso no fpdf.
func TestResolvePDFFamilyChoosesAvailableFont(t *testing.T) {
	t.Setenv(pdfFontPathEnvVar, "")
	t.Setenv(pdfFontConfigEnvVar, "")

	hasSystemFont := false
	if runtime.GOOS == "windows" && os.Getenv("WINDIR") != "" {
		hasSystemFont = existingFile(filepath.Join(os.Getenv("WINDIR"), "Fonts", "arial.ttf")) != ""
	}

	// 1) Sem NENHUMA fonte disponível → família core.
	t.Setenv("WINDIR", "")
	pdf := fpdf.New("P", "mm", "A4", "")
	if fam := resolvePDFFamily(pdf); fam != pdfFallbackFamily {
		t.Fatalf("sem fontes deveria usar %q, obteve %q", pdfFallbackFamily, fam)
	}
	if pdf.Err() {
		t.Fatalf("erro preso ao resolver fonte inexistente: %v", pdf.Error())
	}

	// 2) Com a fonte do sistema disponível → família UTF-8 registrada.
	if !hasSystemFont {
		return
	}
	t.Setenv("WINDIR", os.Getenv("SystemRoot"))
	pdf2 := fpdf.New("P", "mm", "A4", "")
	if fam := resolvePDFFamily(pdf2); fam != pdfFontFamily {
		t.Fatalf("com arial.ttf disponível deveria usar %q, obteve %q", pdfFontFamily, fam)
	}
	if pdf2.Err() {
		t.Fatalf("erro preso ao registrar a TTF do sistema: %v", pdf2.Error())
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
