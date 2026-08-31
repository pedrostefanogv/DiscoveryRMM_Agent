//go:build windows

package native

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"discovery/app/core/models"
)

const (
	logicalProcessorRelationshipCore    = 0 // RelationProcessorCore
	logicalProcessorRelationshipPackage = 3 // RelationProcessorPackage
	processorArchitectureAmd64          = 9
	processorArchitectureIntel          = 0
	processorArchitectureArm64          = 12

	computerNamePhysicalDnsFullyQualified = 5
	allProcessorGroups                    = 0xffff
)

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modntdll    = windows.NewLazySystemDLL("ntdll.dll")
	modiphlpapi = windows.NewLazySystemDLL("iphlpapi.dll")

	procGetComputerNameExW               = modkernel32.NewProc("GetComputerNameExW")
	procGetNativeSystemInfo              = modkernel32.NewProc("GetNativeSystemInfo")
	procGetLogicalProcessorInformationEx = modkernel32.NewProc("GetLogicalProcessorInformationEx")
	procGetActiveProcessorCount          = modkernel32.NewProc("GetActiveProcessorCount")
	procRtlGetVersion                    = modntdll.NewProc("RtlGetVersion")
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
	_ = ctx
	hw := models.HardwareInfo{}
	osInfo := models.OperatingSystem{}

	// Hostname via GetComputerNameExW (fully qualified DNS name).
	hw.Hostname = getComputerName()

	// OS version via RtlGetVersion (works on all Windows versions).
	osInfo.Name = "Windows"
	osInfo.Version, osInfo.Build = getOSVersion()

	// Nome comercial/edição do Windows via registry (ex.: "Windows 11 Pro").
	// Sobrescreve o genérico "Windows" quando disponível.
	if productName, displayVersion, ubr := getWindowsEdition(); productName != "" {
		osInfo.Name = productName
		// Complementa o build com UBR (ex.: build 22631 -> 22631.4460).
		if ubr != "" && osInfo.Build != "" {
			osInfo.Build = osInfo.Build + "." + ubr
		}
		// Guarda o feature update (ex.: "24H2") concatenado na versão.
		if displayVersion != "" && osInfo.Version != "" {
			osInfo.Version = osInfo.Version + " (" + displayVersion + ")"
		}
	}

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

// getWindowsEdition retorna o nome comercial do Windows (ex.: "Windows 11 Pro
// Insider Preview") lendo o registry:
//   - ProductName de HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion
//     (ex.: "Windows 11 Pro"; em Insider Preview pode ser "Windows 11 Home" etc.)
//   - DisplayVersion (ex.: "24H2") e UBR (build revision) para complementar.
//
// Best-effort: retorna strings vazias se o registry não estiver disponível.
func getWindowsEdition() (productName, displayVersion, ubr string) {
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return "", "", ""
	}
	defer key.Close()

	if v, _, err := key.GetStringValue("ProductName"); err == nil {
		productName = strings.TrimSpace(v)
	}
	if v, _, err := key.GetStringValue("DisplayVersion"); err == nil {
		displayVersion = strings.TrimSpace(v)
	}
	if v, _, err := key.GetIntegerValue("UBR"); err == nil {
		ubr = fmt.Sprintf("%d", v)
	}
	return productName, displayVersion, ubr
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
//
// Nota: em Windows modernos, ProcessorCoreCount e ProcessorLogicalCount não
// existem mais em HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0 (ou são
// armazenados como REG_QWORD/bits), fazendo GetIntegerValue retornar 0. Por
// isso a contagem cai em cascata: registro (DWORD/QWORD/binary) →
// GetNativeSystemInfo (lógicos) → placeholder físico = lógicos. A contagem
// física/real é refinada depois via WMI Win32_Processor (collectCPUsWMI) em
// collectHardwareNative.
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

	// 1) Tenta DWORD/QWORD (builds antigas expõem estes valores).
	pc, _, _ := key.GetIntegerValue("ProcessorCoreCount")
	lc, _, _ := key.GetIntegerValue("ProcessorLogicalCount")
	physicalCores, logicalCores = int(pc), int(lc)

	// 2) Fallback para REG_BINARY/QWORD de 8 bytes little-endian.
	if physicalCores == 0 {
		if raw, _, err := key.GetBinaryValue("ProcessorCoreCount"); err == nil && len(raw) >= 8 {
			physicalCores = int(binary.LittleEndian.Uint64(raw[:8]))
		}
	}
	if logicalCores == 0 {
		if raw, _, err := key.GetBinaryValue("ProcessorLogicalCount"); err == nil && len(raw) >= 8 {
			logicalCores = int(binary.LittleEndian.Uint64(raw[:8]))
		}
	}

	// 3) Fallback via GetLogicalProcessorInformationEx: determina núcleos
	// físicos e lógicos diretamente da afinidade do sistema (Vista+).
	if physicalCores <= 0 || logicalCores <= 0 {
		if phys, logi := getPhysicalAndLogicalProcessors(); phys > 0 || logi > 0 {
			if physicalCores <= 0 {
				physicalCores = phys
			}
			if logicalCores <= 0 {
				logicalCores = logi
			}
		}
	}

	// 4) Guarda de última instância: threads ativas via GetNativeSystemInfo.
	if logicalCores <= 0 {
		logicalCores = getNativeLogicalProcessorCount()
	}
	// Se ainda não soubermos um valor físico confiável, usamos o lógico como
	// estimativa inicial; o refinamento WMI (Win32_Processor) em
	// collectHardwareNative sobrescreverá com a contagem física real.
	if logicalCores <= 0 {
		logicalCores = 1
	}
	if physicalCores <= 0 {
		physicalCores = logicalCores
	}

	return brand, physicalCores, logicalCores
}

