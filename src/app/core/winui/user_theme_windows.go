//go:build windows

package winui

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ── Tema do usuário interativo ──
//
// O agente (worker da sessão remota) normalmente roda como SYSTEM na sessão
// interativa do usuário. HKCU aponta para o perfil do SYSTEM, então ler
// HKCU\...\Themes\Personalize daria SEMPRE o tema claro default — foi por
// isso que o banner antigo ficou com cores fixas e sem contraste.
//
// Aqui o tema é lido de HKEY_USERS\<SID> do usuário dono do explorer.exe da
// NOSSA sessão (quem realmente está vendo o banner/prompt). Se isso falhar
// (kiosk, sessão sem shell, acesso negado), o HKCU do próprio processo serve de
// fallback e, por último, o tema claro default.

const (
	personalizeKey = "Software\\Microsoft\\Windows\\CurrentVersion\\Themes\\Personalize"
	dwmKey         = "Software\\Microsoft\\Windows\\DWM"
	appsUseLight   = "AppsUseLightTheme"
)

// DetectTheme devolve a paleta do Windows do usuário interativo.
func DetectTheme() Theme {
	if sid, ok := interactiveUserSID(); ok {
		return detectThemeInHive(sid)
	}
	// Fallback: o processo está rodando como o próprio usuário (UI companion).
	return detectThemeInHKCU()
}

// detectThemeInHive lê o tema do hive HKEY_USERS\<sid>.
func detectThemeInHive(sid string) Theme {
	useLight := true
	if value, ok := readUserDword(sid, personalizeKey, appsUseLight); ok {
		useLight = value != 0
	}
	accent := DefaultAccent
	if value, ok := readUserDword(sid, dwmKey, "AccentColor"); ok {
		if candidate := colorFromABGR(uint32(value)); usableAccent(candidate) {
			accent = candidate
		}
	} else if value, ok := readUserDword(sid, dwmKey, "ColorizationColor"); ok {
		if candidate := colorFromARGB(uint32(value)); usableAccent(candidate) {
			accent = candidate
		}
	}
	return ThemeForPrefs(useLight, accent)
}

// detectThemeInHKCU lê o tema do perfil do próprio processo.
func detectThemeInHKCU() Theme {
	useLight := true
	if key, err := registry.OpenKey(registry.CURRENT_USER, personalizeKey, registry.QUERY_VALUE); err == nil {
		if value, _, err := key.GetIntegerValue(appsUseLight); err == nil {
			useLight = value != 0
		}
		key.Close()
	}
	accent := DefaultAccent
	if key, err := registry.OpenKey(registry.CURRENT_USER, dwmKey, registry.QUERY_VALUE); err == nil {
		if value, _, err := key.GetIntegerValue("AccentColor"); err == nil {
			if candidate := colorFromABGR(uint32(value)); usableAccent(candidate) {
				accent = candidate
			}
		}
		key.Close()
	}
	return ThemeForPrefs(useLight, accent)
}

// readUserDword lê um valor DWORD de HKEY_USERS\<sid>\<subkey>.
func readUserDword(sid, subkey, value string) (int64, bool) {
	key, err := registry.OpenKey(registry.USERS, sid+"\\"+subkey, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer key.Close()
	number, _, err := key.GetIntegerValue(value)
	if err != nil {
		return 0, false
	}
	return int64(number), true
}

// interactiveUserSID devolve o SID do usuário dono do explorer.exe da sessão
// atual e, quando não encontra o shell, o primeiro usuário logado com perfil
// carregado (HKEY_USERS).
func interactiveUserSID() (string, bool) {
	if sid, ok := explorerUserSID(); ok {
		return sid, true
	}
	return firstLoggedOnUserSID()
}

// explorerUserSID acha o SID do usuário do explorer.exe da NOSSA sessão.
func explorerUserSID() (string, bool) {
	session, ok := currentSessionID()
	if !ok {
		return "", false
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err := windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), "explorer.exe") {
			continue
		}
		var processSession uint32
		if windows.ProcessIdToSessionId(entry.ProcessID, &processSession) != nil || processSession != session {
			continue
		}
		if sid, ok := processUserSID(entry.ProcessID); ok {
			return sid, true
		}
	}
	return "", false
}

// processUserSID abre o token de um processo e devolve o SID do usuário dele.
// Como SYSTEM, PROCESS_QUERY_LIMITED_INFORMATION + TOKEN_QUERY é suficiente
// para processos da sessão interativa.
func processUserSID(processID uint32) (string, bool) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(process)

	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", false
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return "", false
	}
	return user.User.Sid.String(), true
}

// currentSessionID devolve a sessão do processo atual.
func currentSessionID() (uint32, bool) {
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		return 0, false
	}
	return session, true
}

// firstLoggedOnUserSID varre HKEY_USERS atrás de um perfil de usuário real com
// o bloco Personalize carregado (SIDs de serviço como S-1-5-18 não têm).
func firstLoggedOnUserSID() (string, bool) {
	users, err := registry.OpenKey(registry.USERS, "", registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return "", false
	}
	defer users.Close()
	names, err := users.ReadSubKeyNames(-1)
	if err != nil {
		return "", false
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "S-1-5-21-") && !strings.HasPrefix(name, "S-1-12-") {
			continue
		}
		if _, ok := readUserDword(name, personalizeKey, appsUseLight); ok {
			return name, true
		}
	}
	return "", false
}
