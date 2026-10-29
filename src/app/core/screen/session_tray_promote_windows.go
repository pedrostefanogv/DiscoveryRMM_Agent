//go:build windows

package screen

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

// ── Promoção do ícone da sessão para a área visível ──
//
// O Windows 11 coloca TODO ícone novo no overflow ("^") da bandeja; não existe
// API pública para pedir "mostre ao lado do relógio" (o antigo XML do Win10 foi
// removido). O próprio shell persiste essa escolha por ícone em
// HKCU\Control Panel\NotifyIconSettings\<id>, no DWORD "IsPromoted".
//
// Então fazemos o mesmo que o usuário faz ao arrastar o ícone para fora:
// localizamos as entradas cujo ExecutablePath aponta para este processo e
// marcamos IsPromoted=1. É best-effort: se a chave ainda não existe (primeira
// execução), tentamos por alguns segundos; se o shell não permitir, o ícone
// continua no overflow como antes (nunca piora).
const (
	notifyIconSettingsPath = `Control Panel\NotifyIconSettings`
	notifyIconPromoteTries = 30
	notifyIconPromoteDelay = 200 * time.Millisecond
)

// promoteSessionTrayIcon marca o ícone desta sessão como visível e, quando
// algum valor mudou, pede à thread da bandeja para recriar o ícone (o shell lê
// IsPromoted no NIM_ADD).
func (t *SessionTray) promoteSessionTrayIcon() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exe = filepath.Clean(exe)

	for attempt := 0; attempt < notifyIconPromoteTries; attempt++ {
		matched, changed, err := promoteNotifyIconEntries(exe)
		if changed > 0 {
			t.requestIconReadd()
			return
		}
		if matched > 0 {
			return // já promovido em uma execução anterior
		}
		if err != nil && attempt == 0 {
			log.Printf("[session-tray] promoção do ícone indisponível: %v", err)
		}
		select {
		case <-t.doneCh:
			return
		case <-time.After(notifyIconPromoteDelay):
		}
	}
}

// requestIconReadd posta na thread da bandeja o pedido de NIM_DELETE+NIM_ADD
// (o shell reavalia a visibilidade no add). Não bloqueia.
func (t *SessionTray) requestIconReadd() {
	t.mu.Lock()
	hwnd, closed := t.hwnd, t.closed
	t.mu.Unlock()
	if hwnd == 0 || closed {
		return
	}
	procPostMessageW.Call(hwnd, trayPromoteMessage, 0, 0)
}

// reapplyIcon recria o ícone para o shell reler IsPromoted.
func (t *SessionTray) reapplyIcon() {
	if err := t.removeIcon(); err != nil {
		log.Printf("[session-tray] falha ao recriar ícone (delete): %v", err)
	}
	if err := t.addIcon(); err != nil {
		log.Printf("[session-tray] falha ao recriar ícone (add): %v", err)
	}
}

// promoteNotifyIconEntries percorre NotifyIconSettings e devolve quantas
// entradas apontam para exePath (matched) e quantas foram promovidas agora
// (changed).
func promoteNotifyIconEntries(exePath string) (matched, changed int, err error) {
	root, err := registry.OpenKey(registry.CURRENT_USER, notifyIconSettingsPath,
		registry.READ|registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return 0, 0, err
	}
	defer root.Close()

	names, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return 0, 0, err
	}

	for _, name := range names {
		sub, openErr := registry.OpenKey(root, name, registry.QUERY_VALUE|registry.SET_VALUE)
		if openErr != nil {
			continue
		}
		rawPath, _, pathErr := sub.GetStringValue("ExecutablePath")
		if pathErr != nil || !notifyIconPathMatches(rawPath, exePath) {
			sub.Close()
			continue
		}
		matched++
		current, _, valueErr := sub.GetIntegerValue("IsPromoted")
		if valueErr != nil || current != 1 {
			if setErr := sub.SetDWordValue("IsPromoted", 1); setErr == nil {
				changed++
			}
		}
		sub.Close()
	}
	return matched, changed, nil
}

// notifyIconPathMatches compara o ExecutablePath do shell (literal ou com
// KNOWNFOLDERID, ex.: "{6D809377-...}\Discovery\discovery-service.exe") com
// o caminho real do processo.
func notifyIconPathMatches(registryPath, exePath string) bool {
	candidate := strings.TrimSpace(registryPath)
	candidate = strings.Trim(candidate, `"`)
	candidate = strings.ReplaceAll(candidate, "/", `\`)
	if candidate == "" {
		return false
	}
	// Remove o prefixo KNOWNFOLDERID ("{GUID}...") — a parte relativa basta
	// para casar com o caminho real (Program Files pode variar de idioma).
	if strings.HasPrefix(candidate, "{") {
		if end := strings.Index(candidate, `}\`); end >= 0 {
			candidate = candidate[end+2:]
		}
	}
	exe := strings.ReplaceAll(strings.TrimSpace(exePath), "/", `\`)
	if strings.EqualFold(candidate, exe) {
		return true
	}
	// Nome solto ("service.exe") não identifica o processo: só casamos quando
	// o caminho do registro tem diretório (literal ou relativo ao KNOWNFOLDER).
	if !strings.Contains(candidate, `\`) {
		return false
	}
	suffix := `\` + strings.TrimPrefix(candidate, `\`)
	return strings.HasSuffix(strings.ToLower(exe), strings.ToLower(suffix))
}