// getNativeLogicalProcessorCount returns the number of logical processors
// (threads) via GetNativeSystemInfo.dwNumberOfProcessors.
func getNativeLogicalProcessorCount() int {
	var si systemInfo
	procGetNativeSystemInfo.Call(uintptr(unsafe.Pointer(&si)))
	return int(si.dwNumberOfProcessors)
}

// getPhysicalAndLogicalProcessors returns the number of physical cores and
// logical processors.
//
// Physical cores are counted from GetLogicalProcessorInformationEx
// (RelationProcessorCore — one entry per physical core). Logical processors
// are counted via GetActiveProcessorCount(ALL_PROCESSOR_GROUPS), which
// returns the total across all processor groups (important on systems with
// more than 64 logical processors / multiple groups).
//
// Returns (0,0) when the APIs are unavailable, so callers can fall back.
func getPhysicalAndLogicalProcessors() (physical, logical int) {
	if procGetLogicalProcessorInformationEx == nil {
		return 0, 0
	}

	// Primeira chamada: determina o tamanho do buffer. A API retorna FALSE e
	// define ERROR_INSUFFICIENT_BUFFER (122) quando o buffer é insuficiente —
	// o que é esperado aqui (queremos apenas o tamanho necessário).
	var required uint32
	r, _, _ := procGetLogicalProcessorInformationEx.Call(
		uintptr(logicalProcessorRelationshipCore),
		0,
		uintptr(unsafe.Pointer(&required)),
	)
	if (r == 0 && required == 0) || required == 0 || required > 1<<20 {
		return 0, 0
	}

	buf := make([]byte, required)
	r, _, _ = procGetLogicalProcessorInformationEx.Call(
		uintptr(logicalProcessorRelationshipCore),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&required)),
	)
	if r == 0 {
		return 0, 0
	}

	// Estrutura retornada:
	//   SYSTEM_LOGICAL_PROCESSOR_INFORMATION_EX {
	//     LOGICAL_PROCESSOR_RELATIONSHIP Relationship; // offset 0 (4 bytes)
	//     DWORD Size;                                   // offset 4 (4 bytes)
	//     union {...}                                  // corpo
	//   }
	// Para RelationProcessorCore, cada entrada corresponde a UM núcleo físico
	// (independente de grupo). Contamos as entradas.
	const headerSize = 8 // Relationship (4) + Size (4)

	physical = 0
	for offset := 0; offset < len(buf); {
		if offset+headerSize > len(buf) {
			break
		}
		size := *(*uint32)(unsafe.Pointer(&buf[offset+4]))
		if size < uint32(headerSize) || offset+int(size) > len(buf) {
			break
		}
		// Relação 0 = RelationProcessorCore.
		rel := *(*uint32)(unsafe.Pointer(&buf[offset]))
		if rel == logicalProcessorRelationshipCore {
			physical++
		}
		offset += int(size)
	}

	if physical == 0 {
		return 0, 0
	}

	// Lógicos globais: GetActiveProcessorCount(ALL_PROCESSOR_GROUPS).
	if procGetActiveProcessorCount != nil {
		if n, _, _ := procGetActiveProcessorCount.Call(uintptr(allProcessorGroups)); n > 0 {
			logical = int(n)
		}
	}
	if logical <= 0 {
		logical = getNativeLogicalProcessorCount()
	}

	return physical, logical
}

// popcount64 returns the number of set bits in a 64-bit word (used to count
// logical processors within a core's processor mask).
func popcount64(x uint64) int {
	c := 0
	for x != 0 {
		x &= x - 1
		c++
	}
	return c
}
