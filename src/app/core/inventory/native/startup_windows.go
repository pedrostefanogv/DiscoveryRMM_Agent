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

	// Variantes usadas ao ler hives de outros usuários (HKEY_USERS\<SID>).
	wow64RunKeyUser     = `Software\Wow6432Node\Microsoft\Windows\CurrentVersion\Run`
	wow64RunOnceKeyUser = `Software\Wow6432Node\Microsoft\Windows\CurrentVersion\RunOnce`

	startupApprovedUser = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\`
	startupApprovedLM   = `SOFTWARE\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\`
	servicesKeyPath     = `SYSTEM\CurrentControlSet\Services`
	profileListKeyPath  = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList`

	maxAutostartServices = 500
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
	srcHKCURun32     = "HKCU Run (32 bits)"
	srcHKCURunOnce32 = "HKCU RunOnce (32 bits)"
	srcStartupUser   = "Pasta Startup (Usuário)"
	srcStartupCommon = "Pasta Startup (Todos os Usuários)"
	srcService       = "Serviço"
)

// Hives suportados no comando de habilitar/desabilitar item de inicialização.
const (
	hiveHKCU      = "HKCU"
	hiveHKLM      = "HKLM"
	hiveHKUPrefix = "HKU:"
)

// collectStartupItemsNative lê itens de inicialização de todas as origens:
// Run/RunOnce (HKLM 64/32 bits e HKCU), pastas Startup (usuário e comum),
// serviços de inicialização automática e os hives dos demais usuários
// carregados em HKEY_USERS. O estado (enabled/disabled) vem das chaves
// StartupApproved — a mesma fonte usada pelo Gerenciador de Tarefas.
//
// Ler HKEY_USERS é essencial quando o inventário roda no serviço (SYSTEM):
// nesse contexto o HKCU do processo é o do próprio SYSTEM, então os itens do
// usuário interativo só aparecem via HKU\<SID>.
func collectStartupItemsNative(ctx context.Context) ([]models.StartupItem, error) {
	_ = ctx
	var items []models.StartupItem

	// ── Registry Run/RunOnce ──
	items = append(items, withHive(readRunKey(registry.LOCAL_MACHINE, runKeyLocalMachine, srcHKLMRun, []string{"Run"}), hiveHKLM, "")...)
	items = append(items, withHive(readRunKey(registry.LOCAL_MACHINE, runOnceKeyLocalMachine, srcHKLMRunOnce, []string{"RunOnce", "Run32"}), hiveHKLM, "")...)
	items = append(items, withHive(readRunKey(registry.LOCAL_MACHINE, wow64RunKeyLocalMachine, srcHKLMRun32, []string{"Run32", "Run"}), hiveHKLM, "")...)
	items = append(items, withHive(readRunKey(registry.LOCAL_MACHINE, wow64RunOnceKeyLocalMachine, srcHKLMRunOnce32, []string{"Run32", "RunOnce"}), hiveHKLM, "")...)
	items = append(items, withHive(readRunKey(registry.CURRENT_USER, runKeyCurrentUser, srcHKCURun, []string{"Run"}), hiveHKCU, "")...)
	items = append(items, withHive(readRunKey(registry.CURRENT_USER, runKeyCurrentUserOnce, srcHKCURunOnce, []string{"RunOnce"}), hiveHKCU, "")...)

	// ── Pastas Startup ──
	items = append(items, readStartupFolder(true)...)
	items = append(items, readStartupFolder(false)...)

	// ── Serviços com inicialização automática ──
	items = append(items, collectAutostartServicesNative()...)

	// ── Demais usuários (hives carregados em HKEY_USERS) ──
	items = append(items, collectOtherUserStartupItems()...)

	return items, nil
}

