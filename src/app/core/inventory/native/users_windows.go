//go:build windows

package native

import (
	"syscall"
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
	wtsCurrentServerHandle    = 0
	wtsInfoClassUserName      = 5
	wtsInfoClassName          = 0
	wtsInfoClassClientAddress = 14
)

// wtsSessionInfo mirrors WTS_SESSION_INFO.
type wtsSessionInfo struct {
	SessionID      uint32
	WinStationName [32]uint16
	State          uint32
}

// collectLoggedInUsersWTS enumerates active sessions via WTSEnumerateSessionsW.
func collectLoggedInUsersWTS() ([]models.LoggedInUser, error) {
	var items []models.LoggedInUser

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
		return items, nil
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

		user := querySessionString(info.SessionID, wtsInfoClassUserName)
		if user == "" {
			continue
		}

		items = append(items, models.LoggedInUser{
			User: user,
			Type: "active",
			TTY:  querySessionString(info.SessionID, wtsInfoClassName),
			PID:  0,
			Time: 0,
		})
	}

	return items, nil
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
