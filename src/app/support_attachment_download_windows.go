//go:build windows

package app

import (
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"discovery/app/core/processutil"
)

// activeUserDownloadsDir resolve a pasta Downloads do usuário da sessão
// interativa quando o processo roda como SYSTEM (o serviço).
//
// os.UserHomeDir() em SYSTEM devolve ...\system32\config\systemprofile, então o
// anexo baixado "sumia" em
// C:\WINDOWS\system32\config\systemprofile\Downloads. Aqui pegamos o token do
// usuário do console (mesmo WTSQueryUserToken já usado na sessão remota) e
// lemos USERPROFILE do bloco de ambiente DELE (CreateEnvironmentBlock), que
// respeita o perfil real do usuário logado.
//
// Devolve "" quando não há sessão interativa — o caller mantém o fallback.
func activeUserDownloadsDir() string {
	sessionID := windows.WTSGetActiveConsoleSessionId()
	if sessionID == 0xFFFFFFFF {
		return ""
	}
	tok, err := wtsQueryUserToken(sessionID)
	if err != nil || tok == 0 {
		return ""
	}
	defer func() { _ = tok.Close() }()

	env, err := processutil.BuildUserEnvironment(syscall.Token(tok))
	if err != nil {
		return ""
	}
	profile := ""
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "USERPROFILE") {
			profile = strings.TrimSpace(value)
			break
		}
	}
	if profile == "" {
		return ""
	}
	return filepath.Join(profile, "Downloads")
}
