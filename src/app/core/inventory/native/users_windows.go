//go:build windows

package native

import (
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/models"
)

var (
	modwtsapi32 = windows.NewLazySystemDLL("wtsapi32.dll")

	procWTSEnumerateSessionsW       = modwtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSQuerySessionInformationW = modwtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory               = modwtsapi32.NewProc("WTSFreeMemory")
)

const (
	wtsCurrentServerHandle = 0
	wtsInfoClassUserName   = 5
	// WTSWinStationName (o valor 0 é WTSInitialProgram e devolvia TTY vazio).
	wtsInfoClassWinStationName = 6
	wtsInfoClassClientAddress  = 14
	// WTSSessionInfoEx traz o logon time em WTSINFOEX_LEVEL1_W. A classe
	// WTSLogonTime (18) não é suportada neste Windows ("request is not
	// supported"), por isso usamos a Ex.
	wtsInfoClassSessionInfoEx = 25
)

// wtsInfoExLevel1W espelha WTSINFOEX_LEVEL1_W (apenas os campos usados).
type wtsInfoExLevel1W struct {
	SessionID      uint32
	SessionState   uint32
	SessionFlags   int32
	WinStationName [33]uint16
	UserName       [21]uint16
	DomainName     [18]uint16
	LogonTime      int64
	ConnectTime    int64
}

// wtsInfoExW espelha WTSINFOEXW.
type wtsInfoExW struct {
	Level uint32
	Data  wtsInfoExLevel1W
}

// PrimaryLoggedInUser descreve a sessão interativa principal da máquina.
type PrimaryLoggedInUser struct {
	// User é "DOMINIO\usuario" (vazio quando não há sessão interativa).
	User string
	// LogonAt é o início da sessão (zero quando desconhecido).
	LogonAt time.Time
}

// wtsSessionInfo mirrors the Win32 WTS_SESSION_INFO.
//
// Layout nativo: DWORD SessionId; LPWSTR pWinStationName; WTS_CONNECTSTATE_CLASS
// State. O campo do nome é um PONTEIRO (8 bytes em x64), não um buffer inline —
// usar [32]uint16 aqui desloca State e a enumeração nunca encontra sessão ativa
// (bug que zerava o usuário logado no heartbeat e no inventário).
type wtsSessionInfo struct {
	SessionID      uint32
	WinStationName *uint16
	State          uint32
}

// activeSession é uma sessão WTS ativa já resolvida.
type activeSession struct {
	SessionID uint32
	User      string
	TTY       string
}

// collectLoggedInUsersWTS enumera as sessões ativas via WTSEnumerateSessionsW.
//
// A sessão de console (interativa) vai para o primeiro lugar: consumidores que
// usam report.LoggedInUsers[0] (envelope de inventário e parse do inventoryRaw
// no servidor) passam a enxergar o MESMO usuário que o heartbeat reporta,
// evitando divergência entre valor ao vivo e último persistido.
func collectLoggedInUsersWTS() ([]models.LoggedInUser, error) {
	sessions := collectActiveSessions()
	consoleID := activeConsoleSessionID()

	items := make([]models.LoggedInUser, 0, len(sessions))
	appendSession := func(s activeSession) {
		items = append(items, models.LoggedInUser{
			User: s.User,
			Type: "active",
			TTY:  s.TTY,
			PID:  0,
			Time: 0,
		})
	}

	for _, s := range sessions {
		if s.SessionID == consoleID {
			appendSession(s)
		}
	}
	for _, s := range sessions {
		if s.SessionID != consoleID {
			appendSession(s)
		}
	}

	return items, nil
}

// CollectPrimaryLoggedInUser devolve apenas o usuário da sessão interativa.
func CollectPrimaryLoggedInUser() string {
	return CollectPrimaryLoggedInUserSession().User
}

// CollectPrimaryLoggedInUserSession devolve o usuário e o instante de logon da
// sessão de console ativa. Sem console ativo (ex.: somente RDP), usa a primeira
// sessão ativa. Devolve PrimaryLoggedInUser vazio quando não há sessão.
func CollectPrimaryLoggedInUserSession() PrimaryLoggedInUser {
	consoleID := activeConsoleSessionID()
	sessions := collectActiveSessions()

	chosen := activeSession{}
	found := false
	for _, s := range sessions {
		if !found {
			chosen = s // primeira sessão ativa (fallback)
			found = true
		}
		if s.SessionID == consoleID {
			chosen = s
			break
		}
	}

	if !found && consoleID != 0 {
		// Fallback independente da enumeração: consulta direta da sessão de
		// console. Cobre variações de layout/versão do WTS que fariam a
		// enumeração falhar.
		if user := strings.TrimSpace(querySessionString(consoleID, wtsInfoClassUserName)); user != "" {
			found = true
			chosen = activeSession{SessionID: consoleID, User: user}
		}
	}

	if !found {
		return PrimaryLoggedInUser{}
	}

	result := PrimaryLoggedInUser{User: strings.TrimSpace(chosen.User)}
	if logonAt, ok := querySessionLogonTime(chosen.SessionID); ok {
		result.LogonAt = logonAt
	}
	return result
}

