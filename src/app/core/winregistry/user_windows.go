//go:build windows

package winregistry

import (
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// activeUserResolver e hiveExistsCheck sao injetaveis em testes para simular
// sessoes interativas sem depender de WTSQueryUserToken ou HKEY_USERS real.
var (
	activeUserResolver = resolveInteractiveUser
	hiveExistsCheck    = checkHKUSidOpenable
)

// ActiveUserContext devolve o contexto do usuario interativo conectado no
// momento (quando o agente roda como servico SYSTEM), ou nil se nao houver
// sessao ativa com hive montado em HKEY_USERS\<SID>.
func ActiveUserContext() *UserContext {
	return activeUserResolver()
}

func resolveInteractiveUser() *UserContext {
	sessionID := windows.WTSGetActiveConsoleSessionId()
	if sessionID != 0xFFFFFFFF {
		if user := resolveUserFromSession(sessionID); user != nil {
			return user
		}
	}

	var sessions *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &sessions, &count); err != nil {
		return nil
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(sessions)))

	for _, s := range unsafe.Slice(sessions, count) {
		if s.State == windows.WTSActive && s.SessionID != 0 {
			if user := resolveUserFromSession(s.SessionID); user != nil {
				return user
			}
		}
	}
	return nil
}

func resolveUserFromSession(sessionID uint32) *UserContext {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &tok); err != nil {
		return nil
	}
	defer tok.Close()

	tu, err := tok.GetTokenUser()
	if err != nil || tu == nil || tu.User.Sid == nil {
		return nil
	}
	sidStr := tu.User.Sid.String()
	switch sidStr {
	case "", "S-1-5-18", "S-1-5-19", "S-1-5-20":
		return nil
	}

	if !hiveExistsCheck(sidStr) {
		return nil
	}

	account, domain, _, err := tu.User.Sid.LookupAccount("")
	if err != nil || account == "" {
		account = sidStr
		domain = ""
	}

	return &UserContext{
		SID:       sidStr,
		Username:  account,
		Domain:    domain,
		SessionID: sessionID,
	}
}

func checkHKUSidOpenable(sid string) bool {
	key, err := registry.OpenKey(registry.USERS, sid, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return false
	}
	_ = key.Close()
	return true
}
