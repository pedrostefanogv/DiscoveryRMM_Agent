//go:build windows

package native

import (
	"context"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"discovery/app/core/models"
)

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modntdll    = windows.NewLazySystemDLL("ntdll.dll")
	modiphlpapi = windows.NewLazySystemDLL("iphlpapi.dll")

	procGetComputerNameExW  = modkernel32.NewProc("GetComputerNameExW")
	procGetNativeSystemInfo = modkernel32.NewProc("GetNativeSystemInfo")
	procRtlGetVersion       = modntdll.NewProc("RtlGetVersion")
)

const (
	computerNamePhysicalDnsFullyQualified = 5
	processorArchitectureAmd64            = 9
	processorArchitectureIntel            = 0
	processorArchitectureArm64            = 12
)

// osVersionInfoEx mirrors the RTL_OSVERSIONINFOW structure.
type osVersionInfoEx struct {
	OSVersionInfoSize uint32
	MajorVersion      uint32
	MinorVersion      uint32
	BuildNumber       uint32
	PlatformID        uint32
	CSDVersion        [128]uint16
}

// systemInfo mirrors the SYSTEM_INFO structure.
type systemInfo struct {
	wProcessorArchitecture      uint16
	wReserved                   uint16
	dwPageSize                  uint32
	lpMinimumApplicationAddress uintptr
	lpMaximumApplicationAddress uintptr
	dwActiveProcessorMask       uintptr
	dwNumberOfProcessors        uint32
	dwProcessorType             uint32
	dwAllocationGranularity     uint32
	wProcessorLevel             uint16
	wProcessorRevision          uint16
}

// collectSystemInfoNative collects hostname, OS version and basic hardware
// identity using native Win32 APIs. It returns a partially-filled
// HardwareInfo (hostname, CPU brand, cores, memory) and OperatingSystem.
func collectSystemInfoNative(ctx context.Context) (models.HardwareInfo, models.OperatingSystem, error) {
	hw := models.HardwareInfo{}
	osInfo := models.OperatingSystem{}

	// Hostname via GetComputerNameExW (fully qualified DNS name).
	hw.Hostname = getComputerName()

	// OS version via RtlGetVersion (works on all Windows versions).
	osInfo.Name = "Windows"
	osInfo.Version, osInfo.Build = getOSVersion()

	// Architecture via GetNativeSystemInfo.
	osInfo.Architecture = getArchitecture()

	// CPU brand and core counts via registry (HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0).
	cpuBrand, physicalCores, logicalCores := getCPUInfoFromRegistry()
	hw.CPU = cpuBrand
	hw.Cores = physicalCores
	hw.LogicalCores = logicalCores

	// Total physical memory via GlobalMemoryStatusEx.
	if totalGB, _, _, ok := getMemoryStatus(); ok {
		hw.MemoryGB = totalGB
	}

	return hw, osInfo, nil
}

func getComputerName() string {
	buf := make([]uint16, 256)
	size := uint32(len(buf))
	r, _, _ := procGetComputerNameExW.Call(
		uintptr(computerNamePhysicalDnsFullyQualified),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if r == 0 {
		// Fallback to the plain computer name.
		n, err := windows.ComputerName()
		if err == nil {
			return n
		}
		return ""
	}
	return syscall.UTF16ToString(buf)
}

func getOSVersion() (version, build string) {
	info := osVersionInfoEx{}
	info.OSVersionInfoSize = uint32(unsafe.Sizeof(info))
	r, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&info)))
	if r != 0 {
		return "", ""
	}
	version = fmt.Sprintf("%d.%d", info.MajorVersion, info.MinorVersion)
	build = fmt.Sprintf("%d", info.BuildNumber)
	return version, build
}

func getArchitecture() string {
	var si systemInfo
	procGetNativeSystemInfo.Call(uintptr(unsafe.Pointer(&si)))
	switch si.wProcessorArchitecture {
	case processorArchitectureAmd64:
		return "x86_64"
	case processorArchitectureArm64:
		return "arm64"
	case processorArchitectureIntel:
		return "x86"
	default:
		return "unknown"
	}
}

// getCPUInfoFromRegistry reads CPU brand and core counts from the registry.
func getCPUInfoFromRegistry() (brand string, physicalCores, logicalCores int) {
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\CentralProcessor\0`,
		windows.KEY_READ,
	)
	if err != nil {
		return "", 0, 0
	}
	defer key.Close()

	brand, _, _ = key.GetStringValue("ProcessorNameString")
	pc, _, _ := key.GetIntegerValue("ProcessorCoreCount")
	lc, _, _ := key.GetIntegerValue("ProcessorLogicalCount")
	return brand, int(pc), int(lc)
}
