//go:build windows

package native

import (
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/models"
)

var (
	procGetExtendedTcpTable = modiphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = modiphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	tcpTableOwnerPidAll = 5
	udpTableOwnerPid    = 1
	afInet              = 2
	afInet6             = 23
)

// collectNetworkConnectionsNative returns listening ports and open sockets
// using GetExtendedTcpTable/GetExtendedUdpTable (no subprocess).
func collectNetworkConnectionsNative(ctx context.Context) ([]models.ListeningPortInfo, []models.OpenSocketInfo, error) {
	_ = ctx
	listening := collectListeningPortsNative()
	open := collectOpenSocketsNative()
	return listening, open, nil
}

func collectListeningPortsNative() []models.ListeningPortInfo {
	var items []models.ListeningPortInfo
	seen := make(map[string]struct{})

	// TCP listening (IPv4 + IPv6).
	items = append(items, collectTCPListening(afInet, seen)...)
	items = append(items, collectTCPListening(afInet6, seen)...)

	// UDP bound (IPv4 + IPv6).
	items = append(items, collectUDPListening(afInet, seen)...)
	items = append(items, collectUDPListening(afInet6, seen)...)

	return items
}

func collectOpenSocketsNative() []models.OpenSocketInfo {
	var items []models.OpenSocketInfo
	seen := make(map[string]struct{})

	// TCP established (IPv4 + IPv6).
	items = append(items, collectTCPOpen(afInet, seen)...)
	items = append(items, collectTCPOpen(afInet6, seen)...)

	return items
}

// tcpRowOwnerPid mirrors MIB_TCPROW_OWNER_PID.
type tcpRowOwnerPid struct {
	State      uint32
	LocalAddr  [4]byte
	LocalPort  uint32
	RemoteAddr [4]byte
	RemotePort uint32
	OwningPid  uint32
}

// tcp6RowOwnerPid mirrors MIB_TCP6ROW_OWNER_PID.
type tcp6RowOwnerPid struct {
	LocalAddr   [16]byte
	LocalScope  uint32
	LocalPort   uint32
	RemoteAddr  [16]byte
	RemoteScope uint32
	RemotePort  uint32
	State       uint32
	OwningPid   uint32
}

// udpRowOwnerPid mirrors MIB_UDPROW_OWNER_PID.
type udpRowOwnerPid struct {
	LocalAddr [4]byte
	LocalPort uint32
	OwningPid uint32
}

// udp6RowOwnerPid mirrors MIB_UDP6ROW_OWNER_PID.
type udp6RowOwnerPid struct {
	LocalAddr  [16]byte
	LocalScope uint32
	LocalPort  uint32
	OwningPid  uint32
}

func collectTCPListening(family int, seen map[string]struct{}) []models.ListeningPortInfo {
	rows := getTCPTable(family)
	var items []models.ListeningPortInfo
	for _, row := range rows {
		// MIB_TCP_STATE_LISTEN = 2
		if row.State != 2 {
			continue
		}
		port := int(binary.BigEndian.Uint16(portBytes(row.LocalPort)))
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
	rows := getUDPTable(family)
	var items []models.ListeningPortInfo
	for _, row := range rows {
		port := int(binary.BigEndian.Uint16(portBytes(row.LocalPort)))
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
	rows := getTCPTable(family)
	var items []models.OpenSocketInfo
	for _, row := range rows {
		// Skip LISTEN (2) and CLOSED (1).
		if row.State == 2 || row.State == 1 {
			continue
		}
		localPort := int(binary.BigEndian.Uint16(portBytes(row.LocalPort)))
		remotePort := int(binary.BigEndian.Uint16(portBytes(row.RemotePort)))
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
		})
	}
	return items
}

func getTCPTable(family int) []tcpRowOwnerPid {
	var rows []tcpRowOwnerPid
	if family == afInet {
		rows = getTCP4Table()
	} else {
		rows = getTCP6Table()
	}
	return rows
}

func getTCP4Table() []tcpRowOwnerPid {
	size := uint32(0)
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(afInet), uintptr(tcpTableOwnerPidAll), 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(afInet),
		uintptr(tcpTableOwnerPidAll),
		0,
	)
	if r != 0 {
		return nil
	}
	// First 4 bytes = number of rows.
	numRows := binary.LittleEndian.Uint32(buf[0:4])
	rows := make([]tcpRowOwnerPid, 0, numRows)
	offset := 4
	for i := uint32(0); i < numRows; i++ {
		if offset+int(unsafe.Sizeof(tcpRowOwnerPid{})) > len(buf) {
			break
		}
		row := *(*tcpRowOwnerPid)(unsafe.Pointer(&buf[offset]))
		rows = append(rows, row)
		offset += int(unsafe.Sizeof(tcpRowOwnerPid{}))
	}
	return rows
}

