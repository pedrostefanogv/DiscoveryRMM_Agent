package screenshot

import (
	"strings"
	"testing"
)

func TestBuildOCRPowerShellScriptContainsWinRTFlow(t *testing.T) {
	script := BuildOCRPowerShellScript("C:\\Temp\\captura.png", "pt-BR")
	required := []string{
		"[Windows.Media.Ocr.OcrEngine,Windows.Foundation,ContentType=WindowsRuntime]",
		"AsTask",
		"IsGenericMethodDefinition",
		"IAsyncOperation",
		"TryCreateFromLanguage",
		"TryCreateFromUserProfileLanguages",
		"RecognizeAsync",
		"ConvertTo-Json",
		"C:\\Temp\\captura.png",
		"'pt-BR'",
	}
	for _, part := range required {
		if !strings.Contains(script, part) {
			t.Errorf("script de OCR nao contem %q", part)
		}
	}
	if !strings.Contains(script, "catch") {
		t.Fatal("script deveria degradar com try/catch")
	}
}

func TestBuildOCRPowerShellScriptEscapesPath(t *testing.T) {
	script := BuildOCRPowerShellScript("C:\\Temp\\o'brien.png", "")
	if !strings.Contains(script, "o''brien.png") {
		t.Fatalf("apostrofo do caminho nao foi escapado: %s", script)
	}
}

// O host do PowerShell escreve o stdout redirecionado na code page do console;
// sem forçar UTF-8, texto acentuado do OCR chega como bytes inválidos (U+FFFD).
func TestBuildOCRPowerShellScriptForcesUTF8Output(t *testing.T) {
	script := BuildOCRPowerShellScript("C:\\Temp\\captura.png", "pt-BR")
	posEncoding := strings.Index(script, "[Console]::OutputEncoding")
	posJSON := strings.Index(script, "ConvertTo-Json")
	if posEncoding < 0 || !strings.Contains(script, "UTF8") {
		t.Fatalf("script nao forca a saida UTF-8: %s", script)
	}
	if posJSON < 0 || posEncoding > posJSON {
		t.Fatal("o encoding da saida precisa ser definido antes de imprimir o JSON")
	}
	// Falha ao ajustar o encoding (host sem console) nao pode abortar o OCR.
	lineStart := strings.LastIndex(script[:posEncoding], "\n") + 1
	lineEnd := strings.Index(script[posEncoding:], "\n")
	if lineEnd < 0 {
		lineEnd = len(script) - posEncoding
	}
	line := script[lineStart : posEncoding+lineEnd]
	if !strings.Contains(line, "try") || !strings.Contains(line, "catch") {
		t.Fatalf("o ajuste de encoding precisa estar protegido por try/catch: %q", line)
	}
}

func TestSanitizeOCRLanguage(t *testing.T) {
	cases := map[string]string{
		"pt-BR":                     "pt-BR",
		"  en-US  ":                 "en-US",
		"pt-BR'; Remove-Item C:\\x": "pt-BRRemove-ItemCx",
		"":                          "",
		strings.Repeat("a", 40):     strings.Repeat("a", 20),
	}
	for in, want := range cases {
		if got := SanitizeOCRLanguage(in); got != want {
			t.Errorf("SanitizeOCRLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildOCRPowerShellScriptRejectsInjection(t *testing.T) {
	script := BuildOCRPowerShellScript("C:\\x.png", "pt-BR'; Remove-Item C:\\Windows -Recurse; '")
	if strings.Contains(script, "Remove-Item C:") || strings.Contains(script, "Recurse") || strings.Contains(script, "; '") {
		t.Fatalf("idioma injetado vazou para o script: %s", script)
	}
	if !strings.Contains(script, "'pt-BRRemove-ItemCWin'") {
		t.Fatalf("sanitizacao inesperada do idioma: %s", script)
	}
}
