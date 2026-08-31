//go:build windows

package native

import (
	"context"
	"net"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/models"
)

var procGetAdaptersAddresses = modiphlpapi.NewProc("GetAdaptersAddresses")
var procGetBestRoute2 = modiphlpapi.NewProc("GetBestRoute2")

// IP adapter address flags.
const (
	gaaFlagIncludePrefix = 0x00000010
	gaaFlagSkipAnycast   = 0x00000002
	gaaFlagSkipMulticast = 0x00000004
	gaaFlagIncludeGateways = 0x00000040
)

// ipAdapterAddresses mirrors the IP_ADAPTER_ADDRESSES structure (partial).
type ipAdapterAddresses struct {
	Length                 uint32
	IfIndex                uint32
	Next                   *ipAdapterAddresses
	AdapterName            *byte
	FirstUnicastAddress    *ipAdapterUnicastAddress
	FirstAnycastAddress    uintptr
	FirstMulticastAddress  uintptr
	FirstDnsServerAddress  *ipAdapterDnsServerAdapter
	DNSSuffix              *uint16
	Description            *uint16
	FriendlyName           *uint16
	PhysicalAddress        [8]byte
	PhysicalAddressLength  uint32
	Flags                  uint32
	Mtu                    uint32
	IfType                 uint32
	OperStatus             uint32
	IPv6IfIndex            uint32
	ZoneIndices            [16]uint32
	FirstPrefix            uintptr
	TransmitLinkSpeed      uint64
	ReceiveLinkSpeed       uint64
	FirstWinsServerAddress uintptr
	FirstGatewayAddress    *ipAdapterGatewayAddress
	Ipv4Metric             uint32
	Ipv6Metric             uint32
	Luid                   uint64
	Dhcpv4Server           *sockaddrStorage
	CompartmentId          uint32
	NetworkGuid            [16]byte
	ConnectionType         uint32
	TunnelType             uint32
	Dhcpv6Server           *sockaddrStorage
	Dhcpv6ClientDuid       [130]byte
	Dhcpv6ClientDuidLength uint32
	Dhcpv6Iaid             uint32
	FirstDnsSuffix         uintptr
}

type ipAdapterUnicastAddress struct {
	Length             uint32
	Flags              uint32
	Next               *ipAdapterUnicastAddress
	Address            socketAddress
	PrefixOrigin       int32
	SuffixOrigin       int32
	DadState           int32
	ValidLifetime      uint32
	PreferredLifetime  uint32
	LeaseLifetime      uint32
	OnLinkPrefixLength uint8
}

type ipAdapterDnsServerAdapter struct {
	Length   uint32
	Reserved uint32
	Next     *ipAdapterDnsServerAdapter
	Address  socketAddress
}

type ipAdapterGatewayAddress struct {
	Length   uint32
	Reserved uint32
	Next     *ipAdapterGatewayAddress
	Address  socketAddress
}

type socketAddress struct {
	lpSockaddr *sockaddrStorage
	iSockaddrLength int32
}

type sockaddrStorage struct {
	Family uint16
	Data   [126]byte
}