func getTCP6Table() []tcpRowOwnerPid {
	size := uint32(0)
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(afInet6), uintptr(tcpTableOwnerPidAll), 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(afInet6),
		uintptr(tcpTableOwnerPidAll),
		0,
	)
	if r != 0 {
		return nil
	}
	numRows := binary.LittleEndian.Uint32(buf[0:4])
	rows := make([]tcpRowOwnerPid, 0, numRows)
	offset := 4
	for i := uint32(0); i < numRows; i++ {
		if offset+int(unsafe.Sizeof(tcp6RowOwnerPid{})) > len(buf) {
			break
		}
		row6 := *(*tcp6RowOwnerPid)(unsafe.Pointer(&buf[offset]))
		rows = append(rows, tcpRowOwnerPid{
			State:      row6.State,
			LocalAddr:  ipv6ToIPv4(row6.LocalAddr),
			LocalPort:  row6.LocalPort,
			RemoteAddr: ipv6ToIPv4(row6.RemoteAddr),
			RemotePort: row6.RemotePort,
			OwningPid:  row6.OwningPid,
		})
		offset += int(unsafe.Sizeof(tcp6RowOwnerPid{}))
	}
	return rows
}

func getUDPTable(family int) []udpRowOwnerPid {
	var rows []udpRowOwnerPid
	if family == afInet {
		rows = getUDP4Table()
	} else {
		rows = getUDP6Table()
	}
	return rows
}

func getUDP4Table() []udpRowOwnerPid {
	size := uint32(0)
	procGetExtendedUdpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(afInet), uintptr(udpTableOwnerPid), 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedUdpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(afInet),
		uintptr(udpTableOwnerPid),
		0,
	)
	if r != 0 {
		return nil
	}
	numRows := binary.LittleEndian.Uint32(buf[0:4])
	rows := make([]udpRowOwnerPid, 0, numRows)
	offset := 4
	for i := uint32(0); i < numRows; i++ {
		if offset+int(unsafe.Sizeof(udpRowOwnerPid{})) > len(buf) {
			break
		}
		row := *(*udpRowOwnerPid)(unsafe.Pointer(&buf[offset]))
		rows = append(rows, row)
		offset += int(unsafe.Sizeof(udpRowOwnerPid{}))
	}
	return rows
}

func getUDP6Table() []udpRowOwnerPid {
	size := uint32(0)
	procGetExtendedUdpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(afInet6), uintptr(udpTableOwnerPid), 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedUdpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(afInet6),
		uintptr(udpTableOwnerPid),
		0,
	)
	if r != 0 {
		return nil
	}
	numRows := binary.LittleEndian.Uint32(buf[0:4])
	rows := make([]udpRowOwnerPid, 0, numRows)
	offset := 4
	for i := uint32(0); i < numRows; i++ {
		if offset+int(unsafe.Sizeof(udp6RowOwnerPid{})) > len(buf) {
			break
		}
		row6 := *(*udp6RowOwnerPid)(unsafe.Pointer(&buf[offset]))
		rows = append(rows, udpRowOwnerPid{
			LocalAddr: ipv6ToIPv4(row6.LocalAddr),
			LocalPort: row6.LocalPort,
			OwningPid: row6.OwningPid,
		})
		offset += int(unsafe.Sizeof(udp6RowOwnerPid{}))
	}
	return rows
}

// ipv6ToIPv4 converts an IPv4-mapped IPv6 address to its IPv4 bytes.
func ipv6ToIPv4(addr [16]byte) [4]byte {
	var out [4]byte
	// IPv4-mapped: ::ffff:a.b.c.d
	if addr[0] == 0 && addr[1] == 0 && addr[2] == 0 && addr[3] == 0 &&
		addr[4] == 0 && addr[5] == 0 && addr[6] == 0 && addr[7] == 0 &&
		addr[8] == 0 && addr[9] == 0 && addr[10] == 0xff && addr[11] == 0xff {
		copy(out[:], addr[12:16])
	}
	return out
}

func ipToString(b []byte, family int) string {
	if family == afInet6 {
		ip := make(net.IP, 16)
		copy(ip, b)
		return ip.String()
	}
	ip := net.IPv4(b[0], b[1], b[2], b[3])
	return ip.String()
}

func portBytes(port uint32) []byte {
	// Ports are stored in network byte order (big-endian) in the MIB structs.
	b := make([]byte, 2)
	b[0] = byte(port >> 8)
	b[1] = byte(port)
	return b
}

func familyString(family int) string {
	if family == afInet6 {
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
