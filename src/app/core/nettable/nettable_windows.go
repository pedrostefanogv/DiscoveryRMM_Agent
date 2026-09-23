//go:build windows

// Package nettable encapsula o acesso às tabelas TCP/UDP do Windows
// (GetExtendedTcpTable/GetExtendedUdpTable). É o ponto único de verdade para as
// structs MIB, o decode de porta e os nomes de estado, compartilhado pela
// coleta de inventário (inventory/native) e pelas métricas por processo
// (sysctrl) — paga a dívida da duplicação de tcpRowOwnerPid/udpRowOwnerPid
// entre os dois pacotes.
package nettable

import (
	"encoding/binary"
	"syscall"
	"unsafe"
)

var (
	modiphlpapi             = syscall.NewLazyDLL("iphlpapi.dll")
	procGetExtendedTcpTable = modiphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = modiphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	TcpTableOwnerPidAll = 5
	UdpTableOwnerPid    = 1

	AfInet  = 2
	AfInet6 = 23
)

// TcpRowOwnerPid espelha MIB_TCPROW_OWNER_PID.
type TcpRowOwnerPid struct {
	State      uint32
	LocalAddr  [4]byte
	LocalPort  uint32
	RemoteAddr [4]byte
	RemotePort uint32
	OwningPid  uint32
}

// Tcp6RowOwnerPid espelha MIB_TCP6ROW_OWNER_PID.
type Tcp6RowOwnerPid struct {
	LocalAddr   [16]byte
	LocalScope  uint32
	LocalPort   uint32
	RemoteAddr  [16]byte
	RemoteScope uint32
	RemotePort  uint32
	State       uint32
	OwningPid   uint32
}

// UdpRowOwnerPid espelha MIB_UDPROW_OWNER_PID.
type UdpRowOwnerPid struct {
	LocalAddr [4]byte
	LocalPort uint32
	OwningPid uint32
}

// Udp6RowOwnerPid espelha MIB_UDP6ROW_OWNER_PID.
type Udp6RowOwnerPid struct {
	LocalAddr  [16]byte
	LocalScope uint32
	LocalPort  uint32
	OwningPid  uint32
}

// Port decodifica o campo de porta dos structs MIB (dwLocalPort/dwRemotePort).
// As tabelas guardam a porta em network byte order nos 16 bits baixos do DWORD;
// em hosts little-endian o uint32 lê como a porta com os bytes trocados
// (ex.: 41080 lê-se 0x78A0 = 30880), então aplica-se ntohs aqui. Antes da
// correção, 135 (RPC) era reportado como 34560 e 3389 (RDP) como 15629.
func Port(port uint32) int {
	return int((port&0xFF)<<8 | (port>>8)&0xFF)
}

// TCPStateName converte o código MIB_TCP_STATE em nome legível.
func TCPStateName(state uint32) string {
	switch state {
	case 1:
		return "CLOSED"
	case 2:
		return "LISTEN"
	case 3:
		return "SYN_SENT"
	case 4:
		return "SYN_RCVD"
	case 5:
		return "ESTABLISHED"
	case 6:
		return "FIN_WAIT1"
	case 7:
		return "FIN_WAIT2"
	case 8:
		return "CLOSE_WAIT"
	case 9:
		return "CLOSING"
	case 10:
		return "LAST_ACK"
	case 11:
		return "TIME_WAIT"
	case 12:
		return "DELETE_TCB"
	default:
		return ""
	}
}

// TCPRows retorna as linhas da tabela TCP da família informada. Para IPv6 as
// linhas são normalizadas em TcpRowOwnerPid (endereços IPv4-mapped viram IPv4;
// os demais ficam 0.0.0.0), de modo que os consumidores iteram uma única struct.
func TCPRows(family int) []TcpRowOwnerPid {
	if family == AfInet {
		return tcp4Rows()
	}
	return tcp6Rows()
}

// UDPRows retorna as linhas da tabela UDP da família informada (mesma
// normalização de TCPRows para IPv6).
func UDPRows(family int) []UdpRowOwnerPid {
	if family == AfInet {
		return udp4Rows()
	}
	return udp6Rows()
}

