//go:build windows

package thirdparty

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"discovery/app/core/processutil"
)

// Verificação de assinatura Authenticode do instalador (WU-21/D24).
//
// O hash garante que o binário é o que foi curado; a assinatura garante QUEM
// produziu. Sem ela, um catálogo comprometido (ou erro de curadoria) ainda
// executaria um binário não assinado com o hash "certo".
//
// Implementação: Get-AuthenticodeSignature (PowerShell), que é a MESMA resposta
// que o Windows Explorer mostra ao abrir o arquivo. A primeira tentativa foi
// WinVerifyTrust via wintrust.dll; em máquina real ela recusou binários
// assinados da Microsoft (catalog-signed) — a API de catálogo exige passos
// extras que não valem o risco aqui. O custo do PowerShell (~0.5 s) é aceitável:
// só roda para apps de catálogo, que são raros e já baixam um instalador.
//
// Falha é FATAL por padrão: o instalador não é executado.

var (
	errUnsignedInstaller  = errors.New("instalador sem assinatura Authenticode valida")
	errUntrustedInstaller = errors.New("assinatura do instalador nao e confiavel nesta maquina")
)

const authenticodeTimeout = 2 * time.Minute

// verifyAuthenticode valida a assinatura do arquivo.
func verifyAuthenticode(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("caminho vazio para verificacao de assinatura")
	}

	ctx, cancel := context.WithTimeout(context.Background(), authenticodeTimeout)
	defer cancel()

	// O caminho vai por variável de ambiente: evita injeção/quoting no script.
	script := `$sig = Get-AuthenticodeSignature -LiteralPath $env:DISCOVERY_AUTH_PATH
if ($null -eq $sig) { Write-Output 'INVALID:sem-resultado' }
elseif ($sig.Status -eq 'Valid') { Write-Output 'VALID' }
else { Write-Output ('INVALID:' + $sig.Status) }`

	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), "DISCOVERY_AUTH_PATH="+path)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return fmt.Errorf("verificar assinatura Authenticode: %w (%s)", err, text)
	}

	switch {
	case text == "VALID":
		return nil
	case strings.Contains(text, "INVALID:NotSigned"):
		return fmt.Errorf("%w: arquivo nao assinado (%s)", errUnsignedInstaller, path)
	case strings.Contains(text, "INVALID:HashMismatch"):
		return fmt.Errorf("%w: assinatura presente mas arquivo alterado (%s)", errUntrustedInstaller, path)
	case strings.Contains(text, "INVALID:UnknownError"):
		// Arquivo sem estrutura PE (ex.: .bin) ou sem assinatura verificável.
		return fmt.Errorf("%w: arquivo sem assinatura verificavel (%s)", errUnsignedInstaller, path)
	case strings.HasPrefix(text, "INVALID:"):
		status := strings.TrimPrefix(text, "INVALID:")
		return fmt.Errorf("%w: status %s (%s)", errUntrustedInstaller, status, path)
	default:
		return fmt.Errorf("verificar assinatura Authenticode: resposta inesperada %q", text)
	}
}
