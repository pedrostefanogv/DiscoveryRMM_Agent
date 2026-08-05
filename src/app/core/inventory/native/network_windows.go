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

// IP adapter address flags.
const (
	gaaFlagIncludePrefix = 0x00000010
	gaaFlagSkipAnycast   = 0x00000002
	gaaFlagSkipMulticast = 0x00000004
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
	Address            sockaddrStorage
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
	Address  sockaddrStorage
}

type ipAdapterGatewayAddress struct {
	Length   uint32
	Reserved uint32
	Next     *ipAdapterGatewayAddress
	Address  sockaddrStorage
}

type sockaddrStorage struct {
	Family uint16
	Data   [126]byte
}

// collectNetworksNative enumerates network interfaces via GetAdaptersAddresses.
func collectNetworksNative(ctx context.Context) ([]models.NetworkInfo, error) {
	var items []models.NetworkInfo

	// First call to get the required buffer size.
	size := uint32(0)
	r, _, _ := procGetAdaptersAddresses.Call(
		uintptr(windows.AF_UNSPEC),
		uintptr(gaaFlagIncludePrefix|gaaFlagSkipAnycast|gaaFlagSkipMulticast),
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
		uintptr(gaaFlagIncludePrefix|gaaFlagSkipAnycast|gaaFlagSkipMulticast),
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

func sockaddrToIP(sa *sockaddrStorage) net.IP {
	if sa == nil {
		return nil
	}
	switch sa.Family {
	case windows.AF_INET:
		// sockaddr_in: family(2) + port(2) + addr(4)
		addr := sa.Data[2:6]
		return net.IPv4(addr[0], addr[1], addr[2], addr[3])
	case windows.AF_INET6:
		// sockaddr_in6: family(2) + port(2) + flowinfo(4) + addr(16)
		addr := sa.Data[6:22]
		ip := make(net.IP, 16)
		copy(ip, addr)
		return ip
	default:
		return nil
	}
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
