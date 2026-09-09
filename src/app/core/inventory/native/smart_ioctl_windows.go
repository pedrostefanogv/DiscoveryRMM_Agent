//go:build windows

package native

import (
	"log"
	"syscall"
	"unsafe"
)

// Fallback SMART via DeviceIoControl(SMART_RCV_DRIVE_DATA) on \\.\PhysicalDriveN.
// Works without admin in most configurations, unlike Get-StorageReliabilityCounter
// which requires CIM/admin privileges. Reads ATA SMART attribute 9 (power-on hours).

const (
	smartRcvDriveData    = 0x0007C088 // IOCTL_SCSI_MINIPORT-based SMART_RCV_DRIVE_DATA
	smartRcvDriveDataRev = 0x0007C084 // unused placeholder
	smartAttributeCount  = 30
	smartAttrPowerHours  = 9 // ATA SMART attribute ID 9: power-on hours
	fileShareReadWrite   = 0x00000001 | 0x00000002
	openExisting         = 3
)

// SENDCMDINPARAMS structure for SMART_RCV_DRIVE_DATA (values identical to x86/x64 packing).
type sendCmdInParams struct {
	BufferSize  uint32
	DriveNumber byte
	Reserved    [3]byte
	Reserved2   [4]uint32
	IDERegs     [8]byte // IDEREGS: features, sectorCount, sectorNumber, cylLow, cylHigh, driveHead, command, reserved
	Buffer      [1]byte // variable
}

// sendCmdOutParams / SENDCMDOUTPARAMS layout: cBufferSize(4) + DRIVERSTATUS(12) + bBuffer(512)
const smartAttrDataOffset = 4 + 12 // offset of 512-byte attribute buffer in output

var (
	modkernel32Smart = syscall.NewLazyDLL("kernel32.dll")
	procCreateFileW  = modkernel32Smart.NewProc("CreateFileW")
	procDeviceIOCtrl = modkernel32Smart.NewProc("DeviceIoControl")
	procCloseHandle  = modkernel32Smart.NewProc("CloseHandle")
)

// collectSmartPowerOnHoursWMICompat returns a map of physical disk index -> power-on hours,
// read directly from ATA SMART data via DeviceIoControl. Returns an empty map when the
// system/disk does not support it (e.g. NVMe handled elsewhere, USB bridges, etc.).
func collectSmartPowerOnHoursWMICompat() map[int]int {
	result := make(map[int]int)
	for idx := 0; idx < 16; idx++ {
		hours := readSmartPowerOnHours(idx)
		if hours > 0 {
			result[idx] = hours
		}
	}
	return result
}

// readSmartPowerOnHours opens \\.\PhysicalDriveN and reads SMART attribute 9.
func readSmartPowerOnHours(diskIndex int) int {
	path, err := syscall.UTF16PtrFromString(`\\.\PhysicalDrive` + uitoa(diskIndex))
	if err != nil {
		return 0
	}

	handle, _, err := procCreateFileW.Call(
		uintptr(unsafe.Pointer(path)),
		uintptr(fileShareReadWrite),
		0,
		0,
		uintptr(openExisting),
		0,
		0,
	)
	if handle == uintptr(^uintptr(0)) { // INVALID_HANDLE_VALUE
		_ = err
		return 0
	}
	defer procCloseHandle.Call(handle)

	// IDENTIFY via SMART_RCV_DRIVE_DATA: command 0xB0, feature 0xD0 (SMART READ DATA).
	in := &sendCmdInParams{
		BufferSize:  512 + smartAttrDataOffset,
		DriveNumber: byte(diskIndex),
	}
	// IDERegs: Features=0xD0 (SMART READ DATA), CylLow=0x4F, CylHigh=0xC2, DriveHead=0xA0, Command=0xB0 (SMART)
	in.IDERegs[0] = 0xD0
	in.IDERegs[3] = 0x4F
	in.IDERegs[4] = 0xC2
	in.IDERegs[5] = 0xA0
	in.IDERegs[6] = 0xB0

	out := make([]byte, smartAttrDataOffset+512+16)
	var bytesReturned uint32

	ok, _, _ := procDeviceIOCtrl.Call(
		handle,
		uintptr(smartRcvDriveData),
		uintptr(unsafe.Pointer(in)),
		unsafe.Sizeof(*in),
		uintptr(unsafe.Pointer(&out[0])),
		uintptr(len(out)),
		uintptr(unsafe.Pointer(&bytesReturned)),
		0,
	)
	if ok == 0 || int(bytesReturned) < smartAttrDataOffset+512 {
		return 0
	}

	// Verify vendor-specific signature: out[smartAttrDataOffset+511] should be F7? Not guaranteed;
	// just parse the attribute table.
	attrs := out[smartAttrDataOffset : smartAttrDataOffset+512]
	return parseSmartAttr9Hours(attrs)
}

// parseSmartAttr9Hours parses a 512-byte ATA SMART attribute table and returns
// the raw (and normalized, whichever larger) value of attribute ID 9.
func parseSmartAttr9Hours(attrs []byte) int {
	for i := 0; i < smartAttributeCount; i++ {
		base := i * 12
		if base+12 > len(attrs) {
			break
		}
		id := attrs[base]
		if id == 0 {
			break // end of table
		}
		if id == smartAttrPowerHours {
			// 12-byte record: ID(1) flags(2) raw(6) reserved(1) current/worst(1+1)
			// Layout: [0]=ID, [1:3]=flags, [3]=current, [4]=worst, [5:11]=raw
			raw := attrs[base+5 : base+11]
			// Little-endian raw value.
			v := int(raw[0]) | int(raw[1])<<8 | int(raw[2])<<16 | int(raw[3])<<24 | int(raw[4])<<32 | int(raw[5])<<40
			// Some drives pack minutes into bytes 4-5 (36550 convention); hours = v % 65536
			// Heuristic: if v is huge, the top bytes may hold the minutes flag.
			if v > 0x1000000 { // suspiciously large (>1M hours)
				// Try 65536-mod interpretation (minutes packed).
				v = v % 65536
			}
			return v
		}
	}
	return 0
}

func uitoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(b[pos:])
}

var _ = log.Printf