// readRunKey enumera os valores de uma chave Run/RunOnce e marca o estado
// de cada item conforme as chaves StartupApproved (approvedSubkeys são as
// subchaves candidatas de StartupApproved, consultadas em ordem).
func readRunKey(root registry.Key, path, source string, approvedSubkeys []string) []models.StartupItem {
	return readRunKeyAt(root, "", path, source, approvedSubkeys)
}

// readRunKeyAt é a versão com hivePath (ex.: o SID ao ler HKEY_USERS).
func readRunKeyAt(root registry.Key, hivePath, path, source string, approvedSubkeys []string) []models.StartupItem {
	var items []models.StartupItem

	key, err := registry.OpenKey(root, joinRegPath(hivePath, path), windows.KEY_READ)
	if err != nil {
		return items
	}
	defer key.Close()

	names, err := key.ReadValueNames(-1)
	if err != nil {
		return items
	}

	approved, approvedOK := openApprovedKeyAt(root, hivePath, approvedSubkeys)
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
	return openApprovedKeyAt(root, "", subkeys)
}

// openApprovedKeyAt é a versão com hivePath (ex.: SID em HKEY_USERS).
func openApprovedKeyAt(root registry.Key, hivePath string, subkeys []string) (registry.Key, bool) {
	prefix := approvedPrefixFor(root)
	for _, sub := range subkeys {
		key, err := registry.OpenKey(root, joinRegPath(joinRegPath(hivePath, prefix), sub), windows.KEY_READ)
		if err == nil {
			return key, true
		}
	}
	return 0, false
}

// approvedPrefixFor retorna o prefixo de StartupApproved conforme o hive.
func approvedPrefixFor(root registry.Key) string {
	if root == registry.LOCAL_MACHINE {
		return startupApprovedLM
	}
	return startupApprovedUser
}