// collectActiveSessions enumera as sessões WTS em estado ativo com usuário
// resolvido. Sem subprocessos — disponível inclusive no modo serviço (SYSTEM).
func collectActiveSessions() []activeSession {
	var sessions []activeSession

	var ppSessionInfo *wtsSessionInfo
	var count uint32
	r, _, _ := procWTSEnumerateSessionsW.Call(
		uintptr(wtsCurrentServerHandle),
		0,
		1,
		uintptr(unsafe.Pointer(&ppSessionInfo)),
		uintptr(unsafe.Pointer(&count)),
	)
	if r == 0 || ppSessionInfo == nil {
		return sessions
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(ppSessionInfo)))

	// WTSActive = 0
	const wtsActive = 0

	base := unsafe.Pointer(ppSessionInfo)
	for i := uint32(0); i < count; i++ {
		info := (*wtsSessionInfo)(unsafe.Pointer(uintptr(base) + uintptr(i)*unsafe.Sizeof(wtsSessionInfo{})))
		if info.State != wtsActive {
			continue
		}

		user := strings.TrimSpace(querySessionString(info.SessionID, wtsInfoClassUserName))
		if user == "" {
			continue
		}

		sessions = append(sessions, activeSession{
			SessionID: info.SessionID,
			User:      user,
			TTY:       querySessionString(info.SessionID, wtsInfoClassWinStationName),
		})
	}

	return sessions
}

// activeConsoleSessionID devolve a sessão de console ativa. 0xFFFFFFFF (sem
// console) vira 0, que não colide com sessões interativas reais.
func activeConsoleSessionID() uint32 {
	if id := windows.WTSGetActiveConsoleSessionId(); id != 0xFFFFFFFF {
		return id
	}
	return 0
}

// querySessionLogonTime devolve o início da sessão via WTSSessionInfoEx.
func querySessionLogonTime(sessionID uint32) (time.Time, bool) {
	var buffer unsafe.Pointer
	var bytesReturned uint32
	r, _, _ := procWTSQuerySessionInformationW.Call(
		uintptr(wtsCurrentServerHandle),
		uintptr(sessionID),
		uintptr(wtsInfoClassSessionInfoEx),
		uintptr(unsafe.Pointer(&buffer)),
		uintptr(unsafe.Pointer(&bytesReturned)),
	)
	if r == 0 || buffer == nil {
		return time.Time{}, false
	}
	defer procWTSFreeMemory.Call(uintptr(buffer))

	logonOffset := unsafe.Offsetof(wtsInfoExW{}.Data) + unsafe.Offsetof(wtsInfoExLevel1W{}.LogonTime)
	if bytesReturned < uint32(logonOffset)+8 {
		return time.Time{}, false
	}

	ticks := *(*int64)(unsafe.Add(buffer, logonOffset))
	if ticks <= 0 {
		return time.Time{}, false
	}

	return filetimeToTime(uint64(ticks)), true
}

// filetimeToTime converte FILETIME (intervalos de 100ns desde 1601-01-01 UTC)
// para time.Time UTC.
func filetimeToTime(ticks uint64) time.Time {
	const (
		ticksPerSecond   = uint64(10_000_000)
		unixEpochSeconds = int64(11_644_473_600) // 1601-01-01 → 1970-01-01
	)
	seconds := int64(ticks/ticksPerSecond) - unixEpochSeconds
	nanos := int64(ticks%ticksPerSecond) * 100
	return time.Unix(seconds, nanos).UTC()
}

func querySessionString(sessionID uint32, infoClass uint32) string {
	var ppBuffer *uint16
	var bytesReturned uint32
	r, _, _ := procWTSQuerySessionInformationW.Call(
		uintptr(wtsCurrentServerHandle),
		uintptr(sessionID),
		uintptr(infoClass),
		uintptr(unsafe.Pointer(&ppBuffer)),
		uintptr(unsafe.Pointer(&bytesReturned)),
	)
	if r == 0 || ppBuffer == nil {
		return ""
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(ppBuffer)))

	if bytesReturned == 0 {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice(ppBuffer, bytesReturned/2))
}
