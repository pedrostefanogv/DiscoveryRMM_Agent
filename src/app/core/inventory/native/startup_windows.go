//go:build windows

package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"discovery/app/core/models"
)

const (
	runKeyCurrentUser           = `Software\Microsoft\Windows\CurrentVersion\Run`
	runKeyCurrentUserOnce       = `Software\Microsoft\Windows\CurrentVersion\RunOnce`
	runKeyLocalMachine          = `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`
	runOnceKeyLocalMachine      = `SOFTWARE\Microsoft\Windows\CurrentVersion\RunOnce`
	wow64RunKeyLocalMachine     = `SOFTWARE\Wow6432Node\Microsoft\Windows\CurrentVersion\Run`
	wow64RunOnceKeyLocalMachine = `SOFTWARE\Wow6432Node\Microsoft\Windows\CurrentVersion\RunOnce`

	startupApprovedLM           = `SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\`
	servicesKeyPath             = `SYSTEM\CurrentControlSet\Services`
	maxAutostartServices        = 500
)

// Fontes de inicialização suportadas. As mesmas strings são usadas no
// comando "startupitem" (enable/disable) para localizar a chave correta.
const (
	srcHKLMRun       = "HKLM Run"
	srcHKLMRunOnce   = "HKLM RunOnce"
	srcHKLMRun32     = "HKLM Run (32 bits)"
	srcHKLMRunOnce32 = "HKLM RunOnce (32 bits)"
	srcHKCURun       = "HKCU Run"
	srcHKCURunOnce   = "HKCU RunOnce"
	srcStartupUser   = "Pasta Startup (Usuário)"
	srcStartupCommon = "Pasta Startup (Todos os Usuários)"
	srcService       = "Serviço"
)

// collectStartupItemsNative lê itens de inicialização de todas as origens:
// Run/RunOnce (HKLM 64/32 bits e HKCU), pastas Startup (usuário e comum) e
// serviços de inicialização automática. O estado (enabled/disabled) vem das
// chaves StartupApproved — a mesma fonte usada pelo Gerenciador de Tarefas.
func collectStartupItemsNative(ctx context.Context) ([]models.StartupItem, error) {
	_ = ctx
	var items []models.StartupItem

	// ── Registry Run/RunOnce ──
	items = append(items, readRunKey(registry.LOCAL_MACHINE, runKeyLocalMachine, srcHKLMRun, []string{"Run"})...)
	items = append(items, readRunKey(registry.LOCAL_MACHINE, runOnceKeyLocalMachine, srcHKLMRunOnce, []string{"RunOnce", "Run32"})...)
	items = append(items, readRunKey(registry.LOCAL_MACHINE, wow64RunKeyLocalMachine, srcHKLMRun32, []string{"Run32", "Run"})...)
	items = append(items, readRunKey(registry.LOCAL_MACHINE, wow64RunOnceKeyLocalMachine, srcHKLMRunOnce32, []string{"Run32", "RunOnce"})...)
	items = append(items, readRunKey(registry.CURRENT_USER, runKeyCurrentUser, srcHKCURun, []string{"Run"})...)
	items = append(items, readRunKey(registry.CURRENT_USER, runKeyCurrentUserOnce, srcHKCURunOnce, []string{"RunOnce"})...)

	// ── Pastas Startup ──
	items = append(items, readStartupFolder(true)...)
	items = append(items, readStartupFolder(false)...)

	// ── Serviços com inicialização automática ──
	items = append(items, collectAutostartServicesNative()...)

	return items, nil
}

// readRunKey enumera os valores de uma chave Run/RunOnce e marca o estado
// de cada item conforme as chaves StartupApproved (approvedSubkeys são as
// subchaves candidatas de StartupApproved, consultadas em ordem).
func readRunKey(root registry.Key, path, source string, approvedSubkeys []string) []models.StartupItem {
	var items []models.StartupItem

	key, err := registry.OpenKey(root, path, windows.KEY_READ)
	if err != nil {
		return items
	}
	defer key.Close()

	names, err := key.ReadValueNames(-1)
	if err != nil {
		return items
	}

	approved, approvedOK := openApprovedKey(root, approvedSubkeys)
	if approvedOK {
		defer approved.Close()
	}

	for _, name := range names {
		val, _, err := key.GetStringValue(name)
		if err != nil {
			continue
		}
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		exePath, args := splitCommand(val)
		status := "enabled"
		if approvedOK && approvedValueDisabled(approved, name) {
			status = "disabled"
		}
		items = append(items, models.StartupItem{
			Name:   name,
			Path:   exePath,
			Args:   args,
			Type:   "registry",
			Source: source,
			Status: status,
		})
	}

	return items
}

// openApprovedKey abre a primeira subchave de StartupApproved existente
// (ex.: Run, RunOnce, Run32, StartupFolder). ok=false se nenhuma existir.
func openApprovedKey(root registry.Key, subkeys []string) (registry.Key, bool) {
	prefix := startupApprovedLM
	if root == registry.CURRENT_USER {
		prefix = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\`
	}
	for _, sub := range subkeys {
		key, err := registry.OpenKey(root, prefix+sub, windows.KEY_READ)
		if err == nil {
			return key, true
		}
	}
	return 0, false
}

// approvedValueDisabled verifica o valor binário de StartupApproved do item.
// Convenção do Windows (mesma do Gerenciador de Tarefas): bit 0 do primeiro
// byte ligado = item desabilitado; ausência de valor = habilitado.
func approvedValueDisabled(approved registry.Key, name string) bool {
	data, _, err := approved.GetBinaryValue(name)
	if err != nil || len(data) == 0 {
		return false
	}
	return data[0]&0x01 != 0
}

// readStartupFolder lista arquivos de uma pasta Startup (usuário ou comum).
func readStartupFolder(userScope bool) []models.StartupItem {
	var items []models.StartupItem
	var dir string
	if userScope {
		if v := os.Getenv("APPDATA"); v != "" {
			dir = filepath.Join(v, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
		}
	} else {
		common := os.Getenv("ProgramData")
		if common == "" {
			common = `C:\ProgramData`
		}
		dir = filepath.Join(common, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	}
	if dir == "" {
		return items
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return items
	}
	source := srcStartupUser
	approvedRoot := registry.CURRENT_USER
	if !userScope {
		source = srcStartupCommon
		approvedRoot = registry.LOCAL_MACHINE
	}
	approved, approvedOK := openApprovedKey(approvedRoot, []string{"StartupFolder"})
	if approvedOK {
		defer approved.Close()
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		status := "enabled"
		if approvedOK && approvedValueDisabled(approved, entry.Name()) {
			status = "disabled"
		}
		items = append(items, models.StartupItem{
			Name:   entry.Name(),
			Path:   filepath.Join(dir, entry.Name()),
			Type:   "folder",
			Source: source,
			Status: status,
		})
	}
	return items
}

// collectAutostartServicesNative enumera serviços (não drivers) com
// inicialização automática em HKLM\SYSTEM\CurrentControlSet\Services.
func collectAutostartServicesNative() []models.StartupItem {
	var items []models.StartupItem
	root, err := registry.OpenKey(registry.LOCAL_MACHINE, servicesKeyPath, windows.KEY_READ)
	if err != nil {
		return items
	}
	defer root.Close()
	names, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return items
	}
	for _, name := range names {
		if len(items) >= maxAutostartServices {
			break
		}
		key, err := registry.OpenKey(root, name, windows.KEY_READ)
		if err != nil {
			continue
		}
		start, _, _ := key.GetIntegerValue("Start")
		if start != 2 { // 2 = SERVICE_AUTO_START
			key.Close()
			continue
		}
		svcType, _, _ := key.GetIntegerValue("Type")
		// Apenas processos Win32 (0x10/0x20 e variantes interativas) — não drivers.
		if svcType&0x30 == 0 {
			key.Close()
			continue
		}
		imagePath, _, _ := key.GetStringValue("ImagePath")
		imagePath = strings.TrimSpace(imagePath)
		if imagePath == "" {
			key.Close()
			continue
		}
		delayed := 0
		if d, _, derr := key.GetIntegerValue("DelayedAutostart"); derr == nil && d == 1 {
			delayed = 1
		}
		key.Close()
		detail := "Automático"
		if delayed == 1 {
			detail = "Automático (Atrasado)"
		}
		exePath, args := splitCommand(imagePath)
		items = append(items, models.StartupItem{
			Name:     name,
			Path:     exePath,
			Args:     args,
			Type:     "service",
			Source:   srcService,
			Status:   "enabled",
			Detail:   detail,
			Username: "(Sistema)",
		})
	}
	return items
}

// collectLoggedInUsersNative returns logged-in users via the WTS API.
func collectLoggedInUsersNative(ctx context.Context) ([]models.LoggedInUser, error) {
	_ = ctx
	return collectLoggedInUsersWTS()
}

// splitCommand separa um comando citado em (path, args), respeitando aspas
// e aceitando caminhos com variáveis de ambiente não expandidas (ImagePath
// de serviços, ex.: `C:\Windows\system32\svchost.exe -k netsvcs`).
func splitCommand(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	raw = strings.TrimPrefix(raw, `\??\`)
	if strings.HasPrefix(raw, "\"") {
		if end := strings.Index(raw[1:], "\""); end >= 0 {
			return strings.TrimSpace(raw[1 : 1+end]), strings.TrimSpace(raw[2+end:])
		}
		return raw, ""
	}
	// Executáveis do system32 frequentemente aparecem sem caminho absoluto.
	if !strings.ContainsRune(raw, ':') {
		parts := strings.SplitN(raw, " ", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
		return raw, ""
	}
	// Sem aspas: o executável termina no primeiro ".exe" (comum em ImagePath).
	lower := strings.ToLower(raw)
	idx := strings.Index(lower, ".exe")
	if idx >= 0 {
		return raw[:idx+4], strings.TrimSpace(raw[idx+4:])
	}
	parts := strings.SplitN(raw, " ", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return raw, ""
}