// joinRegPath concatena um prefixo (ex.: SID em HKEY_USERS) a um caminho de
// registro, tratando barras duplicadas/ausentes.
func joinRegPath(base, sub string) string {
	base = strings.Trim(base, `\`)
	sub = strings.Trim(sub, `\`)
	switch {
	case base == "":
		return sub
	case sub == "":
		return base
	default:
		return base + `\` + sub
	}
}

// withHive carimba o hive e o usuário nos itens recém-lidos (o leitor de
// chaves não conhece a conta de origem).
func withHive(items []models.StartupItem, hive, username string) []models.StartupItem {
	for i := range items {
		items[i].Hive = hive
		items[i].Username = username
	}
	return items
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
	var dir string
	root := registry.CURRENT_USER
	source := srcStartupUser
	hive := hiveHKCU
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
		root = registry.LOCAL_MACHINE
		source = srcStartupCommon
		hive = hiveHKLM
	}
	return readStartupFolderAt(dir, source, hive, "", root, "")
}

// readStartupFolderAt lê uma pasta Startup arbitrária — inclusive a de outro
// usuário (hivePath = SID em HKEY_USERS, quando o serviço roda como SYSTEM).
func readStartupFolderAt(dir, source, hive, username string, root registry.Key, hivePath string) []models.StartupItem {
	var items []models.StartupItem
	if dir == "" {
		return items
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return items
	}
	approved, approvedOK := openApprovedKeyAt(root, hivePath, []string{"StartupFolder"})
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
			Name:     entry.Name(),
			Path:     filepath.Join(dir, entry.Name()),
			Type:     "folder",
			Source:   source,
			Status:   status,
			Username: username,
			Hive:     hive,
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
			Hive:     hiveHKLM,
		})
	}
	return items
}

// collectLoggedInUsersNative returns logged-in users via the WTS API.
func collectLoggedInUsersNative(ctx context.Context) ([]models.LoggedInUser, error) {
	_ = ctx
	return collectLoggedInUsersWTS()
}

// userProfile descreve o perfil de um usuário (pasta + nome curto).
type userProfile struct {
	path string
	name string
}

// collectOtherUserStartupItems lê Run/RunOnce e a pasta Startup dos demais
// usuários. Necessário porque, no serviço (SYSTEM), o HKCU do processo é o do
// SYSTEM — sem isso os itens do usuário interativo nunca seriam coletados.
func collectOtherUserStartupItems() []models.StartupItem {
	var items []models.StartupItem
	profiles := userProfiles()
	currentSID := currentUserSIDString()
	usersRoot := registry.Key(windows.HKEY_USERS)

	// ── Hives carregados em HKEY_USERS\<SID> ──
	if sids, err := usersRoot.ReadSubKeyNames(-1); err == nil {
		for _, sid := range sids {
			if !isInspectableUserSID(sid) || sid == currentSID {
				continue
			}
			username := resolveUsername(sid, profiles)
			hive := hiveHKUPrefix + sid
			items = append(items, withHive(readRunKeyAt(usersRoot, sid, runKeyCurrentUser, srcHKCURun, []string{"Run"}), hive, username)...)
			items = append(items, withHive(readRunKeyAt(usersRoot, sid, runKeyCurrentUserOnce, srcHKCURunOnce, []string{"RunOnce", "Run32"}), hive, username)...)
			items = append(items, withHive(readRunKeyAt(usersRoot, sid, wow64RunKeyUser, srcHKCURun32, []string{"Run32", "Run"}), hive, username)...)
			items = append(items, withHive(readRunKeyAt(usersRoot, sid, wow64RunOnceKeyUser, srcHKCURunOnce32, []string{"Run32", "RunOnce"}), hive, username)...)
		}
	}

	// ── Pastas Startup dos perfis (mesmo com o hive descarregado) ──
	for sid, profile := range profiles {
		if sid == currentSID || profile.path == "" {
			continue
		}
		dir := filepath.Join(profile.path, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
		items = append(items, readStartupFolderAt(dir, srcStartupUser, hiveHKUPrefix+sid, profile.name, usersRoot, sid)...)
	}

	return items
}

// isInspectableUserSID filtra hives que não representam uma conta de usuário.
func isInspectableUserSID(sid string) bool {
	if sid == "" || sid == ".DEFAULT" {
		return false
	}
	if strings.HasSuffix(sid, "_Classes") {
		return false
	}
	return strings.HasPrefix(sid, "S-1-")
}

// userProfiles mapeia SID -> perfil (pasta + nome) via ProfileList.
func userProfiles() map[string]userProfile {
	result := map[string]userProfile{}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, profileListKeyPath, windows.KEY_READ)
	if err != nil {
		return result
	}
	defer key.Close()
	sids, err := key.ReadSubKeyNames(-1)
	if err != nil {
		return result
	}
	for _, sid := range sids {
		sub, err := registry.OpenKey(key, sid, windows.KEY_READ)
		if err != nil {
			continue
		}
		path, _, pathErr := sub.GetStringValue("ProfileImagePath")
		sub.Close()
		path = strings.TrimSpace(path)
		if pathErr != nil || path == "" {
			continue
		}
		result[sid] = userProfile{path: path, name: filepath.Base(path)}
	}
	return result
}

// resolveUsername resolve o nome exibido do SID (perfil, contas de serviço ou
// o próprio SID como último recurso).
func resolveUsername(sid string, profiles map[string]userProfile) string {
	// Contas de sistema primeiro: o ProfileImagePath delas aponta para
	// "…\config\systemprofile", que não é um nome amigável.
	switch sid {
	case "S-1-5-18":
		return "SYSTEM"
	case "S-1-5-19":
		return "SERVIÇO LOCAL"
	case "S-1-5-20":
		return "SERVIÇO DE REDE"
	}
	if p, ok := profiles[sid]; ok && p.name != "" {
		return p.name
	}
	return sid
}

// currentUserSIDString retorna o SID do dono do processo — usado para não
// duplicar os itens já lidos via HKCU.
func currentUserSIDString() string {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return ""
	}
	return user.User.Sid.String()
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
