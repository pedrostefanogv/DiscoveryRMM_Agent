// Package export — shared redaction logic for all export formats.
package export

import "discovery/internal/models"

// Redacted is the placeholder used in place of sensitive field values.
const Redacted = "***"

// RedactHardware returns a copy of HardwareInfo with sensitive fields masked.
func RedactHardware(hw models.HardwareInfo) models.HardwareInfo {
	hw.Hostname = Redacted
	hw.MotherboardSerial = Redacted
	hw.BIOSSerial = Redacted
	return hw
}