// collectNetworksNative enumerates network interfaces via GetAdaptersAddresses.
func collectNetworksNative(ctx context.Context) ([]models.NetworkInfo, error) {
	_ = ctx
	var items []models.NetworkInfo

	// First call to get the required buffer size.
	size := uint32(0)
	r, _, _ := procGetAdaptersAddresses.Call(
		uintptr(windows.AF_UNSPEC),
		uintptr(gaaFlagIncludePrefix|gaaFlagSkipAnycast|gaaFlagSkipMulticast|gaaFlagIncludeGateways),
		0,
		0,
		uintptr(unsafe.Pointer(&size)),
	)
	if r == uintptr(windows.ERROR_BUFFER_OVERFLOW) || size == 0 {
		// size now holds the required buffer size.
	}

	buf := make([]byte, size)
	r, _, _ = procGetAdaptersAddresses.Call(
		uintptr(windows.AF_UNSPEC),
		uintptr(gaaFlagIncludePrefix|gaaFlagSkipAnycast|gaaFlagSkipMulticast|gaaFlagIncludeGateways),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if r != 0 {
		return items, nil
	}

	adapter := (*ipAdapterAddresses)(unsafe.Pointer(&buf[0]))
	for adapter != nil {
		info := models.NetworkInfo{
			Interface:        windows.BytePtrToString(adapter.AdapterName),
			FriendlyName:     utf16PtrToString(adapter.FriendlyName),
			Description:      utf16PtrToString(adapter.Description),
			MAC:              formatMAC(adapter.PhysicalAddress[:adapter.PhysicalAddressLength]),
			MTU:              int(adapter.Mtu),
			LinkSpeedMbps:    int(adapter.TransmitLinkSpeed / 1000000),
			ConnectionStatus: operStatusString(adapter.OperStatus),
			Enabled:          adapter.OperStatus == 1, // IfOperStatusUp
			PhysicalAdapter:  adapter.IfType == 6,     // IF_TYPE_ETHERNET_CSMACD
			Type:             ifTypeString(adapter.IfType),
		}

		// Unicast addresses.
		var ipv4s, ipv6s []string
		for ua := adapter.FirstUnicastAddress; ua != nil; ua = ua.Next {
			ip := sockaddrToIP(&ua.Address)
			if ip == nil {
				continue
			}
			if ip.To4() != nil {
				ipv4s = append(ipv4s, ip.String())
			} else {
				ipv6s = append(ipv6s, ip.String())
			}
		}
		info.IPv4 = strings.Join(ipv4s, ", ")
		info.IPv6 = strings.Join(ipv6s, ", ")

		// Gateways.
		var gateways []string
		for gw := adapter.FirstGatewayAddress; gw != nil; gw = gw.Next {
			if ip := sockaddrToIP(&gw.Address); ip != nil {
				gateways = append(gateways, ip.String())
			}
		}
		// Fallback: FirstGatewayAddress pode vir NULL mesmo com GAA_FLAG_INCLUDE_GATEWAYS.
		// Usa GetBestRoute para descobrir o gateway do primeiro IP unicast do adaptador.
		if len(gateways) == 0 {
			for ua := adapter.FirstUnicastAddress; ua != nil; ua = ua.Next {
				ip := sockaddrToIP(&ua.Address)
				if ip == nil || ip.To4() == nil || ip.IsLinkLocalUnicast() || ip.IsLoopback() {
					continue
				}
				if gwIP := bestRouteGateway(ip); gwIP != "" {
					gateways = append(gateways, gwIP)
					break
				}
			}
		}
		info.Gateway = strings.Join(gateways, ", ")

		// DNS servers.
		var dns []string
		for d := adapter.FirstDnsServerAddress; d != nil; d = d.Next {
			if ip := sockaddrToIP(&d.Address); ip != nil {
				dns = append(dns, ip.String())
			}
		}
		info.DNSServers = strings.Join(dns, ", ")

		// DHCP enabled: check if DHCPv4 server is set.
		info.DHCPEnabled = adapter.Dhcpv4Server != nil

		if info.Interface != "" || info.FriendlyName != "" {
			items = append(items, info)
		}

		adapter = adapter.Next
	}

	return items, nil
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	return syscall.UTF16ToString(unsafe.Slice(p, 512))
}

func formatMAC(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = strings.ToUpper(strings.TrimSpace(net.HardwareAddr{v}.String()))
	}
	return strings.Join(parts, ":")
}

func sockaddrToIP(sa *socketAddress) net.IP {
	if sa == nil || sa.lpSockaddr == nil {
		return nil
	}
	ss := sa.lpSockaddr
	switch ss.Family {
	case windows.AF_INET:
		// sockaddr_in: family(2) + port(2) + addr(4)
		addr := ss.Data[2:6]
		return net.IPv4(addr[0], addr[1], addr[2], addr[3])
	case windows.AF_INET6:
		// sockaddr_in6: family(2) + port(2) + flowinfo(4) + addr(16)
		addr := ss.Data[6:22]
		ip := make(net.IP, 16)
		copy(ip, addr)
		return ip
	default:
		return nil
	}
}

// bestRouteGateway resolves the next-hop gateway for the given source IP via GetBestRoute2.
// A row é lida como buffer bruto pois o layout de MIB_IPFORWARD_ROW2 varia com alinhamento;
// os offsets do NextHop (family=44, addr=48) foram validados empiricamente no Windows x64.
func bestRouteGateway(src net.IP) string {
	src4 := src.To4()
	if src4 == nil {
		return ""
	}
	srcAddr := &windows.RawSockaddrInet4{Family: windows.AF_INET}
	copy(srcAddr.Addr[:], src4)
	dst := &windows.RawSockaddrInet4{Family: windows.AF_INET} // rota default
	var row [128]byte
	var luid uint64
	var path [512]byte
	r, _, _ := procGetBestRoute2.Call(
		uintptr(unsafe.Pointer(&luid)),
		0,
		uintptr(unsafe.Pointer(srcAddr)),
		uintptr(unsafe.Pointer(dst)),
		0,
		uintptr(unsafe.Pointer(&row[0])),
		uintptr(unsafe.Pointer(&path[0])),
	)
	if r != 0 {
		return ""
	}
	family := uint16(row[44]) | uint16(row[45])<<8
	if family != windows.AF_INET {
		return ""
	}
	nh := net.IPv4(row[48], row[49], row[50], row[51])
	if nh.IsUnspecified() {
		return "" // rota on-link, sem gateway
	}
	return nh.String()
}

func operStatusString(status uint32) string {
	switch status {
	case 1:
		return "Up"
	case 2:
		return "Down"
	case 3:
		return "Testing"
	case 4:
		return "Unknown"
	case 5:
		return "Dormant"
	case 6:
		return "NotPresent"
	case 7:
		return "LowerLayerDown"
	default:
		return "Unknown"
	}
}

func ifTypeString(t uint32) string {
	switch t {
	case 6:
		return "Ethernet"
	case 71:
		return "IEEE80211"
	case 24:
		return "Loopback"
	case 53:
		return "PPP"
	case 131:
		return "Tunnel"
	default:
		return "Other"
	}
}
