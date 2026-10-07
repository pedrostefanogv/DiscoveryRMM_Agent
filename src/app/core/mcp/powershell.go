package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"discovery/app/core/processutil"
)

// powershellDefaultTimeout limita scripts administrativos que nao informam
// timeout proprio. Scripts mais pesados passam um valor explicito.
const powershellDefaultTimeout = 60 * time.Second

// powershellEncodingPrologue forca a saida do PowerShell em UTF-8. Sem isso, o
// Windows PowerShell 5.1 escreve na codificacao do console (OEM/ANSI) e textos
// acentuados (mensagens de evento, nomes de impressora) chegavam corrompidos ao
// json.Unmarshal do agente.
const powershellEncodingPrologue = "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $OutputEncoding = [System.Text.Encoding]::UTF8; "

// buildPowerShellCommand monta (SEM executar) o comando powershell.exe com as
// flags padrao do agente. Fica separado de runPowerShell para permitir testar a
// montagem dos argumentos sem tocar no sistema.
func buildPowerShellCommand(ctx context.Context, script string) (*exec.Cmd, error) {
	if strings.TrimSpace(script) == "" {
		return nil, fmt.Errorf("script de PowerShell nao pode ser vazio")
	}
	if strings.ContainsRune(script, '\x00') {
		return nil, fmt.Errorf("script de PowerShell contem caractere invalido")
	}
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script)
	processutil.HideWindow(cmd)
	return cmd, nil
}

// errorResult devolve um payload JSON estruturado {"error": "..."} para que a
// IA receba um contrato estavel em vez de um panic ou de texto solto.
func errorResult(msg string) json.RawMessage {
	raw, err := json.Marshal(map[string]string{"error": strings.TrimSpace(msg)})
	if err != nil {
		return json.RawMessage(`{"error":"falha ao serializar erro"}`)
	}
	return json.RawMessage(raw)
}

// rawHasError informa se o payload devolvido pelo PowerShell e um erro
// estruturado (contrato {"error": "..."}).
func rawHasError(raw json.RawMessage) (string, bool) {
	var probe struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil && strings.TrimSpace(probe.Error) != "" {
		return probe.Error, true
	}
	return "", false
}

// runPowerShell executa o script com timeout e devolve stdout como JSON. Em
// qualquer falha devolve {"error": "..."} — nunca propaga panic.
func runPowerShell(ctx context.Context, script string, timeout time.Duration) json.RawMessage {
	if runtime.GOOS != "windows" {
		return errorResult("PowerShell disponivel apenas no Windows")
	}
	if timeout <= 0 {
		timeout = powershellDefaultTimeout
	}
	// Valida o script ANTES de acrescentar o prologo de encoding: sem isso um
	// script vazio viraria prologo+"" e executaria PowerShell a toa.
	if strings.TrimSpace(script) == "" {
		return errorResult("script de PowerShell nao pode ser vazio")
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd, err := buildPowerShellCommand(runCtx, powershellEncodingPrologue+script)
	if err != nil {
		return errorResult(err.Error())
	}

	// stdout e stderr SEPARADOS: com CombinedOutput o stderr (avisos do
	// PowerShell, CLIXML de erro) entrava no meio do JSON e invalidava o
	// parse. Só o stdout vira payload; o stderr é usado em mensagens de erro.
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	out := strings.TrimSpace(stdout.String())
	errOut := strings.TrimSpace(stderr.String())

	if runErr != nil {
		msg := fmt.Sprintf("erro ao executar PowerShell: %v", runErr)
		if errOut != "" {
			msg += " | stderr: " + truncate(errOut, 400)
		} else if out != "" {
			msg += " | stdout: " + truncate(out, 400)
		}
		return errorResult(msg)
	}
	if out == "" || out == "null" {
		return json.RawMessage(`{}`)
	}
	if !json.Valid([]byte(out)) {
		return errorResult("saida inesperada do PowerShell (nao-JSON): " + truncate(out, 200))
	}
	return json.RawMessage(out)
}

// requireAction valida e normaliza o parametro action das tools de familia.
func requireAction(args map[string]any, allowed ...string) (string, error) {
	action, _ := args["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		return "", fmt.Errorf("action nao pode ser vazia — valores aceitos: %s", strings.Join(allowed, ", "))
	}
	for _, a := range allowed {
		if action == a {
			return action, nil
		}
	}
	return "", fmt.Errorf("action invalida: %q — valores aceitos: %s", action, strings.Join(allowed, ", "))
}

// optionalIntArg le um inteiro opcional do payload, aceitando int/int64/float64
// (o unmarshal de JSON produz float64).
func optionalIntArg(args map[string]any, name string) int {
	switch v := args[name].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

// optionalBoolArg le um booleano opcional do payload.
func optionalBoolArg(args map[string]any, name string) bool {
	switch v := args[name].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}
