//go:build windows

package screenshot

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// ── Processo auxiliar de PrintWindow ──────────────────────────────────────
//
// PrintWindow pode PENDURAR indefinidamente em apps travados e não há como
// cancelar a chamada dentro do processo (limitação do Win32). Rodar a captura
// de janela em um processo FILHO permite ao agente matá-lo por timeout, sem
// deixar thread/goroutine presa nem recursos GDI pendurados.

// startPrintServerProcess sobe o processo auxiliar persistente
// (--screenshot-print-server) e devolve cmd + pipes de stdin/stdout. O processo
// sai sozinho no EOF do stdin (quando o agente encerra), então não fica órfão.
func startPrintServerProcess() (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
	if printHelperDisabled() {
		return nil, nil, nil, fmt.Errorf("processo auxiliar de PrintWindow desabilitado")
	}
	cmd := exec.Command(os.Args[0], "--screenshot-print-server")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	return cmd, stdin, stdout, nil
}

// RunPrintServer executa o modo auxiliar PERSISTENTE
// (--screenshot-print-server): lê uma requisição JSON por linha no stdin
// ({"hwnd":N,"out":"caminho.png"}) e responde {"ok":true} ou
// {"ok":false,"error":"..."} no stdout.
//
// Um único processo atende todas as capturas de janela da sessão: elimina o
// custo de spawn por captura (~100 ms) mantendo o isolamento — se uma janela
// travar o PrintWindow, o agente mata ESTE processo e o próximo pedido sobe um
// novo. O loop termina sozinho no EOF do stdin (agente encerrado), sem deixar
// processo órfão.
func RunPrintServer(in io.Reader, out io.Writer) int {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req struct {
			Hwnd uint64 `json:"hwnd"`
			Out  string `json:"out"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = encoder.Encode(map[string]any{"ok": false, "error": "json invalido: " + err.Error()})
			continue
		}
		if req.Hwnd == 0 || strings.TrimSpace(req.Out) == "" {
			_ = encoder.Encode(map[string]any{"ok": false, "error": "hwnd/out obrigatorios"})
			continue
		}
		if err := writePrintWindowPNG(req.Hwnd, req.Out); err != nil {
			_ = encoder.Encode(map[string]any{"ok": false, "error": err.Error()})
			continue
		}
		_ = encoder.Encode(map[string]any{"ok": true})
	}
	return 0
}

// RunPrintWorker executa o modo auxiliar avulso (flag --screenshot-print-worker):
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
