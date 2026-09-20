//go:build windows

package native

import (
	"context"
	"fmt"
	"strings"

	"discovery/app/core/models"
)

// printerStatusString maps Win32_Printer.PrinterStatus (int) to a readable
// label. Os rótulos em inglês casam com a heurística de cor do console web
// (contém "ready" → success; "offline" → danger; "warn"/"paus" → warning).
func printerStatusString(status int) string {
	switch status {
	case 3:
		return "Ready"
	case 4:
		return "Printing"
	case 5:
		return "Warmup"
	case 6:
		return "Stopped Printing"
	case 7:
		return "Offline"
	case 1:
		return "Other"
	case 2:
		return "Unknown"
	default:
		return ""
	}
}

// printerIsNetwork deriva o flag de impressora de rede. Win32_Printer não
// expõe booleano de rede; usa os bits de atributo do winspool
// (PRINTER_ATTRIBUTE_NETWORK = 0x10), com heurística para portas TCP/IP RAW
// (IP_x.x.x.x) criadas por drivers que não marcam o bit.
func printerIsNetwork(attributes int, portName string) bool {
	if attributes&0x10 != 0 {
		return true
	}
	port := strings.ToUpper(strings.TrimSpace(portName))
	return strings.HasPrefix(port, "IP_") ||
		strings.HasPrefix(port, "HTTP://") ||
		strings.HasPrefix(port, "HTTPS://")
}

// collectPrintersNative enumerates installed printers via WMI Win32_Printer
// (COM, zero subprocess) — mesma estratégia dos demais coletores nativos.
// Bits de atributo do winspool: SHARED=0x8, NETWORK=0x10, LOCAL=0x40.
func collectPrintersNative(ctx context.Context) ([]models.PrinterInfo, error) {
	rows, err := wmiQuery(wmiNamespace,
		"SELECT Name, DriverName, PortName, PrinterStatus, Default, Shared, ShareName, Location, Attributes FROM Win32_Printer")
	if err != nil {
		return nil, fmt.Errorf("wmi Win32_Printer: %w", err)
	}

	printers := make([]models.PrinterInfo, 0, len(rows))
	for _, row := range rows {
		name := wmiString(row, "Name")
		if name == "" {
			continue
		}

		portName := wmiString(row, "PortName")
		printers = append(printers, models.PrinterInfo{
			Name:             name,
			DriverName:       wmiString(row, "DriverName"),
			PortName:         portName,
			PrinterStatus:    printerStatusString(wmiInt(row, "PrinterStatus")),
			IsDefault:        wmiInt(row, "Default") != 0,
			IsNetworkPrinter: printerIsNetwork(wmiInt(row, "Attributes"), portName),
			Shared:           wmiInt(row, "Shared") != 0,
			ShareName:        wmiString(row, "ShareName"),
			Location:         wmiString(row, "Location"),
		})
	}
	return printers, nil
}
