//go:build windows

package screenshot

import (
	"flag"
	"fmt"
	"image/png"
	"os"
	"strings"
)

// ── Processo auxiliar de PrintWindow ──────────────────────────────────────
//
// PrintWindow pode PENDURAR indefinidamente em apps travados e não há como
// cancelar a chamada dentro do processo (limitação do Win32). Rodar a captura
// de janela em um processo FILHO permite ao agente matá-lo por timeout, sem
// deixar thread/goroutine presa nem recursos GDI pendurados.

// RunPrintWorker executa o modo auxiliar (flag --screenshot-print-worker):
// captura a janela via PrintWindow e grava um PNG no caminho informado.
// Retorna o exit code do processo.
func RunPrintWorker(args []string) int {
	fs := flag.NewFlagSet("screenshot-print-worker", flag.ContinueOnError)
	// O próprio marcador de modo chega nos args e precisa ser aceito pelo Parse.
	_ = fs.Bool("screenshot-print-worker", false, "modo auxiliar de PrintWindow")
	handle := fs.Uint64("hwnd", 0, "handle da janela alvo")
	out := fs.String("out", "", "caminho do PNG de saida")
	// O flag package só entende '-'; variantes com '/' são descartadas.
	cleaned := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.HasPrefix(arg, "/") {
			continue
		}
		cleaned = append(cleaned, arg)
	}
	if err := fs.Parse(cleaned); err != nil {
		fmt.Fprintln(os.Stderr, "[print-worker] "+err.Error())
		return 2
	}
	if *handle == 0 || *out == "" {
		fmt.Fprintln(os.Stderr, "[print-worker] --hwnd e --out sao obrigatorios")
		return 2
	}
	if err := writePrintWindowPNG(*handle, *out); err != nil {
		fmt.Fprintln(os.Stderr, "[print-worker] "+err.Error())
		return 3
	}
	return 0
}

func writePrintWindowPNG(handle uint64, outPath string) error {
	hwnd := uintptr(handle)
	if minimized, _, _ := procIsIconicS.Call(hwnd); minimized != 0 {
		return fmt.Errorf("janela minimizada")
	}
	_, _, w, h, ok := windowBounds(hwnd)
	if !ok || w <= 0 || h <= 0 {
		return fmt.Errorf("dimensoes da janela indisponiveis")
	}
	frame, err := windowFrameFromGDI(hwnd, w, h, true)
	if err != nil {
		return err
	}
	img := bgraToRGBA(frame)
	file, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		return err
	}
	return file.Sync()
}
