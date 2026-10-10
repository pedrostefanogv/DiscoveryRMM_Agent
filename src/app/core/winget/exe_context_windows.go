//go:build windows

package winget

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"discovery/app/core/ctxutil"
	"discovery/app/core/processutil"
)

// envValue devolve o valor de KEY=... num bloco de ambiente (puro/testável).
// Usa strings.Cut em vez de fatiar por len(prefix): ToUpper pode alterar o
// comprimento da string e um slice por índice fixo panicaria.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok && strings.EqualFold(strings.TrimSpace(name), key) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// resolveUserContextExecutable devolve o winget do PRÓPRIO usuário quando o ctx
// carrega o token da sessão interativa.
//
// Por que é necessário: o winget é um pacote MSIX POR PERFIL e o executável
// visível é um alias em %LOCALAPPDATA%\Microsoft\WindowsApps\winget.exe. O
// caminho resolvido/cacheado pelo processo do SERVIÇO pode ser o pacote em
// Program Files\WindowsApps (ACL restrita, sem identidade MSIX) ou o alias do
// SYSTEM — executá-lo com o token de OUTRO usuário falha (tipicamente
// 0x8a150001) ou aponta para o perfil errado.
//
// Contrato:
//   - sem token no ctx -> ("", nil): o caller usa a resolução normal;
//   - com token        -> o alias DO USUÁRIO, ou erro EXPLÍCITO quando ele não
//     existe. Nunca devolve "" COM token: cair silenciosamente no caminho
//     cacheado do SYSTEM era exatamente o bug que esta função corrige.
func resolveUserContextExecutable(ctx context.Context) (string, error) {
	tok, ok := ctxutil.ProcessUserToken(ctx)
	if !ok || tok == 0 {
		return "", nil
	}
	env, err := processutil.BuildUserEnvironment(tok)
	if err != nil || len(env) == 0 {
		return "", fmt.Errorf("winget indisponivel no perfil do usuario logado: falha ao montar o ambiente do usuario: %w", err)
	}
	localAppData := envValue(env, "LOCALAPPDATA")
	if localAppData == "" {
		return "", fmt.Errorf("winget indisponivel no perfil do usuario logado: LOCALAPPDATA ausente no ambiente do usuario")
	}
	candidate := filepath.Join(localAppData, "Microsoft", "WindowsApps", "winget.exe")
	if !usable(candidate) {
		return "", fmt.Errorf("winget indisponivel no perfil do usuario logado (%s): o pacote App Installer nao esta registrado para esta conta", candidate)
	}
	return candidate, nil
}
