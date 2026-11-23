//go:build windows

package native

import (
	"context"

	"discovery/app/core/models"
	"discovery/app/core/power"
)

// Códigos de Win32_Battery.BatteryStatus.
const (
	batteryStatusDischarging      = 1
	batteryStatusOnAC             = 2
	batteryStatusFullyCharged     = 3
	batteryStatusLow              = 4
	batteryStatusCritical         = 5
	batteryStatusCharging         = 6
	batteryStatusChargingAndHigh  = 7
	batteryStatusChargingAndLow   = 8
	batteryStatusChargingCritical = 9
	batteryStatusUndefined        = 10
	batteryStatusPartiallyCharged = 11
)

// collectBatteryNative devolve o inventário de bateria via WMI (Win32_Battery)
// enriquecido com o estado de energia AO VIVO de GetSystemPowerStatus.
//
// O estado de energia vem SEMPRE de power.Read() — a mesma fonte usada pelo
// heartbeat e pelo adiamento de deploy. Antes disso o painel não conseguia
// responder "está na tomada?" (ACLineStatus não era persistido) e o campo
// Charging era calculado com a condição invertida (ACLineStatus == 0 significa
// NA BATERIA, não na tomada).
func collectBatteryNative(ctx context.Context) ([]models.BatteryInfo, error) {
	_ = ctx

	powerStatus, powerOK := power.Read()

	var items []models.BatteryInfo

	// WMI Win32_Battery fornece os detalhes (capacidade de projeto e máxima,
	// química, série).
	rows, err := wmiQuery(wmiNamespace, "SELECT Name, Manufacturer, DeviceID, EstimatedChargeRemaining, BatteryStatus, DesignCapacity, FullChargeCapacity, Chemistry, SerialNumber FROM Win32_Battery")
	if err == nil && len(rows) > 0 {
		for _, row := range rows {
			status := wmiInt(row, "BatteryStatus")
			info := models.BatteryInfo{
				Manufacturer:        wmiString(row, "Manufacturer"),
				Model:               wmiString(row, "Name"),
				SerialNumber:        wmiString(row, "SerialNumber"),
				PercentRemaining:    wmiInt(row, "EstimatedChargeRemaining"),
				DesignedCapacityMAh: wmiInt(row, "DesignCapacity"),
				MaxCapacityMAh:      wmiInt(row, "FullChargeCapacity"),
				Chemistry:           wmiString(row, "Chemistry"),
				State:               batteryStatusString(status),
				Charged:             status == batteryStatusFullyCharged,
				Charging:            isChargingBatteryStatus(status),
			}

			applyPowerStatus(&info, powerStatus, powerOK)

			// Linha do WMI já é prova de presença física, mesmo que o
			// GetSystemPowerStatus não tenha sabido responder.
			if info.Present == nil {
				present := true
				info.Present = &present
			}

			items = append(items, info)
		}
	}

	// Fallback: sem WMI (ou WMI sem linhas), ainda dá para reportar "há bateria
	// e a máquina está na tomada/na bateria". Desktop (BatteryFlag 128) e SO que
	// não soube responder (255) não geram item — antes a checagem era
	// BatteryLifePercent <= 100, que tratava 255 como valor plausível.
	if len(items) == 0 && powerOK && powerStatus.BatteryKnown && powerStatus.HasBattery {
		info := models.BatteryInfo{}
		applyPowerStatus(&info, powerStatus, powerOK)
		if info.Present == nil {
			present := true
			info.Present = &present
		}
		if info.State == "" {
			info.State = batteryStateFromPower(powerStatus)
		}
		items = append(items, info)
	}

	return items, nil
}

// applyPowerStatus aplica o estado de energia do SO sobre a bateria. O que o
// WMI já respondeu com valor real é preservado; o resto é completado.
func applyPowerStatus(info *models.BatteryInfo, st power.Status, ok bool) {
	if !ok {
		return
	}

	if st.BatteryKnown {
		present := st.HasBattery
		info.Present = &present
	}

	info.OnAC = st.OnAC

	if st.Charging != nil && *st.Charging {
		info.Charging = true
	}

	// Percentual: só completa quando o WMI não trouxe valor plausível. Zero é
	// ambíguo (0% real × campo ausente), por isso só sobrescrevemos quando o SO
	// reporta carga maior que zero.
	if st.Percent != nil && (info.PercentRemaining <= 0 || info.PercentRemaining > 100) {
		info.PercentRemaining = *st.Percent
	}

	if info.MinutesUntilEmpty == 0 && st.MinutesLeft != nil {
		info.MinutesUntilEmpty = *st.MinutesLeft
	}
	if info.MinutesToFullCharge == 0 && st.MinutesToFull != nil {
		info.MinutesToFullCharge = *st.MinutesToFull
	}

	// Carregada: na tomada com carga cheia (o WMI nem sempre reporta
	// BatteryStatus 3, mesmo com a bateria em 100%).
	if !info.Charged && st.OnAC != nil && *st.OnAC && info.PercentRemaining >= 100 {
		info.Charged = true
	}

	if info.State == "" {
		info.State = batteryStateFromPower(st)
	}
}

// batteryStateFromPower deriva um rótulo legível quando o WMI não deu um.
func batteryStateFromPower(st power.Status) string {
	if st.Charging != nil && *st.Charging {
		return "Charging"
	}
	if st.OnAC != nil {
		if *st.OnAC {
			return "On AC"
		}
		return "Discharging"
	}
	return "Unknown"
}

func isChargingBatteryStatus(status int) bool {
	switch status {
	case batteryStatusCharging, batteryStatusChargingAndHigh, batteryStatusChargingAndLow, batteryStatusChargingCritical:
		return true
	default:
		return false
	}
}

func batteryStatusString(status int) string {
	switch status {
	case batteryStatusDischarging:
		return "Discharging"
	case batteryStatusOnAC:
		return "On AC"
	case batteryStatusFullyCharged:
		return "Fully Charged"
	case batteryStatusLow:
		return "Low"
	case batteryStatusCritical:
		return "Critical"
	case batteryStatusCharging:
		return "Charging"
	case batteryStatusChargingAndHigh:
		return "Charging and High"
	case batteryStatusChargingAndLow:
		return "Charging and Low"
	case batteryStatusChargingCritical:
		return "Charging and Critical"
	case batteryStatusUndefined:
		return "Undefined"
	case batteryStatusPartiallyCharged:
		return "Partially Charged"
	default:
		return ""
	}
}
