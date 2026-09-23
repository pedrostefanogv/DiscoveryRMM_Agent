//go:build windows

package native

import (
	"context"
	"net"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"

	"discovery/app/core/models"
	"discovery/app/core/nettable"
)

// collectNetworkConnectionsNative returns listening ports and open sockets
// using the shared nettable package (GetExtendedTcpTable/GetExtendedUdpTable,
// no subprocess).
func collectNetworkConnectionsNative(ctx context.Context) ([]models.ListeningPortInfo, []models.OpenSocketInfo, error) {
	// Enumera as tabelas em duas fases: respeita o cancelamento antes de cada
	// uma e ao final, para que um contexto cancelado não segure a coleta.
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	listening := collectListeningPortsNative()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	open := collectOpenSocketsNative()
	return listening, open, ctx.Err()
}

func collectListeningPortsNative() []models.ListeningPortInfo {
	var items []models.ListeningPortInfo
	seen := make(map[string]struct{})

	// TCP listening (IPv4 + IPv6).
	items = append(items, collectTCPListening(nettable.AfInet, seen)...)
	items = append(items, collectTCPListening(nettable.AfInet6, seen)...)

	// UDP bound (IPv4 + IPv6).
	items = append(items, collectUDPListening(nettable.AfInet, seen)...)
	items = append(items, collectUDPListening(nettable.AfInet6, seen)...)

	return items
}

func collectOpenSocketsNative() []models.OpenSocketInfo {
	var items []models.OpenSocketInfo
	seen := make(map[string]struct{})

	// TCP established (IPv4 + IPv6).
	items = append(items, collectTCPOpen(nettable.AfInet, seen)...)
	items = append(items, collectTCPOpen(nettable.AfInet6, seen)...)

	return items
}

func collectTCPListening(family int, seen map[string]struct{}) []models.ListeningPortInfo {
	rows := nettable.TCPRows(family)
	var items []models.ListeningPortInfo
	for _, row := range rows {
		// MIB_TCP_STATE_LISTEN = 2
		if row.State != 2 {
			continue
		}
		port := nettable.Port(row.LocalPort)
		if port <= 0 {
			continue
		}
		addr := ipToString(row.LocalAddr[:], family)
		pid := int(row.OwningPid)
		key := "tcp|" + addr + "|" + itoa(port) + "|" + itoa(pid)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, models.ListeningPortInfo{
			ProcessName: processName(pid),
			ProcessID:   pid,
			ProcessPath: processPath(pid),
			Protocol:    "tcp",
			Address:     addr,
			Port:        port,
		})
	}
	return items
}

func collectUDPListening(family int, seen map[string]struct{}) []models.ListeningPortInfo {
	rows := nettable.UDPRows(family)
	var items []models.ListeningPortInfo
	for _, row := range rows {
		port := nettable.Port(row.LocalPort)
		if port <= 0 {
			continue
		}
		addr := ipToString(row.LocalAddr[:], family)
		pid := int(row.OwningPid)
		key := "udp|" + addr + "|" + itoa(port) + "|" + itoa(pid)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, models.ListeningPortInfo{
			ProcessName: processName(pid),
			ProcessID:   pid,
			ProcessPath: processPath(pid),
			Protocol:    "udp",
			Address:     addr,
			Port:        port,
		})
	}
	return items
}

func collectTCPOpen(family int, seen map[string]struct{}) []models.OpenSocketInfo {
	rows := nettable.TCPRows(family)
	var items []models.OpenSocketInfo
	for _, row := range rows {
		// Skip LISTEN (2) and CLOSED (1).
		if row.State == 2 || row.State == 1 {
			continue
		}
		localPort := nettable.Port(row.LocalPort)
		remotePort := nettable.Port(row.RemotePort)
		if localPort <= 0 && remotePort <= 0 {
			continue
		}
		localAddr := ipToString(row.LocalAddr[:], family)
		remoteAddr := ipToString(row.RemoteAddr[:], family)
		pid := int(row.OwningPid)
		key := "tcp|" + localAddr + "|" + itoa(localPort) + "|" + remoteAddr + "|" + itoa(remotePort) + "|" + itoa(pid)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, models.OpenSocketInfo{
			ProcessName:   processName(pid),
			ProcessID:     pid,
			ProcessPath:   processPath(pid),
			LocalAddress:  localAddr,
			LocalPort:     localPort,
			RemoteAddress: remoteAddr,
			RemotePort:    remotePort,
			Protocol:      "tcp",
			Family:        familyString(family),
			State:         nettable.TCPStateName(row.State),
		})
	}
	return items
}

func ipToString(b []byte, family int) string {
	if family == nettable.AfInet6 {
		ip := make(net.IP, 16)
		copy(ip, b)
		return ip.String()
	}
	ip := net.IPv4(b[0], b[1], b[2], b[3])
	return ip.String()
}

func familyString(family int) string {
	if family == nettable.AfInet6 {
		return "IPv6"
	}
	return "IPv4"
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// processName returns the executable name for a PID.
func processName(pid int) string {
	path := processPath(pid)
	if path == "" {
		return ""
	}
	// Extract the base name.
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '\\' || path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// processPath returns the full executable path for a PID via
// QueryFullProcessImageNameW.
func processPath(pid int) string {
	if pid <= 0 {
		return ""
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)

	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	err = windows.QueryFullProcessImageName(handle, 0, &buf[0], &size)
	if err != nil {
		return ""
	}
	return syscall.UTF16ToString(buf[:size])
}
