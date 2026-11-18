//go:build windows

package remotesession

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type rdpProbeDeps struct {
	queryTermService func() (state string, startType string)
	readRDPRegistry  func() (fDeny *uint32, port int)
	probeTCP         func(ctx context.Context, port int) bool
	now              func() time.Time
}

func defaultRDPProbeDeps() rdpProbeDeps {
	return rdpProbeDeps{
		queryTermService: queryTermServiceReadOnly,
		readRDPRegistry:  readRDPRegistryReadOnly,
		probeTCP:         probeRDPLoopbackTCP,
		now:              func() time.Time { return time.Now().UTC() },
	}
}

// CheckRDPPreflight executa diagnostico estritamente somente-leitura do host RDP
// (servico TermService, chaves de registro RDP/GPO e probe TCP em 127.0.0.1:<port>).
func CheckRDPPreflight(ctx context.Context) RDPStatus {
	return checkRDPPreflightWithDeps(ctx, defaultRDPProbeDeps())
}

func checkRDPPreflightWithDeps(ctx context.Context, deps rdpProbeDeps) RDPStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	termState, termStart := "unknown", ""
	if deps.queryTermService != nil {
		termState, termStart = deps.queryTermService()
	}
	var fDeny *uint32
	port := 3389
	if deps.readRDPRegistry != nil {
		fDeny, port = deps.readRDPRegistry()
	}
	effPort := normalizeRDPPort(port)
	listening := false
	if deps.probeTCP != nil {
		listening = deps.probeTCP(ctx, effPort)
	}
	now := time.Now().UTC()
	if deps.now != nil {
		now = deps.now()
	}
	return evaluateRDPStatus(termState, termStart, fDeny, effPort, listening, now)
}

func queryTermServiceReadOnly() (string, string) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "unknown", "unknown"
	}
	defer windows.CloseServiceHandle(scm)

	namePtr, err := syscall.UTF16PtrFromString("TermService")
	if err != nil {
		return "unknown", "unknown"
	}
	svcHandle, err := windows.OpenService(scm, namePtr, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return "not_found", "unknown"
	}
	defer windows.CloseServiceHandle(svcHandle)

	state := "unknown"
	var procStatus windows.SERVICE_STATUS_PROCESS
	var needed uint32
	buf := make([]byte, unsafe.Sizeof(procStatus))
	if qerr := windows.QueryServiceStatusEx(
		svcHandle,
		windows.SC_STATUS_PROCESS_INFO,
		&buf[0],
		uint32(len(buf)),
		&needed,
	); qerr == nil {
		sp := (*windows.SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buf[0]))
		state = rdpSvcStateName(svc.State(sp.CurrentState))
	}

	startType := "unknown"
	n := uint32(1024)
	for {
		b := make([]byte, n)
		cfgPtr := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&b[0]))
		cerr := windows.QueryServiceConfig(svcHandle, cfgPtr, n, &n)
		if cerr == nil {
			startType = rdpSvcStartTypeName(cfgPtr.StartType)
			break
		}
		if cerr != syscall.ERROR_INSUFFICIENT_BUFFER || n <= uint32(len(b)) {
			break
		}
	}

	return state, startType
}

func rdpSvcStateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start_pending"
	case svc.StopPending:
		return "stop_pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue_pending"
	case svc.PausePending:
		return "pause_pending"
	case svc.Paused:
		return "paused"
	default:
		return "unknown"
	}
}

func rdpSvcStartTypeName(t uint32) string {
	switch t {
	case mgr.StartAutomatic:
		return "auto"
	case mgr.StartManual:
		return "demand"
	case mgr.StartDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

func readRDPRegistryReadOnly() (*uint32, int) {
	var fDeny *uint32
	port := 3389

	if k, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Terminal Server`,
		registry.QUERY_VALUE,
	); err == nil {
		if val, _, verr := k.GetIntegerValue("fDenyTSConnections"); verr == nil {
			v := uint32(val)
			fDeny = &v
		}
		_ = k.Close()
	}

	// GPO override tem precedencia no Windows quando presente.
	if gp, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Policies\Microsoft\Windows NT\Terminal Services`,
		registry.QUERY_VALUE,
	); err == nil {
		if val, _, verr := gp.GetIntegerValue("fDenyTSConnections"); verr == nil {
			v := uint32(val)
			fDeny = &v
		}
		_ = gp.Close()
	}

	if pk, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Terminal Server\WinStations\RDP-Tcp`,
		registry.QUERY_VALUE,
	); err == nil {
		if val, _, verr := pk.GetIntegerValue("PortNumber"); verr == nil && val >= 1 && val <= 65535 {
			port = int(val)
		}
		_ = pk.Close()
	}

	return fDeny, port
}

func probeRDPLoopbackTCP(ctx context.Context, port int) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	dialer := net.Dialer{Timeout: 1500 * time.Millisecond}
	conn, err := dialer.DialContext(probeCtx, "tcp", fmt.Sprintf("127.0.0.1:%d", normalizeRDPPort(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