func tcp4Rows() []TcpRowOwnerPid {
	size := uint32(0)
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(AfInet), uintptr(TcpTableOwnerPidAll), 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(AfInet),
		uintptr(TcpTableOwnerPidAll),
		0,
	)
	if r != 0 {
		return nil
	}
	// Primeiros 4 bytes = número de linhas.
	numRows := binary.LittleEndian.Uint32(buf[0:4])
	rows := make([]TcpRowOwnerPid, 0, numRows)
	offset := 4
	for i := uint32(0); i < numRows; i++ {
		if offset+int(unsafe.Sizeof(TcpRowOwnerPid{})) > len(buf) {
			break
		}
		row := *(*TcpRowOwnerPid)(unsafe.Pointer(&buf[offset]))
		rows = append(rows, row)
		offset += int(unsafe.Sizeof(TcpRowOwnerPid{}))
	}
	return rows
}

func tcp6Rows() []TcpRowOwnerPid {
	size := uint32(0)
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(AfInet6), uintptr(TcpTableOwnerPidAll), 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(AfInet6),
		uintptr(TcpTableOwnerPidAll),
		0,
	)
	if r != 0 {
		return nil
	}
	numRows := binary.LittleEndian.Uint32(buf[0:4])
	rows := make([]TcpRowOwnerPid, 0, numRows)
	offset := 4
	for i := uint32(0); i < numRows; i++ {
		if offset+int(unsafe.Sizeof(Tcp6RowOwnerPid{})) > len(buf) {
			break
		}
		row6 := *(*Tcp6RowOwnerPid)(unsafe.Pointer(&buf[offset]))
		rows = append(rows, TcpRowOwnerPid{
			State:      row6.State,
			LocalAddr:  ipv6ToIPv4(row6.LocalAddr),
			LocalPort:  row6.LocalPort,
			RemoteAddr: ipv6ToIPv4(row6.RemoteAddr),
			RemotePort: row6.RemotePort,
			OwningPid:  row6.OwningPid,
		})
		offset += int(unsafe.Sizeof(Tcp6RowOwnerPid{}))
	}
	return rows
}

func udp4Rows() []UdpRowOwnerPid {
	size := uint32(0)
	procGetExtendedUdpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(AfInet), uintptr(UdpTableOwnerPid), 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedUdpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(AfInet),
		uintptr(UdpTableOwnerPid),
		0,
	)
	if r != 0 {
		return nil
	}
	numRows := binary.LittleEndian.Uint32(buf[0:4])
	rows := make([]UdpRowOwnerPid, 0, numRows)
	offset := 4
	for i := uint32(0); i < numRows; i++ {
		if offset+int(unsafe.Sizeof(UdpRowOwnerPid{})) > len(buf) {
			break
		}
		row := *(*UdpRowOwnerPid)(unsafe.Pointer(&buf[offset]))
		rows = append(rows, row)
		offset += int(unsafe.Sizeof(UdpRowOwnerPid{}))
	}
	return rows
}

func udp6Rows() []UdpRowOwnerPid {
	size := uint32(0)
	procGetExtendedUdpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(AfInet6), uintptr(UdpTableOwnerPid), 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedUdpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(AfInet6),
		uintptr(UdpTableOwnerPid),
		0,
	)
	if r != 0 {
		return nil
	}
	numRows := binary.LittleEndian.Uint32(buf[0:4])
	rows := make([]UdpRowOwnerPid, 0, numRows)
	offset := 4
	for i := uint32(0); i < numRows; i++ {
		if offset+int(unsafe.Sizeof(Udp6RowOwnerPid{})) > len(buf) {
			break
		}
		row6 := *(*Udp6RowOwnerPid)(unsafe.Pointer(&buf[offset]))
		rows = append(rows, UdpRowOwnerPid{
			LocalAddr: ipv6ToIPv4(row6.LocalAddr),
			LocalPort: row6.LocalPort,
			OwningPid: row6.OwningPid,
		})
		offset += int(unsafe.Sizeof(Udp6RowOwnerPid{}))
	}
	return rows
}

// ipv6ToIPv4 converte um endereço IPv4-mapped (::ffff:a.b.c.d) para IPv4.
func ipv6ToIPv4(addr [16]byte) [4]byte {
	var out [4]byte
	if addr[0] == 0 && addr[1] == 0 && addr[2] == 0 && addr[3] == 0 &&
		addr[4] == 0 && addr[5] == 0 && addr[6] == 0 && addr[7] == 0 &&
		addr[8] == 0 && addr[9] == 0 && addr[10] == 0xff && addr[11] == 0xff {
		copy(out[:], addr[12:16])
	}
	return out
}
