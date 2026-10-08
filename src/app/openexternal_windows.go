//go:build windows

package app

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// knownFolderIDs mapeia o nome lógico do apelido para o FOLDERID do Windows.
var knownFolderIDs = map[string]*windows.KNOWNFOLDERID{
	"profile":   windows.FOLDERID_Profile,
	"desktop":   windows.FOLDERID_Desktop,
	"documents": windows.FOLDERID_Documents,
	"downloads": windows.FOLDERID_Downloads,
	"pictures":  windows.FOLDERID_Pictures,
	"videos":    windows.FOLDERID_Videos,
	"music":     windows.FOLDERID_Music,
}

func init() {
	// SHGetKnownFolderPath respeita pastas redirecionadas (perfil móvel, Downloads
	// em outro disco) — o fallback por %USERPROFILE% erraria nesses casos.
	knownFolderPath = func(name string) (string, bool) {
		id, ok := knownFolderIDs[strings.ToLower(strings.TrimSpace(name))]
		if !ok || id == nil {
			return "", false
		}
		p, err := windows.KnownFolderPath(id, 0)
		if err != nil || strings.TrimSpace(p) == "" {
			return "", false
		}
		return p, true
	}
}

// launchExternalTarget abre o alvo pelo shell do Windows (Explorer). O caminho
// vai como ARGUMENTO ÚNICO (o Go faz o quoting), sem shell intermediário — não
// há injeção de comando e só abrimos o que já foi resolvido/validado.
func launchExternalTarget(path string) error {
	cmd := exec.Command("explorer.exe", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("falha ao abrir %s: %w", path, err)
	}
	// O Explorer sai logo depois de repassar o pedido; não bloqueamos o chat.
	go func() { _ = cmd.Wait() }()
	return nil
}

// lookupRegisteredAppPath procura o executável em App Paths do Registro
// (HKLM e HKCU), que é como o "Executar" do Windows resolve nomes de app.
func lookupRegisteredAppPath(name string) (string, bool) {
	raw := strings.TrimSpace(name)
	if raw == "" {
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(raw), ".exe") {
		raw += ".exe"
	}
	key := "SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\App Paths\\" + raw
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, key, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, _, err := k.GetStringValue("")
		_ = k.Close()
		if err == nil && strings.TrimSpace(value) != "" {
			return value, true
		}
	}
	return "", false
}
