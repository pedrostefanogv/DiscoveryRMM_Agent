package inventory

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"discovery/internal/models"
)

// -----------------------------------------------------------------------
// Byte-size constants
// -----------------------------------------------------------------------

const (
	bytesPerGB = 1024 * 1024 * 1024

	// memorySizeAmbiguityThreshold: values below this are assumed to be in
	// MB rather than bytes (some osquery providers report MB directly).
	memorySizeAmbiguityThreshold = 4 * bytesPerGB
)

// -----------------------------------------------------------------------
// Generic row helpers
// -----------------------------------------------------------------------

// getString extracts a string value from an untyped map.
// Numeric types are formatted without trailing zeros.
func getString(row map[string]any, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// parseInt parses a trimmed string as an integer; returns 0 on failure.
func parseInt(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	return n
}

// parseFloat parses a trimmed string as a float64; returns 0 on failure.
func parseFloat(value string) float64 {
	n, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return n
}

// parseBoolLoose interprets a string as a boolean using a broad set of
// truthy values commonly returned by osquery and WMI.
// Recognized truthy values: "1", "true", "yes", "up", "connected".
func parseBoolLoose(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	return v == "1" || v == "true" || v == "yes" || v == "up" || v == "connected"
}

// round2 rounds a float64 to 2 decimal places using math.Round,
// which handles both positive and negative values correctly.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// firstNonEmpty returns the first non-blank string among the arguments.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// firstRow returns the first element of a row slice, or an empty map.
func firstRow(rows []map[string]any) map[string]any {
	if len(rows) == 0 {
		return map[string]any{}
	}
	return rows[0]
}

// appendCSV appends value to current as a comma-separated list,
// avoiding duplicates.
func appendCSV(current string, value string) string {
	if strings.TrimSpace(value) == "" {
		return current
	}
	if strings.TrimSpace(current) == "" {
		return value
	}
	if strings.Contains(current, value) {
		return current
	}
	return current + ", " + value
}

// -----------------------------------------------------------------------
// Hardware sanitization
// -----------------------------------------------------------------------

// sanitizeHardwareFields blanks out placeholder/OEM strings from hardware info.
func sanitizeHardwareFields(report *models.InventoryReport) {
	if report == nil {
		return
	}

	report.Hardware.MotherboardManufacturer = sanitizeHardwareValue(report.Hardware.MotherboardManufacturer)
	report.Hardware.MotherboardModel = sanitizeHardwareValue(report.Hardware.MotherboardModel)
	report.Hardware.MotherboardSerial = sanitizeHardwareValue(report.Hardware.MotherboardSerial)
	report.Hardware.BIOSVendor = sanitizeHardwareValue(report.Hardware.BIOSVendor)
	report.Hardware.BIOSVersion = sanitizeHardwareValue(report.Hardware.BIOSVersion)
	report.Hardware.BIOSReleaseDate = sanitizeHardwareValue(report.Hardware.BIOSReleaseDate)
	report.Hardware.BIOSSerial = sanitizeHardwareValue(report.Hardware.BIOSSerial)
}

func sanitizeHardwareValue(value string) string {
	v := strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	if v == "" {
		return ""
	}

	l := strings.ToLower(v)
	switch l {
	case "default string", "to be filled by o.e.m.", "to be filled by oem",
		"o.e.m.", "oem", "none", "n/a", "na", "unknown",
		"system serial number", "invalid", "not applicable":
		return ""
	}

	if strings.HasPrefix(l, "default") || strings.HasPrefix(l, "to be filled") {
		return ""
	}

	return v
}
